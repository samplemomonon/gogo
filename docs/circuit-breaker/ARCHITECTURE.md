# Circuit Breaker Architecture

Request flows, state machine and failure scenarios. For *what* is protected and
*how it is configured*, see
[`../CIRCUIT_BREAKER_IMPLEMENTATION.md`](../CIRCUIT_BREAKER_IMPLEMENTATION.md).

## System flow

The gateway holds four breakers for the product platform, not one. The diagram
shows browse (`product-listing`), which is the dependency that decides whether
the storefront can render at all:

```
┌──────────────────────────────────────────────────────────────────┐
│                    Load Balancer / Ingress                        │
│                 (health check: GET /readyz)                        │
└────────┬────────────────────────────┬────────────────────────────┘
         │                            │
    ┌────▼─────┐                 ┌────▼─────┐
    │ Gateway  │                 │ Gateway  │
    │ Pod 1    │                 │ Pod 2    │
    │ browse:  │                 │ browse:  │
    │ CLOSED   │                 │ OPEN  ❌ │  → /readyz 503 → drained
    └────┬─────┘                 └────┬─────┘
         │  search: CLOSED             │
         │  writes:  CLOSED            │
         └──────────┬──────────────────┘
                    │  gRPC (four separate breakers)
     ┌──────────────┴───────────────────────────────────────────┐
     │  product-platform namespace (another team, another repo)  │
     │                                                            │
     │  product-service   product-listing-service                │
     │  product-search-service   category-service                │
     └────────────────────────────────────────────────────────────┘

     gogo's own store (unaffected by any of the above):
     gogo-postgres — the inventory ledger and the sales history
```

Readiness aggregates every contributed breaker, so any one of them being open
takes that replica out. Half-open counts as ready — the breaker is probing, and
draining a recovering replica would keep it from ever recovering.

## State machine

Identical for every upstream; only the wrapped dependency differs.

```
                  ┌───────────────┐
                  │    CLOSED     │
                  │  (Normal ops) │
                  └───────┬───────┘
                          │
                          │ 5 consecutive *transport* failures
                          │ (a business rejection does not count)
                          ▼
                  ┌───────────────┐
                  │     OPEN      │◄─────┐
                  │ (Fast reject) │      │ probe fails
                  └───────┬───────┘      │
                          │              │
                          │ after 5s    │
                          ▼              │
                  ┌───────────────┐      │
                  │  HALF-OPEN    │──────┘
                  │ (1 probe)     │
                  └───────┬───────┘
                          │
                          │ probe succeeds
                          ▼
                  ┌───────────────┐
                  │    CLOSED     │
                  └───────────────┘
```

## Request flow

### Normal (CLOSED)

```
User → Gateway → grpcconsumer.Do → breaker(closed) → upstream → response
                                     │
                                     ├─→ successes++
                                     └─→ business rejection → failures NOT incremented
```

### Failure detected

```
User → Gateway → grpcconsumer.Do → breaker(closed) → upstream (transport error)
                                     │
                                     ├─→ failures++
                                     └─→ at 5: OPEN
```

### OPEN

```
User → Gateway → grpcconsumer.Do → breaker(OPEN) ─X─ no dial
                                     │
                                     ├─→ rejects++
                                     ├─→ returns breaker.ErrOpen (survives wrapping)
                                     ├─→ page maps it: browse outage → 503,
                                     │                     search outage → 200, search off
                                     └─→ /readyz → 503 → replica drained
```

### Recovery (HALF-OPEN)

```
User → Gateway → grpcconsumer.Do → breaker(half-open) → upstream (1 probe)
                                     │
                                     ├─→ success: CLOSED
                                     └─→ failure: OPEN for another 5s
```

## Readiness

```
GET /readyz
  │
  ├─→ registry.ReadyExcluding(ctx, "ops")     # every module's own health report
  │
  └─→ every contributed breaker.State()
       │
       ├─→ any == OPEN  → 503
       └─→ else         → 200
```

`ops` never names a specific upstream — the composition root contributes the
*group*. Adding a platform service is therefore one contribution plus one
`productconsumer` upstream constant; the readiness message does not change.

## Metrics pipeline

```
breaker.Breaker (state + counters)
  │
  ├─→ internal/shared/httpserver/metrics.go
  │     └─→ /metrics (Prometheus text, names sanitized to [a-zA-Z0-9_:])
  │     └─→ /debug/vars (expvar, raw upstream names)
  │
  └─→ Prometheus
       ├─→ circuit_breaker_alerts.yml → Alertmanager → PagerDuty/Slack
       └─→ Grafana: circuit_breaker_dashboard.json (one panel per signal,
            every upstream on each, legend names the dependency)
```

## Failure scenarios

### 1. One upstream replica dies (no effect)

```
Gateway-1 → product-listing-1 ❌ → failures++ → gRPC retries/load-balances
           → product-listing-2 ✓   → success
breaker stays CLOSED; nothing to do.
```

### 2. All replicas of one upstream die

```
Gateway-1,2,3 → product-listing (all down) → 5 failures each → OPEN
  │
  ├─→ /readyz 503 on all three → load balancer has nowhere to send traffic
  ├─→ any request that does arrive → 503 (not an empty grid)
  └─→ alert: ProductPlatformCircuitBreakerOpen (per replica)
             ProductPlatformCircuitBreakerOpenClusterWide (when >2)
```

### 3. Only the search service dies (the case per-upstream breakers exist for)

```
GET /?page=2  → browse CLOSED  → 200 with a working grid
GET /?q=ayam → search OPEN    → 200, search disabled, term preserved
GET /seller/produk (POST) → writes CLOSED → unaffected

Only the search breaker trips. A shared breaker would have taken all three.
```

### 4. The platform's own database is exhausted

```
Gateway → product-service → its pool full → transport error
  → 5 consecutive → OPEN → /readyz 503 → drained
  → alerts name the `product` upstream; the platform team owns the pool
```

## Configuration point

```go
// internal/shared/breaker/breaker.go
func DefaultConfig() (threshold int, timeout time.Duration, successesToClose int) {
    return 5, 5 * time.Second, 1
}
```

One definition, shared by every breaker. Not per-upstream and not
environment-tunable: a threshold that varies per deployment cannot be reasoned
about during an incident, and none of these upstreams justifies a different one.

## Why these choices

**Why does readiness fail when a breaker opens?** Without it, the load balancer
keeps routing to a replica that can only answer "no", and the outage reaches
users served by replicas that could have served them. With it, traffic
redistributes — or, if every replica is affected, the platform's own alerts fire
instead of a slow trickle of 503s from everywhere.

**Why does half-open count as ready?** A draining replica cannot recover: if
readiness only passes when closed, an instance whose upstream just came back
never receives the probe that would close its circuit.

**Why 5 failures?** A network blip is 1–2; five *consecutive* transport
failures indicates something systemic rather than a dropped packet.

**Why 5 seconds?** Long enough not to hammer a service that is restarting
(2–5s is typical), short enough that recovery is visible within a request's
patience.

**Why one breaker per upstream?** Because they fail independently and the user-
visible cost differs. This is the decision D-018 forced: four remote services
replaced one in-process module, and the blast radius question became real.

## Layers

```
internal/bootstrap/product_connection.go   dial the four connections, create the
                                           breakers, contribute them to readiness
        │
        ▼
internal/shared/productconsumer            per-upstream vocabulary + wire→Core,
                                           Client.Breaker(upstream)
        │
        ▼
internal/shared/grpcconsumer               the call convention: tenant stamp, run
                                           under the breaker, classify the failure,
                                           preserve breaker.ErrOpen
        │
        ▼
internal/shared/breaker                    the state machine and counters
        │
        ▼
internal/shared/httpserver/metrics.go      the exposition (sanitized names)
```
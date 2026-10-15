# Circuit Breaker Quick Reference

The gateway holds **one breaker per upstream**, not one for the deployment as a
whole. That is the fact this whole page hangs off: a product-platform outage
should degrade exactly the surfaces that depend on it, and nothing else.

| Upstream | `breaker_state` series | What it guards |
| --- | --- | --- |
| product | `gogo_product_breaker_*` | writes (seller product create/update/delete) |
| product-listing | `gogo_product_listing_breaker_*` | browse (the storefront grid) |
| product-search | `gogo_product_search_breaker_*` | search (`/?q=` with 3+ characters) |
| category | `gogo_category_breaker_*` | the category hierarchy |
| inventory, sales, auth | `gogo_<name>_breaker_*` | the in-process modules' contracts |

**Note the underscores.** The services are hyphenated (`product-listing`), but a
Prometheus metric name may only contain `[a-zA-Z0-9_:]`, so the exposition
sanitizes the hyphen. An un-sanitized name would make Prometheus reject the
*whole* `/metrics` scrape, not just those series. The real name is in the HELP
text.

## Endpoints

```bash
# Every breaker's state, as Prometheus text
curl -s http://gateway:8080/metrics | grep '_breaker_state'

# The same, as JSON via expvar (dev only unless ENABLE_DEBUG_ENDPOINT)
curl -s http://gateway:8080/debug/vars | jq '.product_listing_breaker'

# Readiness — aggregates every module health report AND every upstream breaker,
# so a gateway that cannot reach browse leaves the load balancer.
curl -s -o /dev/null -w '%{http_code}\n' http://gateway:8080/readyz

# Liveness — this process only; stays 200 through a dependency outage.
curl -s -o /dev/null -w '%{http_code}\n' http://gateway:8080/healthz
```

## Alerts

Defined in `deploy/prometheus/circuit_breaker_alerts.yml`; all four match **any**
of the product-platform series.

| Alert | Severity | For | Action |
|-------|----------|-----|--------|
| `ProductPlatformCircuitBreakerOpen` | critical | 1m | Find which upstream from the series; check that service in the `product` namespace |
| `ProductPlatformCircuitBreakerOpenClusterWide` | critical | 1m | A platform outage, not one pod — page the platform on-call |
| `ProductPlatformHighFailureRate` | warning | 2m | Pre-trip window: the dependency is degrading but still answering |
| `ProductPlatformBreakerRejectingCalls` | warning | 30s | Already down; the gateway is failing fast rather than timing out |
| `ProductPlatformBreakerTripped` | info | — | A trip happened; repeats mean a flapping dependency |

The two criticals are separated deliberately: one replica's broken connection
and a dead upstream need different responses, and one alert cannot tell them
apart.

## Breaker states

| State | Value | Meaning | Action |
|-------|-------|---------|--------|
| CLOSED | 0 | Healthy | Normal operation |
| OPEN | 1 | Failing | Upstream unavailable; calls are rejected without a dial |
| HALF-OPEN | 2 | Testing | One probe admitted; success closes, failure reopens |

## What a user sees per upstream

| Failing upstream | `/` (browse) | `/` (search) | `/seller` writes |
| --- | --- | --- | --- |
| product-listing | **503** — cannot answer honestly | works | works |
| product-search | works | search off, grid intact | works |
| product | works | works | 503 on the mutation, form preserved |
| category | category list empty | works | 503 on a category-bearing write |

This is why the breakers are per-upstream: a shared breaker would have made
every page 503 for any single outage, which is strictly worse for users and
tells an operator nothing about what actually broke.

## Quick troubleshooting

```bash
# 1. Which upstream is open, and on how many replicas?
for p in $(kubectl get pods -n gogo -l app.kubernetes.io/name=gogo-gateway -o name); do
  echo -n "$p: "
  kubectl exec -n gogo "$p" -- wget -qO- localhost:8080/metrics 2>/dev/null \
    | grep -E 'product(_listing|_search)?_breaker_state|category_breaker_state' \
    | tr '\n' ' '; echo
done

# 2. The platform's own health (it is a different namespace, different team)
kubectl get pods -n product -o wide
kubectl logs -n product -l app=product-listing --tail=50 --timestamps

# 3. Do the addresses still resolve from a gateway pod?
kubectl exec -n gogo deploy/gogo-gateway -- getent hosts product-listing.product.svc.cluster.local

# 4. Network policy still permitting egress to the platform namespace?
kubectl get networkpolicy -n gogo -o yaml | grep -A6 'namespaceSelector'
```

**Do not restart the gateway.** The breaker closes itself after one successful
probe (5s cooldown); a restart discards that state and sends a burst of
immediately-failing calls at a service that is already down.

## Configuration

`breaker.DefaultConfig()`: trip after **5** consecutive transport failures,
**5s** cooldown, **1** success closes. Not environment-tunable — a breaker
whose threshold is per-deployment becomes one nobody can reason about during an
incident.

Only **transport** failures count. A business rejection (`NotFound`, `Invalid`,
`Conflict`) is the dependency working correctly and must never open the circuit —
that mistake is what `grpcconsumer` exists to make impossible.

## Escalation

1. On-call SRE.
2. Platform on-call — for any of the four product upstreams (they are the
   platform team's deployments).
3. Infrastructure, for this gateway's own Postgres or the change feed.
4. Engineering manager, past 15 minutes.

## Related docs

- [`ARCHITECTURE.md`](../ARCHITECTURE.md) — D-018 (why the platform is remote)
  and the events section (the change feed that is *not* breaker-guarded).
- `docs/CIRCUIT_BREAKER_IMPLEMENTATION.md` — the breaker itself and the
  transport-vs-business classification.
- `docs/circuit-breaker/ARCHITECTURE.md` — deeper design notes.
- `deploy/prometheus/circuit_breaker_alerts.yml`,
  `deploy/grafana/circuit_breaker_dashboard.json` — the shipped rules and
  dashboard.
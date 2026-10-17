# Circuit Breaker

Operational documentation for the gateway's upstream breakers. See
[`../CIRCUIT_BREAKER_IMPLEMENTATION.md`](../CIRCUIT_BREAKER_IMPLEMENTATION.md)
for how the breaker works and [`ARCHITECTURE.md`](./ARCHITECTURE.md) for the
request flows.

## The short version

One breaker **per upstream** — `product`, `product-listing`, `product-search`,
`category`, plus the in-process `inventory`, `sales` and `auth`. After 5
consecutive *transport* failures a breaker opens, rejects calls without dialing,
and — for a required upstream — its gateway leaves the load balancer. After a
5-second cooldown one probe is admitted; success closes it, failure reopens.

Three things that are true here and usually are not:

1. **Business rejections never trip a breaker.** A `NotFound` is the dependency
   working. `grpcconsumer` makes the transport-vs-business split structural
   rather than a convention anyone has to remember.
2. **Readiness gates on every required breaker.** An open breaker on a required
   upstream means 503 on `/readyz`, so the instance stops receiving traffic.
   `search` is classified optional (the root's readiness policy), so its outage
   degrades search without draining the instance.
3. **A search outage does not take browse down.** The four product upstreams have
   separate breakers, so `/` still serves when search fails — at the page level
   and at the traffic level, because search is the one optional readiness
   upstream. When browse fails, `/` returns 503 rather than a blank grid, and
   readiness drains the instance.

## Metrics

```
gogo_<upstream>_breaker_state             # 0=closed, 1=open, 2=half-open
gogo_<upstream>_breaker_successes_total
gogo_<upstream>_breaker_failures_total
gogo_<upstream>_breaker_rejects_total     # refused without dialing
gogo_<upstream>_breaker_trips_total
```

`<upstream>` is the metric-safe form of the service name: `product-listing`
appears as `product_listing`, because a Prometheus metric name cannot contain a
hyphen and an invalid name fails the whole scrape.

```bash
curl -s http://localhost:8080/metrics | grep '_breaker_state'
curl -s http://localhost:8080/debug/vars | jq '.product_listing_breaker'
```

## Checking state during an incident

```bash
# Per replica, across the namespace
for p in $(kubectl get pods -n gogo -l app.kubernetes.io/name=gogo-gateway -o name); do
  echo -n "$p: "
  kubectl exec -n gogo "$p" -- wget -qO- localhost:8080/metrics 2>/dev/null \
    | grep '_breaker_state' | tr '\n' ' '; echo
done
```

Then the upstream itself — it lives in another namespace and belongs to another
team:

```bash
kubectl get pods -n product -o wide
kubectl logs -n product -l app=product-listing --tail=100 --timestamps
```

**Do not restart the gateway.** The breaker closes itself after one successful
probe; a restart throws that state away and sends a burst of immediately-failing
calls at a service that is already down.

## Alerts and dashboard

- `deploy/prometheus/circuit_breaker_alerts.yml` — five rules, all matching any
  product-platform series: `Open`, `OpenClusterWide`, `HighFailureRate`,
  `RejectingCalls`, `Tripped`.
- `deploy/grafana/circuit_breaker_dashboard.json` — four panels, each querying
  every upstream so the legend names the dependency.

## Testing it

```bash
./scripts/test-circuit-breaker.sh     # end-to-end against a running gateway
go -C apps/web test -race ./internal/shared/breaker/...
go -C apps/web test -race ./internal/shared/grpcconsumer/...
go -C apps/web test -race ./internal/shared/productconsumer/...
go -C apps/web test -race ./internal/shared/httpserver/...
go -C apps/web test -race ./internal/bootstrap/...
```

## Deployment

The alerts and dashboard are files, not cluster objects — apply or import them
into your monitoring stack:

```bash
# Prometheus rule file
kubectl apply -f deploy/prometheus/circuit_breaker_alerts.yml
# Grafana: import deploy/grafana/circuit_breaker_dashboard.json via the UI
```

Then configure notification routing, and confirm the alert path works by
simulating a failure — an untested alert is an assumption.

## Related

- [`../CIRCUIT_BREAKER_QUICK_REF.md`](../CIRCUIT_BREAKER_QUICK_REF.md) — the
  operations cheat sheet: states, per-upstream user-visible behaviour,
  troubleshooting, escalation.
- [`../CIRCUIT_BREAKER_IMPLEMENTATION.md`](../CIRCUIT_BREAKER_IMPLEMENTATION.md)
  — the sentinel, the classification, the metric-name sanitization, the tests.
- [`ARCHITECTURE.md`](./ARCHITECTURE.md) — request flows and failure scenarios.
- [`../../ARCHITECTURE.md`](../../ARCHITECTURE.md) — D-018 (why the product
  platform is remote, and why there are now four upstreams).

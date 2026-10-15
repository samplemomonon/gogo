# Circuit Breaker — Implementation Notes

How the breaker works, and what it is (and is not) responsible for.

## The one fact that shapes everything

The gateway holds **one breaker per upstream**, not one for the platform:

| Upstream | Guards | `breaker_state` series |
| --- | --- | --- |
| product | seller writes | `gogo_product_breaker_*` |
| product-listing | browse / the storefront grid | `gogo_product_listing_breaker_*` |
| product-search | search | `gogo_product_search_breaker_*` |
| category | the category hierarchy | `gogo_category_breaker_*` |
| inventory, sales, auth | the in-process modules' contracts | `gogo_<name>_breaker_*` |

These became four separate upstreams in [D-018](../ARCHITECTURE.md#d-018), when
the product domain left this process. Before that there was one `catalog`
upstream and the question never arose. It arises now because the platform's four
contracts fail independently, and lumping them would mean a search outage
taking the browse grid down with it — strictly worse for users and useless to an
operator.

## Where it lives

| Concern | Package |
| --- | --- |
| The breaker itself (state machine, counters) | `internal/shared/breaker` |
| The call convention: tenant stamp, breaker, error classification | `internal/shared/grpcconsumer` |
| Per-upstream error vocabulary + the wire→`Core` mapping | `internal/shared/productconsumer` |
| Metrics exposition | `internal/shared/httpserver/metrics.go` |
| Readiness aggregation over every breaker | `internal/modules/ops` |
| Wiring (dial, create, contribute) | `internal/bootstrap/product_connection.go` |

`productconsumer` exposes `Client.Breaker(Upstream)`; `ops` never reaches for a
specific one — it asks for the **group of contributed breakers**, so adding an
upstream is one contribution in the composition root and neither `ops` nor its
readiness message changes.

## Transport vs business failure

The load-bearing rule: **only a transport failure counts against the breaker.**
A `NotFound`, `Invalid` or `Conflict` answer is the dependency working exactly
as intended.

This is the mistake that is easy to make by hand and expensive in production —
open a breaker because users searched for a product that does not exist, and the
storefront goes down for everyone. `grpcconsumer` makes it structurally
impossible: a consumer cannot construct a call that omits the classification,
because the mechanics live in one place and the kits call `Do`/`DoUnit`.

The consequence worth knowing: a dependency returning `Internal` for every call
(a server-side bug, not an outage) *does* count as a failure, because nothing on
the wire distinguishes the two. That is the right default — an upstream failing
every call is a problem whether or not the status code was polite.

## The outage sentinel

`breaker.ErrOpen` means "I did not attempt this". It must survive wrapping so
`errors.Is` still finds it three layers up, where a page decides between 503 and
200. `grpcconsumer` guarantees this: the sentinel is returned, not converted.

Without it, an open breaker looks like any other error and pages guess — which
is exactly the case where a 200-with-an-empty-grid is the worst possible answer.

## Configuration

`breaker.DefaultConfig()` → trip after **5** consecutive transport failures,
**5s** cooldown, **1** success closes.

Not environment-tunable, deliberately. A breaker threshold that varies per
environment is one nobody can reason about mid-incident, and the values are not
load-bearing enough to justify that.

## Metrics

```
gogo_<upstream>_breaker_state             # gauge: 0=closed, 1=open, 2=half-open
gogo_<upstream>_breaker_successes_total   # counter
gogo_<upstream>_breaker_failures_total    # counter
gogo_<upstream>_breaker_rejects_total     # counter — refused without dialing
gogo_<upstream>_breaker_trips_total       # counter — times it opened
```

**Metric names are sanitized.** The services are hyphenated; a Prometheus metric
name may only contain `[a-zA-Z0-9_:]`. An un-sanitized name would make
Prometheus reject the **entire scrape**, taking every other series with it — so
`metrics.go` maps `-` → `_` and keeps the real service name in the HELP text.
`TestPrometheusExpositionUsesValidMetricNames` pins this, because the failure
mode is invisible in a unit test that only greps for a substring.

The expvar snapshot (`/debug/vars`) exposes the same counters as JSON under
`gogo_http`, using the raw upstream names.

## What the breaker does not cover

- **The product change feed.** `productevents` consumes Kafka and skips
  undecodable records rather than failing; a stale island cache until the TTL is
  the accepted degradation, and `KAFKA_BROKERS` is optional for the same reason.
- **The in-process modules.** No network, no dial timeout, no cascade. Their
  breakers exist so `/metrics` and readiness are uniform across every
  contribution, not because a local function call needs one.
- **Correctness.** A breaker that is closed and the upstream is answering slowly
  will still time out. Timeouts are per-call; the breaker only stops a *known*
  bad dependency from being called again.

## Tests

```bash
go test -race ./internal/shared/breaker/...
go test -race ./internal/shared/grpcconsumer/...
go test -race ./internal/shared/productconsumer/...
go test -race ./internal/shared/httpserver/...
go test -race ./internal/bootstrap/...
```

The interesting ones:

- `internal/shared/breaker` — the state machine's transitions, including
  half-open probe accounting.
- `internal/shared/grpcconsumer` — that a business error does not open a breaker
  and that `ErrOpen` survives wrapping.
- `internal/shared/productconsumer` — one breaker per upstream, and that mapping
  failures are classified per upstream.
- `internal/shared/httpserver` — the exposition is always a *valid* Prometheus
  scrape, and every configured upstream appears.
- `internal/bootstrap` — the four connections, and that readiness aggregates all
  four breakers (`fakestack_test.go`, `productplatform_integration_test.go`).

## Shipped monitoring

- `deploy/prometheus/circuit_breaker_alerts.yml` — five alerts, all matching any
  product-platform series. See
  [`CIRCUIT_BREAKER_QUICK_REF.md`](CIRCUIT_BREAKER_QUICK_REF.md) for what each
  one is for.
- `deploy/grafana/circuit_breaker_dashboard.json` — four panels (state, call
  outcomes, success rate vs a 95% SLA line, trips), each querying every upstream
  so a legend entry names the dependency instead of the metric.

## Deployment checklist

- [ ] Gateway image with breaker metrics deployed.
- [ ] `circuit_breaker_alerts.yml` imported; notification routing configured.
- [ ] Grafana dashboard imported.
- [ ] On-call trained on the quick reference — **especially** "do not restart the
      gateway": a restart discards half-open state and re-hammers a service that
      is already down.
- [ ] Alert workflow tested against a simulated upstream failure.
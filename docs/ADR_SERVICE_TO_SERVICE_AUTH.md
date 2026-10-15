# ADR: The platform services authenticate their callers

**Date:** 2026-10-15
**Status:** Accepted. Follows `ADR_AUTH_AT_THE_COMPOSITION_LAYER.md`.
**Owner:** project owner.

## The finding

The previous ADR authenticated the composition layer. Three days of reading
later, that control was found to be decorative.

Measured, not inferred:

```
$ grep -rl 'Authorization|Bearer|JWT|OAuth' --include=*.go   (production)
0
```

Still zero — but the shape of the problem had changed. The aggregator now
requires a credential for its mutations. `product-service`, which holds the ten
routes that write the catalogue, listens on host ports `8082` (HTTP) and `9092`
(gRPC) with `http.NewServeMux()` and no middleware at all:

```
POST   /api/v1/products          CreateProduct
PUT    /api/v1/products/{id}     UpdateProduct
DELETE /api/v1/products/{id}     DeleteProduct
```

So the bypass was one command:

```
curl localhost:8082/api/v1/products -d '{...}'      # writes, no token
```

Everything the aggregator's `MUTATION_TOKENS` protects is reachable directly.
"One point closes all APIs" was true only of the paths that point went through.

## The decision

**Every platform service refuses callers that do not present a credential it was
configured to accept, on both transports. A service with no credential
configured does not start.**

| Service | Inbound | Outbound |
|---|---|---|
| aggregator | `MUTATION_TOKENS` (existing) | `DOWNSTREAM_SERVICE_TOKEN` (new) |
| product | `INBOUND_SERVICE_TOKENS` (new) | — |
| product-listing | `INBOUND_SERVICE_TOKENS` (new) | — |
| product-search | `INBOUND_SERVICE_TOKENS` (new) | — |

Fail-closed at startup, for the same reason as before: an absent variable must
mean *refuse every caller*, never *admit every caller*. A token list may be empty,
short, or duplicated, and each is rejected at load rather than at the first
request.

The credential is a shared secret rather than an identity. It answers "may this
caller talk to me" and nothing more — there is still no principal, no
per-caller authorisation, and no audit of who changed what.

## What this protects, and what it does not

**It protects against reachability.** A caller who can reach a port but did not
come through the composition layer is refused. That is the actual failure: the
services publish host ports in development, and an unauthenticated write reached
the catalogue.

**It does not protect against an on-path attacker.** The gRPC connections use
`insecure` transport credentials, so the bearer token travels in the clear. This
is the one place in the repository where a credential is knowingly sent
unprotected, and it is recorded as such in `serviceauth.RequireTransportSecurity`.
Closing it means TLS, and on a service mesh mTLS so the peer is authenticated by
certificate rather than by a shared secret. That is a separate decision and is
not taken here — but "not taken" is not "not real", so it is written down rather
than left to be discovered.

## The finding worth more than the fix

Every guard here is installed in a composition root, and **the composition root
was not covered by any test.** Verified by sabotage:

| Sabotage | Suite result, before the fix |
|---|---|
| Remove the gRPC interceptor from `product-service` | **0 — passed, unguarded** |
| Remove the HTTP guard from `product-service` | **0 — passed, unguarded** |
| Remove `/health` from the exempt list | **0 — passed; probes would answer 401** |
| Neutralise the guard in product-listing / product-search | **0 — passed, unguarded** |

The `serviceauth` package's own tests all passed throughout, because they test
the mechanism. Deleting the mechanism's *installation* is invisible to them.

Three extractions closed it, each verified by re-running the same sabotage:

- `grpcServerOptions(auth, log)` — a real server, started with exactly the
  options production passes, reached over `bufconn`.
- `wrapHandler` / `wrapChain` / the `middlewareChain` signature — the chain is
  reachable from a test without a wired `di.App` or database.
- `guardedHandler(mux, tokens)` and `probePaths` as package values — the probe
  exemption is read by the test from the same value production passes, rather
  than restated, so the two cannot drift.

The probe exemption is the one that would have hurt most. An orchestrator that
receives 401 from a liveness probe restarts the container, and restarting the
process that was answering the probe turns a partial outage into a total one.
`TestProbesStayOpenThroughTheRealGuard` reads `probePaths` — production's own
slice — so deleting `/health` from it fails the suite.

## Consistency, and why the read models were included

product-service is the only service with writes, so it is the only one where the
bypass was a catalogue corruption. product-listing and product-search are
read-only projections.

They were guarded anyway. Leaving two of four services unauthenticated produces a
posture nobody can reason about — "is this port protected?" has to be answered
per service rather than once — and the next service written would have no
precedent to copy. The rule is now uniform: every platform service, every
transport, fail closed.

## Consequences

- **Every deployment of every platform service must set `INBOUND_SERVICE_TOKENS`,
  and the aggregator `DOWNSTREAM_SERVICE_TOKEN`.** Intended. A deployment that
  has never chosen a credential should not start.
- **`TestLoadWithoutAnyEnvironment` was inverted in all three services.** It
  asserted that a service boots with nothing configured, so it can come up before
  its dependencies. That is still true of every other setting. It is no longer
  true of the credential, and a service that boots with nothing configured and
  then answers `POST /api/v1/products` for anyone is the failure this ADR closes.
- **gRPC health is not covered by a test here.** It is answered by gRPC before
  interceptors run, so the chain cannot affect it, and covering it would have
  meant extracting the health registration out of `buildGRPCServer` as well.

# Performance baseline

**Status:** baseline not measured yet. No latency, throughput, or resource target is set by this document.

**Implementation snapshot (2026-10-16):** local `docker compose ps` returned no running services. No production or staging workload, regional path, or representative dataset was available in this workspace, so a runtime capacity baseline cannot be honestly recorded yet.

The repository exposes Go HTTP request duration and request counters at `/metrics`, and the gateway has request timeouts and structured slow-request logging. Those are measurement surfaces, not a baseline. Fragment-level surfaces exist as well: every island render reports `Server-Timing` and aggregates into the expvar `gogo_render_islands` (renders, fallbacks, timeouts, errors, durations keyed by island family), and real-browser web vitals (LCP/INP/CLS/FCP/TTFB) are beaconed to `POST /rum` and aggregated per island family and rating in `gogo_rum_web_vitals` (unsampled; no cap on beacon volume beyond the 4 KiB body). There is no checked-in representative traffic profile, production trace, or captured run from which to infer national-scale targets.

## Measurement contract

Before changing caches, concurrency, database pools, resource requests, or service boundaries, capture the following against a named Git revision and deployment environment:

| Field | Required value |
|---|---|
| Revision / dirty state | `git rev-parse HEAD` and `git status --short` |
| Environment | local, staging, or production; include region and network path |
| Workload source | replay file or load-generator revision; identify endpoint mix and request rate source |
| Test duration | warm-up and steady-state interval |
| Client shape | concurrency, connection reuse, payload sizes, geographic/network location |
| Data shape | anonymized dataset size and tenant distribution; never use production PII in a test fixture |
| Results | request rate, p50/p95/p99, error rate, CPU, memory, GC, DB pool wait, upstream latency |
| Reproducibility | commands, tool versions, dashboards/query export, and artifact location |

Run a low-risk staging capture first. Increase offered load in documented steps and stop at the first sustained error-rate increase, saturation signal, or agreed operational abort threshold. The operator supplying the workload owns the abort threshold; it is not inferred here. Record cold-start and warmed results separately. Repeat enough times to report run-to-run variance.

## Current baseline register

| Surface | Current evidence | Missing evidence |
|---|---|---|
| Gateway HTTP | Prometheus instrumentation and slow-request logging exist in the codebase | Captured latency distribution and representative endpoint mix |
| Fragment rendering (server) | Per-island `Server-Timing` on responses; expvar `gogo_render_islands` (renders, fallbacks, timeouts, errors, durations by island family) exported at `/metrics` | Per-island duration distribution, fallback/timeout rates, and cache-hit ratio under a representative workload |
| Field web vitals (client) | `POST /rum` beacon receiver; expvar `gogo_rum_web_vitals` (LCP/INP/CLS/FCP/TTFB by island family and rating); vendored web-vitals v4.2.4 attribution build | Field data from real sessions; sampling policy and expected-volume evidence before configuring sampling (ADR trigger) |
| Dependencies | Per-upstream breakers/readiness and remote gRPC clients exist | Per-dependency latency/error baseline under an agreed traffic profile |
| Runtime | Container and Kubernetes resource configuration exist | Observed CPU/memory/GC at known load; no resource optimization is justified yet |
| Database | Pool configuration is explicit | Pool wait, query latency, and realistic data-volume profile |

## Target setting

After the first representative capture, owners should agree SLOs by user journey and write them in `docs/PERFORMANCE_BASELINE.md` with the supporting evidence. Do not use platform-comparison traffic numbers as a substitute for this product's workload. Keep baseline artifacts out of Git if they contain sensitive operational details; commit only sanitized summaries and reproducible commands.

## Change review

Any performance change records before/after results on the same workload and environment, the changed resource cost, and the correctness trade-off (freshness, consistency, or availability). A result without the workload and revision metadata above is not comparable and must not be presented as a measured improvement.

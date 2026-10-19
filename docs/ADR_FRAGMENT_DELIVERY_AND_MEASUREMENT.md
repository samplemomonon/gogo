# ADR — Fragment Delivery: Streaming, Fault Tolerance, and Measurement

**Status:** Accepted — owner-ratified 2026-10-17; implementation tracked in [`IMPLEMENTATION_LOG.md`](./IMPLEMENTATION_LOG.md)
**Date:** 2026-10-17
**Decision owner:** project owner (options and semantics ratified in review; the agent authored this record, not the decision)
**Evidence base:** templ v0.3.1020 source pinned in `apps/web/go.mod`, the code cited in §2, and the published fragment systems in §11
**Supersedes:** nothing; extends [`DECISION_UI_OWNERSHIP.md`](./DECISION_UI_OWNERSHIP.md) and implements the composition rules of [`ADR_CROSS_TEAM_UI_CONTRACT.md`](./ADR_CROSS_TEAM_UI_CONTRACT.md)

---

## 1. Context

The repository already composes pages from independently-owned islands: each island renders in isolation to `[]byte`, is planned by its rendering contract (static / revalidate / dynamic / client), may be cached and coalesced, and is assembled into one document (`render/composer.go:70-92`). What it does **not** have is any of the three properties the ratified scope requires:

1. **Progressive delivery.** Every byte of a composed document waits for the slowest island. There is no streaming: `templ.Flush` and `WithStreaming` appear nowhere in the repository (verified by source search).
2. **Fault tolerance per fragment.** One island returning an error aborts the whole page (`renderComponent` error propagates out of `RenderPage`); a slow island has no timeout. The failure containment ESI, Tailor and Podium all implement for their fragments has no counterpart here.
3. **Measurement per fragment.** Neither server-side island timings nor client-side web-vitals attributed to an island exist. `PERFORMANCE_BASELINE.md` documents a measurement contract with an explicitly unmeasured baseline.

The owner ratified the scope for this iteration as: (1) streaming + fault tolerance, (2) per-fragment measurement on both sides of the wire, and (3) completion of the ownership catalog. This ADR records the decisions and their evidence before implementation, per repository rules.

### 1.1 Constraints inherited, not re-litigated

- Fragment registry remains deferred (`DECISION_UI_OWNERSHIP.md:63`); pages declare islands directly.
- Server-side composition only. The Netflix SDUI warning cited at `DECISION_UI_OWNERSHIP.md:180` — do not ship a general server-driven UI protocol to the client — is respected: web-vitals attribution uses a bounded, closed vocabulary (§5.3).
- Ownership is derived from [`TEAM_TOPOLOGIES_PRD.md`](./TEAM_TOPOLOGIES_PRD.md); no org data exists in this repository, so no `CODEOWNERS` is created (`TEAM_TOPOLOGIES_PRD.md:37`).

---

## 2. Facts from this repository (verified before deciding)

### 2.1 Composition, delivery and failure (today)

| Fact | Evidence |
|---|---|
| Every island pre-renders to `[]byte` before the layout runs; the document is written once, in full | `render/composer.go:70-92` (`RenderPage`) |
| First island error aborts the document; there is no per-island timeout or fallback path for server modes | `render/composer.go:77-81`; `Component` fields `render/component.go:7-27` |
| `Fallback` exists only for Client-mode placeholders, rendered while the browser fetches `FetchURL` | `render/composer.go:658` (`renderClientPlaceholder`) |
| The HTTP status and `Content-Type` are written **before** any component renders | `render/render.go:23-25` (`HTMLStatus`) |
| `Vary` is set only by `render.Respond`; pages composed through the composer (pagekit) never set it | `render/strategy.go:61-66`; pagekit calls `RenderNavigation` directly |
| A cache-hit fragment fast path writes raw bytes with no response headers of its own | `render/composer.go:631` (`CachedIsland`) |
| PDP bypasses the composition layer: it renders its own template around a hand-rolled buffer | `internal/features/home/ui/handlers/products/detail/handler.go` |
| The composer's only consumer today is the home listing (pagekit). Billing and generated modules use `render.Respond` + `shell.Page`; seller renders a whole page via `render.HTML` | source search across `internal/features/**` |

### 2.2 templ mechanics relevant to streaming (v0.3.1020 runtime)

| Fact | Evidence |
|---|---|
| `templ.Flush()` renders its children and then flushes the writer if it implements `Flush()` or `Flush() error` | templ `flush.go` (`FlushComponent.Render`) |
| The runtime exposes a public `Buffer` type: `Flush()` flushes its `bufio.Writer` and then invokes `Flush` on the underlying `http.ResponseWriter` when it implements `http.Flusher` | templ `runtime/buffer.go` |
| Generated code calls `templruntime.GetBuffer(w)`: it **reuses** `w` when `w` is already a `*runtime.Buffer`, and only wraps-and-defers-release otherwise | `shell/layouts/base_templ.go:36-44` |
| Child components rendered from generated code receive the `*runtime.Buffer` itself, not the raw writer | `shell/layouts/base_templ.go:76` (`partials.Header(...).Render(ctx, templ_7745c5c3_Buffer)`) |
| `WithStreaming` exists only on `templ.Handler(ComponentHandler)`; it is not needed to obtain flush behaviour when the caller owns the buffer | templ `runtime/handler.go`; `flush.go` |
| Generated files in-repo are v0.3.1001 (CLI locally at v0.3.1001); the runtime `go.mod` pins v0.3.1020 | headers of `*_templ.go`; `apps/web/go.mod` |

**Consequence.** A component that holds the `*runtime.Buffer` can flush the socket at arbitrary points while the layout renders, without `WithStreaming` and without emitting flush markers in `.templ` files. This is the mechanism §3 selects.

### 2.3 HTMX semantics

| Fact | Evidence |
|---|---|
| Fragment responses are swapped atomically: htmx replaces the target with the complete response after it arrives | htmx docs, `hx-swap` (§11) |
| Multi-region updates in one response require `hx-swap-oob` | htmx docs (§11) |
| Once the first byte of a streamed response is flushed, status and headers are locked | Next.js streaming guide (§11) |

### 2.4 Measurement surfaces already present

| Fact | Evidence |
|---|---|
| Metrics are stdlib-only: `expvar` counters rendered as Prometheus text, with a deliberate bounded-cardinality label policy | `internal/shared/httpserver/metrics.go` |
| A render-cache expvar already exists (`gogo_render_cache`) | `render/cache.go` |
| Island names are a curated, closed, owner-namespaced set (`ValidateIslandName` panics on violation) | `render/component.go:33-38`, `islandname.go`, IMPLEMENTATION_LOG §14 |
| Client islands already render `id="island-NAME" data-island="NAME"`; the `class="island"` value has no CSS rule (inert) | `render/composer.go:658`; `.island` does not appear in `assets/css/app.css` |
| Static assets are vendored files served from `go:embed`, mounted at `/assets` (htmx is the precedent) | `assets/assets.go`; `shell/layouts/base.templ:48,76` |
| There is no app-JS bundler; the root `package.json` only builds Tailwind CSS | root `package.json` |

### 2.5 Ownership catalog

| Fact | Evidence |
|---|---|
| `team_catalog.json` + validator (`kind`, `tier`, `context`, `complexity`, `rung`) exist; the pilot scope is `seller-stock-adjustment` only | `internal/architecture/team_catalog.json`, `cmd/teamcatalog/main.go`, root `Makefile:113` |
| The validator checks listed paths exist and contract edges against the committed import graph | `cmd/teamcatalog/main.go` |
| The import graph is committed; architecture tests flag drift until regenerated | `internal/architecture/import_graph.txt`, `architecture_test.go` |

---

## 3. Decision 1 — streaming mechanism

### 3.1 Options

| | A · `templ.Handler(WithStreaming)` | B · composer-owned `*runtime.Buffer` **(chosen)** | C · `@templ.Flush()` inside `.templ` |
|---|---|---|---|
| Fits this system | No — there is no single `templ.Component` handler; the document is composed per request from `Page.Components` with per-island policy, cache and fallback | Yes — the composer already owns the render loop | No — flush points would live in templates, but which islands exist is a runtime decision; two sources of truth |
| Mechanics | Handler-owned buffer and flush | Composer holds the buffer; flushes between islands | Template authors must know composition |
| Evidence | templ `runtime/handler.go` | `base_templ.go:36-44,76`; templ `runtime/buffer.go` | templ `flush.go` |

### 3.2 Decision

**Streaming applies to full documents only** (`Page.Streaming` is set by the handler that serves a navigation; HTMX fragment responses stay buffered because their swap is atomic by contract).

When streaming is on and the writer is flush-capable, `RenderPage` renders the layout into a composer-owned `*runtime.Buffer` obtained via `templruntime.GetBuffer(w)` (reusing the caller's buffer when `w` already is one, exactly as generated code does). The composed islands are wrapped in one `templ.Component` that, while the layout renders, for each island:

1. renders the island to isolated `[]byte` (per-island atomicity — identical to today's behaviour, keeping cache/singleflight/fallback semantics intact),
2. writes the island's bytes into the buffer in document order,
3. calls `Buffer.Flush()`, pushing the buffer to the socket.

Delivery order is document order, sequential. Out-of-order patching (the React `Suspense` model) requires a client runtime this stack deliberately does not have; htmx could emulate it with `hx-swap-oob` streams, but that buys nothing user-visible for a page whose slow region is one listing grid. Recorded as a trigger in §10.

**Status and headers.** The first flushed island locks them. Therefore the composer sets `Content-Type` (if unset), `Vary: HX-Request`, and `Server-Timing` (trailer — §5.1) **before** the first flush. Buffered responses are unaffected: bytes reach the socket only after the full document succeeds, and errors before the first flush still produce a clean error response with no partial HTML.

**Buffer lifecycle.** The composer releases the buffer (flush + pool return) on success only; on error before the first flush it discards it, so a failed composition never leaks a partial document. When the caller passed a `*runtime.Buffer` (nested composition), the composer neither releases nor flushes past what the caller owns.

### 3.3 Consequences of the mechanism choice

- No `.templ` file needs a flush marker; page templates stay free of delivery concerns.
- Works with the checked-in v0.3.1001 generated code (verified pattern above), and with v0.3.1020. Regenerating templates with the installed v0.3.1001 CLI keeps the tree consistent; that is a routine `templ generate`, not a rendering-semantics change.
- Streaming engines other than this one are not possible by accident: only `RenderPage` implements the buffer dance.

---

## 4. Decision 2 — fault tolerance: explicit fallback and timeout per island

### 4.1 API

`Component` gains two fields; `Fallback` is re-documented to cover both of its now-honest meanings:

| Field | Meaning |
|---|---|
| `Load func(ctx) (templ.Component, error)` | Server-side data loading owned by the island. Optional; when nil, `Body` is rendered exactly as today. The generated code of the island's own template checks `ctx.Err()` at entry; `Load` must pass `ctx` to its I/O (gRPC/HTTP), so timeout cancellation reaches the real work. |
| `Timeout time.Duration` | Per-island budget. Explicit value honoured in every mode. When zero: **streaming islands default to 3000 ms** (Tailor's documented default, §11); buffered islands keep today's no-timeout behaviour (trigger in §10). |
| `Fallback templ.Component` | (existing field, extended doc) Client-mode placeholder **and** the server-mode substitute rendered when `Load` or the island render fails or times out. Not cached. |

### 4.2 Semantics

- **Containment.** An island failure renders `Fallback` in place — the page continues. This is the ESI `onerror="continue"` / Tailor `fallback-src` behaviour for the case where the substitute is known locally.
- **No fallback, no silence.** `Fallback == nil` on failure returns the error to the composer: buffered mode → clean error response (nothing flushed); streaming mode → if the failure lands before the first island flush the response is still clean, after it the response is truncated — a constraint inherited from HTTP (Next.js documents the same). The contract for streamed islands is therefore: **streamed islands should declare a fallback**; a streamed island with no fallback is a wiring smell visible in code review and in metrics.
- **Parent cancellation is not an island failure.** If the request context itself is done (client left), the composer aborts; it does not paint fallbacks into a gone connection. Only island-scoped errors (load error, render error, island deadline) trigger the fallback.
- **Fallback context.** The fallback renders with `context.WithoutCancel` applied to the island's timed context — values (tenant, identity) preserved, the expired island deadline dropped. A fallback that itself enforces deadlines re-derives them.
- **Not cached.** A fallback is transient failure state; it is never stored in the island cache, so the next request retries the real render. Coalesced renders store only successful bytes (existing generation guard applies unchanged).
- **Visible degradation.** Rendering a fallback increments `gogo_render_islands{island="…",result="fallback"}` and the island's `Server-Timing` entry is marked `;desc="fallback"`. Silent degradation is forbidden; the owner sees it in the same dashboards as errors.

### 4.3 Failure taxonomy (recorded for evaluators)

| Failure | Buffered | Streaming, before first flush | Streaming, after first flush |
|---|---|---|---|
| Island load/render error, fallback set | fallback rendered, page 200 | fallback rendered, page 200 | fallback rendered, truncated? no — fallback bytes flushed, page 200 |
| Island load/render error, no fallback | error out, clean error response | error out, clean error response | error out, response truncated |
| Island deadline (timeout), fallback set | fallback rendered | fallback rendered | fallback rendered |
| Parent context cancelled | abort, no fallback | abort, no fallback | abort, no fallback |

---

## 5. Decision 3 — per-fragment measurement

### 5.1 Server side

- Every server-mode island render (buffered or streamed) is timed; entries are emitted as `Server-Timing` (`island-<name>;dur=<ms>[;desc="fallback"]`), with characters outside the HTTP token charset replaced by `_` (paged names carry `:`, which tokens do not allow). Client-mode placeholders are deliberately not timed or counted: their server work is trivial and constant, and their names may embed ids (`detail.<id>.stock`), which would add an unbounded label set.
- Buffered responses: `Server-Timing` is set as a **header** by the composer before the first write.
- Streamed responses: timings are not known when headers lock, so the composer announces `Trailer: Server-Timing` and sets the value after the last island — the W3C Server-Timing document itself shows the trailer form.
- Aggregates live in expvar `gogo_render_islands` — `island family → {renders, fallbacks, timeouts, errors, duration_seconds}` — rendered by the existing Prometheus text handler (`httpserver/metrics.go`). The family is the name before the first `:`; cardinality is bounded because paged listings (`home.product-grid:t<hash>:p3`) aggregate per island even though the search term is attacker-controlled.

### 5.2 Client side — web-vitals with island attribution (owner-ratified “complete”)

- **Vendored, pinned, no third-party network.** `web-vitals.attribution.iife.js` (Apache-2.0, license header preserved) is vendored under `assets/js/` and served from `/assets`, exactly like htmx. No CDN request at runtime.
- **Beacon.** An inline boot script (a constant alongside `FormGuardJS` / `HtmxConfigJS`) registers the attributed callbacks and reports via `navigator.sendBeacon("/rum", blob)` (JSON body; `fetch keepalive` fallback), only when `document.visibilityState` reporting is due (web-vitals’ own lifecycle). Payload: `{metric, value, rating, delta, id, navigationType, island, path}` — no cookies, no identifiers, no user agent.
- **Attribution.** For LCP and INP the web-vitals attribution build exposes the candidate element; the boot script walks `element.closest("[data-island]")` and sends the island name (or `"page"` when absent).

### 5.3 Markers

- Client islands already carry `data-island` (`render/composer.go:658`).
- Server islands are wrapped in `<div class="ds-island" data-island="<name>">…</div>`, with one CSS rule: `.ds-island { display: contents }`. The wrapper therefore generates no box and cannot participate in layout — grid/flex children keep their positions — while remaining a DOM-level attribution anchor. If an island root already carries its own visible box, nothing changes visually; if it was expected to be the direct child of a grid, `display: contents` keeps that true.
- `display: contents` on a plain `div` removes the box only; children and their semantics are untouched (CSS Display §11). Attribute `data-island` is matched case-insensitively as HTML attributes always are.

### 5.4 Endpoint `POST /rum`

- Method-restricted; JSON only; body capped at 4 KiB.
- `island` is normalized to a bounded label before aggregation: `"page"` and an empty value stay `"page"`; a value outside the island-name grammar is `"unknown"`; a valid name aggregates under its *family* — the part before the first `:`, which drops paged coordinates and cache keys — and digit-only dotted segments collapse to `{id}` (`detail.1042.stock` → `detail.{id}.stock`, so per-product names stay one series). A cap of 64 distinct families bounds the aggregate map regardless; everything past it is `"unknown"` (the count is itself the drift signal, so a drift is visible without unbounded cardinality).
- `path`, `delta`, `id` and `navigationType` are accepted for wire compatibility with the vendored build and deliberately neither stored nor logged: the aggregate schema is `metric|island|rating`. The raw IP is not recorded beyond the request log line that every request already produces.
- Aggregates: expvar `gogo_rum_web_vitals` — `metric|island|rating → {count, value_sum}` → `/metrics`.
- No auth: the endpoint is a beacon receiver with no write access to anything; its exposure is bounded by body cap, validation, and the aggregation schema above. Sampling configuration is deliberately absent until beacon volume (visible in the same metrics) justifies it — trigger in §10.

---

## 6. Decision 4 — ownership catalog completion

Basis: [`TEAM_TOPOLOGIES_PRD.md`](./TEAM_TOPOLOGIES_PRD.md) §6.4/§7 and [`ADR_CROSS_TEAM_UI_CONTRACT.md`](./ADR_CROSS_TEAM_UI_CONTRACT.md) §4.3. Every unit below is a real path in this repository; the validator will enforce it.

| Unit | Kind | Team | Tier | Basis |
|---|---|---|---|---|
| `internal/features/home/ui` | page | T3 | business | PRD §6.4 (storefront surface) |
| `internal/features/sales/ui` | page | T2 | business | PRD line 209 (merchant console) |
| `internal/features/auth/ui`, `internal/features/profile/ui` | page | T4 | business | PRD §6.4 (identity surfaces) |
| `internal/features/auth` | module | T4 | business | PRD §6.4 |
| `internal/shared` (incl. `internal/shared/authconsumer`) | module | T5 | foundation | PRD §6.4; authconsumer carve-out decision recorded here: it remains under the shared module and is **not** a contract of T4 |
| `internal/features/billing` | module | T5 | business | PRD line 316 (rung 1) |
| `internal/features/ops` | module | T5 | business | **derived**: readiness aggregation + circuit-breaker surface are platform-operational capabilities, not merchant surface (basis: `internal/features/ops` purpose docs, `CIRCUIT_BREAKER_IMPLEMENTATION.md`) |
| `internal/features/contentcms/ui` | page | T5 | business | **derived**: a platform-wide content surface; no PRD unit claims it (basis: `CMS_CONTENT_MODEL.md` framing as a shared editorial surface) |
| `cmd/**` | composition | T5 | foundation | PRD §6.4 (bootstrap/entrypoints) |
| `design-system/web/ui` | module | T5 | foundation | ADR_CROSS_TEAM §4.3 (platform owns primitives) |

**Moved by [D-023](../ARCHITECTURE.md#d-023):** every unit above was re-homed
under `internal/features/**` (and `internal/bootstrap` became `internal/shell`);
the enforced source of truth is the updated
`apps/web/internal/architecture/team_catalog.json`.

Also decided:

- **Scope change**: the catalog's single pilot scope `seller-stock-adjustment` is replaced by `gateway` (the release unit that actually contains every unit above). A scope that names one flow while the catalog lists platform-foundation units would be presumptive.
- **No `CODEOWNERS`**: `TEAM_TOPOLOGIES_PRD.md:37` — the repository has no organizational data; teams are placeholders (T1–T5). Generating a CODEOWNERS file would fabricate authorities. Trigger: real org data.
- **Contracts**: only edges the validator can derive from the import graph are added. An `auth.v1` contract edge is added **iff** the validator accepts it from the existing imports; the external product platform is not a repository unit and gets no contract row (trigger: it becomes an in-repo unit).

---

## 7. Decision 5 — naming ambiguity pass (owner-directed)

Renames are behaviour-preserving and each is justified by a collision or an anti-pattern; anything not listed here was left alone on purpose.

| Current | New | Why |
|---|---|---|
| `render/templutil.go` | `render/html.go` | `util` is a non-name (Go style); the file holds markup-assembly helpers (`renderTempl`, `rawHTML`, `join`, `isNilComponent`) |
| `Composer.CachedFragment` | `Composer.CachedIsland` | The codebase now uses “island” for the unit; “fragment” stays reserved for HTMX response fragments. The method returns an island’s bytes |
| `renderClient` | `renderClientPlaceholder` | Says what it renders (the placeholder), not just the mode |
| `Component.FetchURL` (doc) vs new `Load` | unchanged names, documented contrast | `FetchURL` is fetched by the browser; `Load` runs server-side. The doc comment states the pairing so “fetch” alone is never ambiguous |
| `internal/features/home/ui/handlers/products/{list,detail}` | unchanged | Same package name across read-side trees, disambiguated at import sites by aliases (`homelist`, `homedetail`); renaming would churn imports with no reader benefit at today's size. Trigger: a third `detail` package |
| `handlers/products/*/views/pages` etc. | unchanged | Names are load-bearing in generated-code paths (`templ generate` output location); renaming them is a non-trivial churn with no ambiguity payoff |

---

## 8. Ratification record (2026-10-17)

The following were put to the owner in review; the answers below are the decision, recorded verbatim in substance.

| Question | Answer |
|---|---|
| Measurement surface | **Complete, including web-vitals** — vendor the attribution build, beacon to `POST /rum`, island attribution via `data-island`, privacy/abuse posture documented (§5) |
| Ownership catalog derivation | **Include with derivation notes** — T5 for `ops` and `contentcms` with the “derived” basis recorded and cited (§6) |

---

## 9. Consequences

**Positive**

- The slowest island delays only itself; head, chrome and fast islands paint early. Cache-hit islands are flushed from bytes — cheap by construction.
- One failing island degrades to its fallback instead of the page. Failures are counted, timed and surfaced, never silent.
- Per-fragment latency is on the wire (`Server-Timing`) for the server, in `/metrics` for aggregates, and in web-vitals for the human-perceived side (LCP/INP attributed to islands).
- The ownership map covers the gateway; review ownership questions stop being answered by memory.

**Negative / accepted limits**

- Sequential, document-order flushing: a slow island delays later islands, not earlier ones (no out-of-order patching; §3.2, trigger §10).
- Post-first-flush errors truncate the stream; the fallback contract is the mitigation, not the guarantee.
- HTML responses now carry an extra `display: contents` wrapper per server island; it is a real DOM node (small, layout-inert).
- web-vitals adds one vendored script (~a few KB gz) to every composed page load.

---

## 10. Out of scope, with triggers to revisit

| Item | Trigger |
|---|---|
| Out-of-order streaming (`hx-swap-oob` stream, React-style suspending) | A page whose dominant slow island is *below* the fold of user attention, measured |
| Default timeout for **buffered** islands (today: none) | One page-level latency incident whose tail is a hung buffered island |
| Default `Fallback` for server islands (skeleton) | A second page adopting streaming without declaring fallbacks |
| RUM sampling and retention | Beacon volume distorting gateway metrics in `/metrics` |
| Fragment registry (still deferred, `DECISION_UI_OWNERSHIP.md:63`) | Third-party/island marketplace requirement |
| `CODEOWNERS` | Real org data lands in `TEAM_TOPOLOGIES_PRD.md` |
| `auth.v1` contract edge (and future edges) | Validator derivation passes / a second consuming unit appears |
| Third-party APM / RUM SaaS | Explicit owner decision; today vendor-and-aggregate is the posture |

---

## 11. References

- templ — `FlushComponent`, `runtime.Buffer`, `runtime.GetBuffer/ReleaseBuffer`, `Handler(WithStreaming)`: <https://pkg.go.dev/github.com/a-h/templ@v0.3.1020> (`flush.go`, `runtime/buffer.go`, `runtime/bufferpool.go`, `runtime/handler.go`); in-repo evidence `shell/layouts/base_templ.go:36-44,76`.
- Zalando Tailor — fragment attributes (`timeout` default 3000 ms, `fallback-src`, `async`, `primary`), fault tolerance and performance budgets: <https://github.com/zalando/tailor>.
- W3C Edge Side Includes — `<esi:include alt=… onerror="continue">`: <https://www.w3.org/TR/esi-lang/>.
- Podium — podlet manifest `content()`/`fallback()`, layout fetch model: <https://podium-lib.io>.
- Next.js — streaming guide, status/headers locked after first chunk: <https://nextjs.org/docs/app/guides/streaming>.
- htmx — response handling and `hx-swap-oob`: <https://htmx.org/docs/#response-headers>.
- GoogleChrome/web-vitals — attribution build (`onLCP`, `onINP`, …): <https://github.com/GoogleChrome/web-vitals>.
- W3C Server-Timing — header and trailer forms: <https://www.w3.org/TR/server-timing/>.
- CSS Display Module Level 3 — `display: contents` box generation: <https://drafts.csswg.org/css-display/#box-generation>.
- Internal: [`TEAM_TOPOLOGIES_PRD.md`](./TEAM_TOPOLOGIES_PRD.md), [`ADR_CROSS_TEAM_UI_CONTRACT.md`](./ADR_CROSS_TEAM_UI_CONTRACT.md), [`DECISION_UI_OWNERSHIP.md`](./DECISION_UI_OWNERSHIP.md), [`PERFORMANCE_BASELINE.md`](./PERFORMANCE_BASELINE.md), [`CMS_CONTENT_MODEL.md`](./CMS_CONTENT_MODEL.md), [`CIRCUIT_BREAKER_IMPLEMENTATION.md`](./CIRCUIT_BREAKER_IMPLEMENTATION.md).

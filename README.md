# Gogo

A federated modular-monolith web application built with **Go**, **Chi**, **Templ**, **HTMX**, **Tailwind CSS** and **DaisyUI**.

Gogo implements **"Module Federation for Go + HTMX"**: a **shell/gateway** discovers feature modules through their **manifests** (`GET /module-registry`) and mounts them without knowing their internals. Instead of bundling JavaScript remotes, modules federate **HTML fragments** — the entry point of every module is an HTML route that htmx swaps into the shell, and modules communicate through an **event bus**, mirroring the custom DOM events htmx already dispatches.

Each module follows hexagonal architecture — `domain` (business rules), `application` (use cases and ports), `infrastructure` (technical implementations), and `presentation` (HTTP adapter with Templ views) — and is a self-contained unit that plugs into the gateway.

For maintainers: the load-bearing dependency rules, the event contract
boundary and the architectural decision log live in
[`ARCHITECTURE.md`](ARCHITECTURE.md) and are enforced by
[`apps/web/internal/architecture`](apps/web/internal/architecture) (`go -C apps/web test ./internal/architecture`).

## Repository structure

This is one Git monorepo organized by deployable application platform: the
server-rendered web app is in `apps/web`, with mobile and desktop boundaries in
`apps/mobile` and `apps/desktop`. Platform services live in `services/<name>`
and protobuf contracts in `contracts`. Mobile and desktop currently have
architecture notes, not runnable projects; their stacks are chosen with their
first end-to-end feature. The root `go.work` supports local Go development; it
does not combine modules into one deployable application. Each service keeps
its own `go.mod` and can be built on its own. See
[`docs/MONOREPO_STRUCTURE.md`](docs/MONOREPO_STRUCTURE.md) for the directory map
and the rule for deciding when a service needs a separate repository.

From the repository root, use `make build`, `make check`, and `make up` to work
across the gateway and services. For a service-specific command, change into
that service directory first.

## Features

- 🧩 **Federated modules**: every feature module declares a manifest (name, version, entry, routes) and registers its own routes; the gateway exposes the registry at `/module-registry` — see [Module federation](#module-federation-gateway--registry)
- 🚦 **Module lifecycle + health aggregation**: modules may implement an optional `Start`/`Stop` lifecycle and volunteer health reports; the gateway starts them at boot and drains them in reverse order on shutdown, and the ops module's readiness probe aggregates *every* module's health (`ReadyExcluding` keeps the probe from recursing into itself)
- 📡 **Module event bus**: mutations publish domain events; subscribers (e.g. island-cache invalidation, the billing activity feed) react through **contract-keyed subscriptions** (`SubscribeTypes`), each of which returns a cancellable `Subscription` — the server-side counterpart to htmx custom events
- 🛒 **Product data lives in the platform, not here**: gogo owns no product rows. Writes go to `product-service` (`product.v1`), browse reads to `product-listing-service` (`productlisting.v1`), search to `product-search-service` (`productsearch.v1`), the category hierarchy to `category-service` (`category.v1`) — four required gRPC addresses, no in-process fallback. See [ARCHITECTURE.md D-018](ARCHITECTURE.md#d-018)
- ✅ **Home storefront** as a page at `/`: a read-only product grid that **reads through the platform's contracts** (browse for the grid, search when `?q=` is 3+ characters) and renders it with its own views. A browse outage returns **503**; a search outage returns a working storefront with search off — the breakers are per-upstream, not global
- ✅ Storefront product detail at `/catalog/{id}`: server-rendered product description, brand, price and stock from the listing projection; missing products return 404 and an unavailable catalog returns 503
- ✅ **Seller console** as a page at `/seller`: product and category management against the platform, a stock ledger with full-form mutations, **point-of-sale** (`/seller/kasir`) and **sales history with void** (`/seller/transaksi`) — it never imports module runtime code, it consumes the **product platform contracts** plus **`inventory.v1`** and **`sales.v1`**. A dedicated **inventory module** owns stock and the atomic, idempotent adjustment ledger
- 📡 **Product change feed**: `productevents` consumes the platform's Kafka topic `product.events.v1` and republishes it as `product.changed.*` on the in-process bus, so a product changed in *another* deployment still drops this one's island caches. Optional (`KAFKA_BROKERS`); without it the gateway serves on cache TTLs alone — correct, just staler
- ✅ **Islands rendering engine**: component-level rendering strategy — SSG/ISR/SSR/CSR per island, contract-driven planner, revalidation TTL cache, page composer — see below
- ✅ Embedded static assets (`go:embed`) — no runtime filesystem dependency for CSS/JS
- ✅ Structured request logging, request IDs, panic recovery, compression and per-request timeouts
- ✅ Graceful shutdown orchestrates module lifecycle (reverse-order `Stop`) in addition to HTTP draining
- ✅ Startup configuration validation (`config.Validate()`) fails fast on invalid ports, timeouts or identity
- ✅ In-memory, concurrency-safe repository + bounded LRU TTL cache behind swappable ports
- ✅ Paged product listing end to end, threaded from the platform's browse contract through `productconsumer` into the home page's read model; short queries (< 3 chars) fall through to browse rather than a pointless search round trip
- ✅ Crawlable paginated storefront (`/?page=N`) with server-rendered prev/next + numbered nav — one ISR cache entry per page, cleared together by prefix invalidation on `product.changed.*`
- ✅ Storefront search (`/?q=term`, 3+ characters) served by `product-search-service`, with the term preserved across pages. One breaker per upstream, so search failing does not take the browse grid down with it
- ✅ `cmd/modulegen` scaffolds a new contract-compliant module in seconds (`make module name=billing desc="…"`)
- ✅ Unit, package and end-to-end tests (run with `-race`)

## Routes

Modules own their routes; pages own theirs. The gateway serves the storefront,
the seller console and the account surfaces, and mounts the modules it discovers
at `/module-registry`. Product data is **not** in this process: the **home** page
reads it from the platform's browse and search read models, and the **seller**
console writes products and categories through `product.v1` / `category.v1`.
Each surface renders its own pages from its own read model.

| Method   | Path                          | Module   | Area   | Description                                        |
| -------- | ----------------------------- | -------- | ------ | -------------------------------------------------- |
| `GET`    | `/`                           | home     | public | Storefront product grid (ISR island; paginated via `/?page=N`, searchable via `/?q=term` with 3+ characters). Browse and search have **separate breakers**: a browse outage returns 503, a search outage returns a working storefront with search off |
| `GET`    | `/catalog/{id}`               | home     | public | Product detail page (SSR from `productlisting.v1`; 404 for missing products, 503 when the catalog is unavailable) |
| `GET`    | `/login`                      | auth     | public | Sign-in form (consumes `auth.v1` Login; writes the session cookie) |
| `POST`   | `/login`                      | auth     | public | Verify credentials and set the session cookie (Post/Redirect/Get) |
| `POST`   | `/logout`                     | auth     | public | Clear the session cookie and return to the signed-out view |
| `GET`    | `/profile`                    | profile  | account | Account page (signed-in employee via `auth.v1` Lookup; signed-out otherwise) |
| `GET`    | `/profile/pesanan`            | profile  | account | Orders section |
| `GET`    | `/profile/alamat`             | profile  | account | Addresses section |
| `GET`    | `/profile/pengaturan`         | profile  | account | Settings section |
| `GET`    | `/seller`                     | seller   | seller | Dashboard (produk/kategori/stok/kasir/transaksi)   |
| `GET`    | `/seller/kasir`               | seller   | seller | Point-of-sale page (product grid + payment form)          |
| `POST`   | `/seller/kasir`               | seller   | seller | Record a sale (composes `inventory.v1` AdjustStock)       |
| `GET`    | `/seller/transaksi`           | seller   | seller | Sales history page (list + void form)                     |
| `POST`   | `/seller/transaksi/{id}/void` | seller   | seller | Void a sale (composes `inventory.v1` AdjustStock)         |
| `GET`    | `/seller/produk`              | seller   | seller | Product management page (list + create form)        |
| `POST`   | `/seller/produk`              | seller   | seller | Create a product                                    |
| `POST`   | `/seller/produk/{id}`         | seller   | seller | Update a product                                    |
| `POST`   | `/seller/produk/{id}/delete`  | seller   | seller | Delete a product                                    |
| `GET`    | `/seller/kategori`            | seller   | seller | Category list (read-only; `category-service` owns the hierarchy) |
| `GET`    | `/seller/stok`                | seller   | seller | Stock ledger page (adjust form + movement history)  |
| `POST`   | `/seller/stok`                | seller   | seller | Adjust stock (atomic `inventory.v1` AdjustStock)    |
| `GET`    | `/healthz`                    | ops      | ops    | Liveness probe (deployment tooling)                |
| `GET`    | `/readyz`                     | ops      | ops    | Readiness probe (aggregates every module's health report) |
| `GET`    | `/billing`                    | billing  | billing| Landing page + live product-activity summary       |
| `GET`    | `/billing/fragment`           | billing  | billing| Activity feed fragment (consumes `product.changed.*` events) |
| `GET`    | `/module-registry`            | gateway  | ops    | Federated module manifests (JSON, or HTML for browsers) |
| `GET`    | `/metrics`                    | gateway  | ops    | Prometheus-text counters (requests by method/status/route, in-flight, durations, uptime) |
| `GET`    | `/debug/vars`                 | gateway  | ops    | Same counters as JSON via `expvar` (`gogo_http`)       |

The `/seller*` routes return HTML; every machine call travels over a gRPC wire
contract, never over HTTP — `product.v1`, `category.v1`, `inventory.v1` and
`sales.v1` to the platform and to in-process modules respectively. Product
changes arrive out of band over Kafka (`product.events.v1`) and are republished
onto the in-process bus; they do not flow over any of these routes. The seller
console renders full pages with Post/Redirect/Get mutations, so it works without
JavaScript.

Supplier management was removed along with the product CRUD module and has no
replacement in the platform — see
[ARCHITECTURE.md D-018](ARCHITECTURE.md#d-018).

## Module federation (gateway + registry)

Gogo follows the **"Go + HTMX Micro Frontends"** pattern. Each feature module is an independently deployable unit; the gateway acts as the shell:

```
                    ┌────────────────────────────────────┐
                    │        Gateway / Shell              │
                    │  Go + HTMX · Layout / Registry     │
                    │  routes: /, /seller*, /login, …     │
                    └──────────┬─────────────────────────┘
                               │  GET /module-registry
        ┌──────────┬───────────┼──────────┬──────────────┐
        ▼          ▼           ▼          ▼              ▼
   ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐  ┌────────┐
   │ auth   │ │billing │ │inventory│ │ sales  │  │ ops    │
   │ module │ │ module │ │ module  │ │ module │  │ module │
   └────────┘ └────────┘ └────────┘ └────────┘  └────────┘
                                          (every in-process module,
                                           federated by manifest)

  pages (no manifest, no lifecycle, wired by the composition root):
   home   ──▶ productlisting.v1 List / productsearch.v1 Search  (browse + search)
   seller ──▶ product.v1 CRUD · category.v1 read · inventory.v1 AdjustStock · sales.v1 Checkout

  all four product contracts are reached through ONE package:
   internal/shared/productconsumer ──▶ product/v1 · productlisting/v1 ·
                                       productsearch/v1 · category/v1
   (import rule 6 — an architecture test, not a convention)

  product changes arrive out of band, not from a page:
   Kafka product.events.v1 ──▶ productevents ──product.changed.*──▶ shared bus
                                                            ◀── home + billing
   (cache invalidation + the billing activity feed)
```

There is no product module in that diagram, and that is the point: the platform
owns products, so gogo holds no product rows and publishes no product events.
The store read by the storefront lives in the platform's Postgres; the stock
ledger lives here, in the inventory module.

The shell never reaches into a module: it knows modules through their **manifests** only.

### The module contract

Every module implements `module.Interface` (`internal/shared/module`):

```go
type Interface interface {
    Manifest() Descriptor
    RegisterRoutes(r chi.Router)
}
```

Modules stay minimal by default, but can opt into two optional contracts
for richer orchestration (`internal/shared/module`):

```go
// Lifer: lifecycle hooks — Start runs at boot (registration order), Stop
// runs on graceful shutdown (reverse order, so dependents stop first).
type Lifer interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}

// HealthReporter: a voluntary health report. Modules without one are
// always reported ready, so a minimal module never blocks readiness.
type HealthReporter interface {
    Healthy(ctx context.Context) Health // Status: ok | degraded | down
}
```

`Descriptor` is the manifest — the module's `remoteEntry.js` equivalent:

```json
{
  "name": "billing",
  "version": "1.2.0",
  "entry": "/billing",
  "description": "Subscription state and the live product-activity feed, rendered from product.changed.* events on the shared bus.",
  "routes": {
    "landing": "/billing",
    "fragment": "/billing/fragment"
  }
}
```

Not every surface is a module. The home storefront (root route) and the
seller console are top-level **pages** (`internal/pages/home`, `internal/pages/seller`) — plain HTTP surfaces with no manifest, no lifecycle and no entry in the `/module-registry`. They consume the product platform the way any external service would: over its gRPC contracts, through the shared `productconsumer` kit, with their own client adapters in `infrastructure/` mapping the kit's neutral `Core` into page-local read models. They observe `product.changed.*` on the shared bus only to keep their island caches coherent. Pages are wired by the composition root and handed straight to the gateway:

```go
// internal/bootstrap/di.go — the composition root declares the whole graph
fdi.ProvideType[homeports.Listing](b, func(c *fdi.Container) (homeports.Listing, error) {
    return fdi.ResolveAs[*homelistingclient.Client](c, serviceListingClient)
})
fdi.ProvideType[sellerports.ProductCatalog](b, func(c *fdi.Container) (sellerports.ProductCatalog, error) {
    return fdi.ResolveAs[*sellercatalogclient.Client](c, serviceSellerCatalogClient)
})
fdi.ProvideType[sellerports.Inventory](b, func(c *fdi.Container) (sellerports.Inventory, error) {
    return fdi.ResolveAs[*sellerinventoryclient.Client](c, serviceSellerInventoryClient)
})
fdi.ProvideType[sellerports.Sales](b, func(c *fdi.Container) (sellerports.Sales, error) {
    return fdi.ResolveAs[*sellersalesclient.Client](c, serviceSellerSalesClient)
})

// Each page owns its own graph, keyed by type, and receives exactly the
// capability it declares — so no caller has to name a page or its port.
home.Register(b)
seller.Register(b)
```

Because pages carry no manifest, their nav items don't live in the
registry — the gateway compiles the shell menu from **page nav first, then
module nav** (`Registry.Nav()`), so a page's entry points always lead the top
bar ahead of the module-federated items.

The gateway (`internal/shared/httpserver`) collects the modules into a `module.Registry`, mounts each module's routes and exposes the registry at `/module-registry` (JSON by default, an HTML page for browsers). Registering a new module is one line in the composition root — its **navigation appears in the shell top bar automatically**, because the gateway compiles the nav from the manifests and injects it into every request:

```go
// apps/web/cmd/server/main.go
// BuildGateway composes the whole gateway — the four platform connections,
// registry, pages, feature modules — and returns one shutdown hook, so the
// product connections and the pages that use them cannot drift.
app, closeAll, err := bootstrap.BuildGateway(ctx, cfg, logger)
defer closeAll()
```

That one line is a `Register` call, because each page and feature module
declares its own graph rather than being hand-wired
(`internal/bootstrap/di.go`):

```go
home.Register(b)
seller.Register(b)
billing.Register(b) // subscribes to product.changed.* on the shared bus
ops.Register(b)     // /healthz + /readyz, aggregating every other module
```

`ops.Register` is the interesting one. Readiness needs the state of every
upstream the gateway renders from, but the clients that own those circuit
breakers belong to pages — so rather than reach for them, `ops` asks for the
**group of contributed breakers**. Adding an upstream is then one contribution
in the composition root, and neither `ops` nor its readiness message needs to
change.

The gateway (`NewWithRegistry`) mounts the registry's routes and the pages,
starts every `Lifer` in registration order before listening, drains them in
reverse order during graceful shutdown, and serves the aggregated registry
at `/module-registry`. `reg.Ready()` errs while any module reports `down`,
so `/readyz` stays 503 under partial failure while `/healthz` keeps
reporting the process is alive.

The composition root owns one `events.Bus` and hands it to whichever
surface needs it. gogo publishes no product events — the platform owns the
product aggregate — so `productevents` *produces* onto this bus from the
platform's Kafka feed, and billing and the home page *subscribe* as observers,
to keep the activity feed and island caches coherent. The gateway never knows
(or cares) which surfaces talk to each other. Surfaces also talk over gRPC:
the platform exposes four versioned contracts as its remote interface, reached
only through `productconsumer`, and the home page and seller console map the
kit's `Core` into their own read models with their own client adapters — no
surface imports another, and no surface imports a generated contract.

### Where the shell ends and modules begin

| Module Federation   | Go + HTMX (this repo)            |
| ------------------- | -------------------------------- |
| Remote JS bundle    | Remote HTML fragment             |
| `remoteEntry.js`    | Module manifest (`module.Descriptor`) |
| Webpack runtime     | Gateway + `/module-registry`     |
| Component federation| HTML/component federation        |
| Shared npm deps     | Shared shell (`shell.Page`)      |
| JS state            | Server/session state             |
| Dynamic `import()`  | `hx-get` fragment swaps          |

The golden rule is unchanged: *"If code is still useful when the module that uses it is deleted, it does not belong in that module."* `shared/module`, `shared/events` and `shared/web/shell` are federation substrate because every module needs them.

### The events contract

Modules publish domain events through `shared/events` (a synchronous in-process bus) and subscribe to each other's events **without importing each other** — the server-side mirror of htmx custom events. Subscriptions are **contract-keyed**: a handler declares which event contracts it cares about (`SubscribeTypes`), each call returns a cancellable `Subscription`, and listeners can also be handed `synchronous` vs `async` delivery:

```go
// A module subscribes to the types it understands, with the payload
// already narrowed to the contract. The returned subscription cancels
// delivery (idempotently) when the module no longer needs the feed.
added := bus.SubscribeTypes(
    func(ctx context.Context, e events.Event) {
        payload := e.Payload.(productevents.Payload)
        composer.Invalidate(islandkeys.ProductGrid)
    },
    productevents.UpdatedType,
)
defer added.Cancel()
```

The handler only ever receives events that match the declared contracts, so
type assertions inside a handler are part of its contract check rather than
a blind cast. `bus.PublishAsync` schedules delivery off the request path
where subscribers don't need ordering guarantees.

Publication happens in the **application layer** (`application.Service`), not in a presentation adapter: every accepted mutation emits exactly one event through the `ports.EventPublisher` port, whichever surface triggered it. A new transport (a queue consumer, a seller write endpoint) therefore inherits the event contract for free instead of silently dropping it. `application/service_test.go` pins the "one event per accepted mutation, none on failure" invariant.

Product changes are the one case where nothing in this process is the author,
so there is no application layer to publish from. `internal/shared/productevents`
is the producer instead: it consumes Kafka `product.events.v1`, `Decode`s each
record into gogo's own vocabulary and republishes it on the shared bus. Two
consequences worth knowing:

- A product changed **in another deployment** — or by another client of
  product-service — still drops this one's island caches. That is the whole
  point of consuming the feed rather than publishing locally.
- `KAFKA_BROKERS` is **optional**. Without it there is no consumer and no
  republication, and caches expire on their TTLs (30s for the storefront grid)
  instead of immediately. That degradation is correct, so it is allowed to be
  configured away; the four gRPC addresses are not, because those degrade into
  a silently empty storefront.

The `billing` module is the working subscriber example: it subscribes to
`product.changed.*` on the shared bus and renders them as a live
HTMX fragment at `/billing` — change a product in `/seller`, open `/billing`,
and the activity feed shows it without billing importing any product-platform
code (its test pins this behaviour).

The `home` page and the `seller` console are the **remote-consumer**
examples: neither imports module runtime code — and neither imports a
generated contract, which import rule 6 enforces. Home declares
`application/ports.Listing` (a read-only port over the platform's browse and
search contracts) and implements it over `shared/productconsumer`; the seller
console declares `application/ports.ProductCatalog`, `ports.Inventory` and
`ports.Sales` and implements the first over the same kit and the other two over
its own inventory/sales clients. Each page's `infrastructure/*client` is only
the final hop — mapping the kit's neutral `Core` into the page's own read model.
Home additionally subscribes to `product.changed.*` on the shared bus only to
invalidate its own grid island cache, never to publish.

### Scaffolding a new module

```sh
make module name=billing desc="Subscription billing, invoices and payments"
# or: make module name=billing desc="Subscription billing, invoices and payments"
```

This creates `internal/modules/billing/` with a manifest, a shell-rendered landing page, an HTMX fragment endpoint and a passing smoke test — no templ CLI required. Then register it with the gateway (one line in `cmd/server/main.go`); its nav item federates into the shell top bar and its manifest appears in `/module-registry` the moment the gateway boots.

### The product platform is a separate deployment

The product domain is not a module here, so the "split a module into its own
process" story now lives in the platform repository. What this repo owes that
story is a **contract and an address**:

```sh
# The four addresses are required configuration. There is no in-process
# fallback: the gateway refuses to boot rather than serve a storefront that
# can never have products in it.
PRODUCT_ADDRESS=dns:///product-app:9092
PRODUCT_LISTING_ADDRESS=dns:///product-listing-app:9093
PRODUCT_SEARCH_ADDRESS=dns:///product-search-app:9094
CATEGORY_ADDRESS=dns:///category-app:9090
PRODUCT_PLATFORM_SERVICE_TOKEN=change-this-to-a-shared-service-token
KAFKA_BROKERS=kafka:19092   # optional — see below
```

Three things make that boundary work, and each is a seam rather than a
convention:

1. **Contracts, not imports.** The four `.proto` files are vendored into
   `contracts/` with `go_package` rewritten to gogo's module path, so the wire
   types are the seam. Import rule 6 restricts them to `productconsumer`, so a
   page cannot grow a private gRPC client that bypasses the mapping.
2. **A neutral `Core`.** The kit maps every wire shape into one `Core` —
   `int64` IDs to decimal strings, money kept in minor units — so pages keep
   their own read models and gogo's own ID representation. See
   [D-018](ARCHITECTURE.md#d-018) for why each conversion is lossy in one
   direction only, and why a caller must not read an absent field as a real
   zero.
3. **Per-upstream breakers.** Four upstreams, four breakers, four `/metrics`
   series. Browse failing is a 503; search failing is a storefront with search
   off. One shared breaker would have made every page 503 for any single
   outage.

**Testing it without the platform.** `internal/bootstrap/fakestack_test.go` and
`productplatform_integration_test.go` run the real gRPC servers over a
bufconn, so the gateway's platform wiring — tenant stamping, breaker
classification, mapping, cache invalidation across the event feed — is covered
without a network.

**Local run.** `docker compose up` from the repository root brings up the
platform; `docker compose up -d --build` from `gogo/` brings up Postgres and the
gateway against it. The two stacks meet on the shared `ecommerce_default`
network.

A full cross-deployment demo: with the platform running, create a product at
`http://localhost:8080/seller` and watch it appear in the `/billing` activity
feed — the event travels Kafka → `productevents` → the shared bus, not an
in-process publish. Kill `product-listing-service` and refresh `/`: browse
returns **503** rather than an empty grid, while `/metrics` shows
`gogo_product_listing_breaker_state 1`. Kill `product-search-service` instead
and `/` still serves, with search off.

The platform layer is **extractable to its own repo**: the contracts are a
separate Go module, `internal/shared` imports no app code (enforced in
`internal/architecture`), and `productconsumer` imports no page or module
package — so moving the client kit out is a copy, not a rewrite.

## Islands architecture (component-level rendering)

A page is never rendered by a single strategy. Following the islands
architecture (static HTML "sea" with islands of dynamic content), every
component of a page gets its own rendering decision — planners and page
composers instead of one giant render switch.

```
Page Schema (islands + contracts)
        │
        ▼
Rendering Planner  ──►  Rendering Decision (SSG | ISR | SSR | CSR)
        │                      │
        ▼                      ▼
  Page Composer ◄── cached HTML (TTL cache) / SSR render / CSR placeholder
        │
        ▼
   composed HTML document
```

- **Rendering Contract** (`RenderingContract`) — what the data *needs*:
  `CacheTTL`, `Public`, `Personalized`, `RequiresSEO`, `Freshness`
  (or an explicit `Mode` pin).
- **Rendering Context** (`RenderingContext`) — per-request facts the planner
  may consult (`Path`, `IsAuthenticated`, `IsHTMX`).
- **Render Decision** (`RenderDecision`) — the planner's output: mode + TTL.
- **Rendering Planner** (`Planner`, default `ContractPlanner`) — a policy
  table driven by contract characteristics, never by component names, so new
  islands are covered without touching the policy:

  | Contract                                | Mode    | Semantics          |
  | --------------------------------------- | ------- | ------------------ |
  | realtime + personalized                 | Client  | CSR: lazy placeholder fetched by the browser (`hx-trigger="load"`) |
  | realtime                                | Dynamic | SSR: fresh on every request                |
  | `CacheTTL` set                          | Revalidate | ISR: cached until TTL expiry or invalidate |
  | otherwise                               | Static  | SSG: render once, cached until invalidated |

- **Page Composer** (`Composer`) — renders each island with its decision,
  composes the region and wraps it in the page shell; serves and invalidates
  revalidated HTML. `RenderComponent` serves single-island fragments; mutations
  invalidate affected islands via the module event bus.

- **Cached HTMX fragment fast path** — a page's list handler (the shared
  `pagekit` `PagedListPage`/`ListPage`) answers an HTMX fragment request
  straight from the island cache when it is warm, with **no data fetch on the
  backing service**: the fragment needs nothing but the cached HTML. Full-page
  requests still fetch (the shell renders the count and pagination from live
  data). The peek applies the same `Public && !Personalized` gate as every
  cache read, so a cached island is never served to a viewer it was not
  writable for; a miss falls through to the normal coalesced render.

> Only `Public && !Personalized` islands are cached, so cached HTML can never
> leak across viewers. The same predicate gates both the cache read and the
> cache write, so an island is never read from a cache it would not have been
> written to. An explicit `contract.Mode` always wins; handlers that need a
> different policy inject their own planner via `WithPlanner`.

The mechanics live in `internal/shared/web/render/component.go` and
`cache.go`. The `TTLCache` is bounded and uses **least-recently-used**
eviction (dropping an expired entry first), so a hot island is never evicted
in favour of a cold one that merely arrived later. Each surface declares its
own island keys — the home page in its own `presentation/http/islandkeys`
(`home.product-grid`), the seller console in its own. Handlers
build their islands, the composer decides, and cache invalidation listens
for product events. Mapping to what this repo still serves:

| Island                          | Page          | Strategy | Invalidation                                  |
| ------------------------------- | ------------- | -------- | --------------------------------------------- |
| `home.product-grid`             | `/` (`/?page=N`) | ISR 30s | every product change — **the home page owns this island**; it is paginated (one cache entry per page) and invalidated by prefix via its own `product.changed.*` subscription |
| `billing.activity`              | `/billing`    | SSR     | — (always fresh; fed by the same `product.changed.*` events) |

The product-detail islands (`detail.{id}.product-info`, `.price`, `.stock`)
went with the catalog's product pages.

References: [Astro — Islands Architecture](https://docs.astro.build/en/concepts/islands),
[htmx — Lazy Load](https://htmx.org/docs/#lazy-loading) (`hx-trigger="load"`),
SSG/ISR/SSR/CSR trade-offs ([web.dev — Rendering on the web](https://web.dev/articles/rendering-on-the-web),
[Robust Client-Side JavaScript — rendering patterns](https://developer.chrome.com/blog/rendering-on-the-web)).

## Response intent (use case–driven rendering)

Rendering *how* a use case answers the request is a decision owned by the
renderer, never duplicated per handler. Two concepts work together:

- **Rendering strategy** — the policy that classifies a use case by its
  hypermedia requirement (`internal/shared/web/render/strategy.go`):
  `KindNavigation` returns a complete document for plain requests and only
  the swapped region for HTMX requests; `KindMutation` returns a fragment
  for HTMX requests and falls back to Post/Redirect/Get for plain requests.
- **Response intent** — what the handler declares next to the use case:
  the page islands + shell for navigation (`Composer.RenderNavigation`), or
  the fragment + PRG redirect for mutations (`render.Respond`). The renderer
  then reads the request (`ContextFromRequest`) and picks the
  representation, so handlers never branch on `HX-Request` for their happy
  path — HTMX is a property of rendering, not handler code.

| Use case       | Intent          | Plain request (no JS)    | HTMX request (`HX-Request: true`)        |
| -------------- | --------------- | ------------------------ | ---------------------------------------- |
| `ListProducts` (home) | Navigation | Full storefront (ISR grid island) | grid fragment → `#product-list` |

The `ListProducts` row belongs to the **home page**: its handler reads
products through its remote listing client (`ports.Listing` over the platform's
browse/search contracts via `productconsumer`), maps the response into its own
read model (`results.Product`), and renders its own Templ partials. The seller
console takes the simpler route: its reads render full pages and its mutations
Post/Redirect/Get, so it never branches on `HX-Request` at all — the intent
table above is a convention every surface follows.

Validation errors are a per-case intent, so they bypass the strategy: HTMX
gets a 200 response with an **out-of-band alert**, because htmx never
swaps 4xx/5xx responses (`htmx.org/quirks`). Fragments are the **same Templ
partials** the GET endpoints render, so swapped markup always reflects
server truth.

References: [htmx — Requests & Responses](https://htmx.org/docs/#requests--responses),
[htmx — Template Fragments](https://htmx.org/essays/template-fragments),
[htmx — Quirks](https://htmx.org/quirks),
[templ guide — htmx](https://templ.guide/server-side-rendering/htmx),
[The Case for HTMX](https://medium.com/@paulhoke/the-case-for-htmx-rethinking-modern-web-architecture-1e6772cf0c74).

## Project layout

The repository layout is shown above. This tree is the gateway module's layout;
its paths are relative to `apps/web/`:

```
cmd/
├── server/                # composition root: config, logger, gateway lifecycle (pages + modules)
└── modulegen/             # scaffolds a new contract-compliant feature module
    └── templates/         # module.go / handler.go / module_test.go templates

internal/
├── bootstrap/             # gateway-only startup: SignalContext, the four product-platform
│                          # connections, BuildGateway (modules + pages + registry)
├── modules/               # feature modules — manifest + routes, join the /module-registry
│   ├── inventory/         # owns stock: the inventory_stock table + the movement ledger,
│   │   ├── compose/       # Build (Postgres ↔ in-memory)
│   │   ├── domain/        # pure business rules — knows nothing about HTTP/templ/HTMX
│   │   ├── application/   # use cases + ports; returns result DTOs (results/), never HTML
│   │   │   ├── events/    # inventory domain events
│   │   │   ├── ports/     # single-method ports the infrastructure implements
│   │   │   ├── results/   # the read model handed to views
│   │   │   └── use-cases/ # one package per use case
│   │   ├── infrastructure/# technical implementations
│   │   │   └── postgres/  # inventory_stock + inventory_movements; its own migration filenames
│   │   └── presentation/grpcservice/ # inventory.v1 gRPC service (AdjustStock: atomic, idempotent)
│   ├── sales/             # point-of-sale: composes inventory.v1 AdjustStock per checkout line
│   ├── auth/              # identity: auth.v1, the session cookie, tenant resolution
│   ├── billing/           # billing module: subscribes to product.changed.* events
│   │   ├── activity/      # in-memory read model kept coherent by contract-keyed On* handlers
│   │   ├── handlers/      # landing page + activity-feed fragment (hx-get)
│   │   ├── views/         # Templ components for the landing page and feed
│   │   └── module.go      # manifest, routes, contract-keyed bus subscriptions
│   └── ops/               # operational module: /healthz + /readyz (no views)
├── pages/                 # top-level pages — NO manifest, NO lifecycle, wired by the root
│   ├── home/              # storefront: a REMOTE consumer of the platform (browse + search)
│   │   ├── application/   # ports.Listing (home's own read contract) + results.Product read model
│   │   ├── handlers/products/list/  # listing handler — mirrors seller's per-handler layout
│   │   ├── infrastructure/listingclient/  # adapter over productconsumer: Core → home's read model
│   │   ├── views/         # presentation model: results → Templ components
│   │   │   ├── pages/        # full document (home)
│   │   │   └── partials/     # product-list, product-card (the grid the root page composes)
│   │   ├── islandkeys/    # home.product-grid — owned here
│   │   ├── page.go        # page wiring: composer, cache coherency subscription, nav
│   │   └── page_test.go
│   ├── seller/            # seller console: a REMOTE consumer of the platform (products,
│   │                      # read-only categories), inventory (stock) and sales (POS)
│   │   ├── application/   # ports.ProductCatalog + ports.Inventory + ports.Sales + seller's own read models
│   │   ├── handlers/      # one package per surface, split by file: handler (render/fail), catalog, stock, pos, form
│   │   ├── httperr/       # peer error → HTTP status + cashier-facing message; outages collapse to one 503
│   │   ├── infrastructure/catalogclient/  # adapter over productconsumer: Core + status → port errors
│   │   ├── infrastructure/inventoryclient/ # gRPC adapter over inventoryv1
│   │   ├── infrastructure/salesclient/    # gRPC adapter over salesv1
│   │   ├── views/         # presentation model: results → Templ components
│   │   │   ├── pages/        # full documents (seller)
│   │   │   └── partials/     # dashboard, product-table, category-list, stock-ledger, kasir-form, transactions
│   │   ├── islandkeys/    # seller.product-table, seller.kasir, seller.transactions — owned here
│   │   ├── page.go        # page wiring: composer, cache coherency subscription, nav
│   │   └── page_test.go
│   ├── auth/              # sign-in page: a REMOTE consumer of auth (login.v1)
│   │   ├── application/   # ports + results the page owns; no authv1 wire types
│   │   ├── handlers/      # login + logout; writes the cookie via shared/web/session
│   │   ├── infrastructure/authclient/  # thin adapter over the shared authconsumer client
│   │   ├── views/         # pages/ (login)
│   │   ├── page.go        # page wiring
│   │   └── page_test.go
│   ├── profile/           # account page: a REMOTE consumer of auth (login.v1)
│   │   ├── application/   # ports + results.Account read model
│   │   ├── handlers/      # account section; reads the cookie via shared/web/session
│   │   ├── infrastructure/authclient/  # thin adapter over the shared authconsumer client
│   │   ├── views/         # pages/ (profile)
│   │   ├── page.go        # page wiring
│   │   └── page_test.go
│   └── README.md          # pages vs modules, placement rules
└── shared/                # generic, reusable infrastructure (the federation substrate)
    ├── authconsumer/      # shared remote-client kit for identity: wraps auth.v1, one error vocabulary
    │                      # shared by the auth and profile pages (one client, one breaker)
    ├── breaker/           # circuit breaker shared by the consumer kits (one per upstream)
    ├── config/            # environment-driven configuration (validated at startup);
    │                      # the four product-platform addresses are REQUIRED
    ├── events/            # in-process module event bus (publish / contract-keyed subscribe)
    ├── foundation/        # the gateway kernel: context, DI container, logger, module contract
    ├── grpcconsumer/      # the shared gRPC call path: breaker + tenant + status→error mapping,
    │                      # so no consumer hand-rolls a call block or loses its outage sentinel
    ├── grpcx/             # shared gRPC transport: Dial (network) + InProcess (bufconn);
    │                      # consumers keep their own ports/error mapping
    ├── httpserver/        # gateway: router, middleware, module registry mount, lifecycle orchestration
    ├── idgen/             # ID generation (UUID) behind a port
    ├── kernel/            # gateway kernel primitives
    ├── logging/           # structured logger factory (service/version/env on every record)
    ├── productconsumer/   # the ONLY package that may import a product-platform contract;
    │                      # maps all four wire shapes → one neutral Core + status→error,
    │                      # and reports per-upstream breaker state
    ├── productevents/     # consumes Kafka product.events.v1 → republishes as
    │                      # product.changed.* on the in-process bus (cache invalidation + billing)
    ├── module/            # the module contract: Descriptor, Interface, Lifer,
    │                      # HealthReporter, Registry (snapshot-based lifecycle/health aggregation)
    ├── money/             # Rupiah formatting
    ├── netutil/           # trusted-proxy parsing shared by config.Validate and the RealIP middleware
    ├── pgxkit/            # shared pgx pool construction + ping shared by the Postgres adapters
    ├── tenant/            # the tenant boundary: tenant ID + context carrier, host→tenant registry,
    │                      # and the gateway middleware that resolves one per request
    ├── version/           # link-time injected build version (via -ldflags)
    └── web/
        ├── assets/        # embedded CSS/JS
        ├── pagekit/       # page composition helpers (composer, event→invalidate, generic ListPage,
        │                  # ClassifyError/WithErrorStatus so an outage is 503, not 500)
        ├── render/        # rendering mechanics only: strategy.go, render.go, htmx.go,
        │                  # component.go + cache.go — knows nothing about products
        ├── session/       # session cookie policy (name, attributes, read, clear); Secure is
        │                  # derived from the deployment, so handlers stop repeating it
        └── shell/         # only genuinely shared layouts (base shell + shell.Page)
                           # and the templ-rendered /module-registry page
```

Wiring happens in each module's `module.go` (the module composition root):
infrastructure → application ports → use cases → HTTP handlers. A thin
`application.Service` facade wires the use cases, owns the event
contract, and exposes only narrow reader/writer ports to presentation — each use
case receives exactly the single-method repository port it needs. Modules are
registered with the gateway by their manifests only.

### Boundary rules

Dependency direction (the only way the layers may know each other):

```
presentation/http ──► application ──► domain
                          ▲
infrastructure ──────────┘   (implements application ports)
```

- **`domain`** is pure business rules: it may import nothing internal —
  no HTTP, templ, HTMX, infrastructure, or application.
- **`application`** executes use cases through ports and returns result
  DTOs (`results.ProductResult`). It may know `domain`, ports and results —
  never HTTP, HTMX, templ, or SQL/database implementations. The application
  `Service` facade owns the **event contract**: it publishes one
  `events.Event` per accepted mutation through the
  `ports.EventPublisher` port, so event emission is transport-neutral.
- **`infrastructure`** implements the application ports and nothing else.
- **`presentation/http`** translates HTTP ↔ use cases and owns the HTMX
  decision: `handlers/` parses input, runs the use case and declares the
  response intent; `views/` (the presentation model) renders result DTOs
  into Templ + htmx and never runs use cases; `httperr/` maps domain errors
  to HTTP status/messages; `islandkeys/` names this module's islands.
- **`shared/module` + `shared/events`** are federation substrate: modules
  declare manifests and publish/subscribe events without importing each
  other. **`shared/web/render`** owns the rendering mechanics — HTMX
  detection, full page vs fragment, redirect, cache, island strategy — and
  knows nothing about products: no `Product`, no use cases, no repository.
- **Modules never import each other** (enforced). Cross-module needs go
  through exactly two channels, both owned at the boundaries: (1) the
  **shared event bus** — observe `product.changed.*` without importing the
  producer (billing renders the feed from it, the home page invalidates its
  island cache on it); (2) a **remote gRPC contract** — the contract lives in
  the `contracts/` Go module, the client's transport mechanics live once in
  `shared/grpcx` + `shared/grpcconsumer` + `shared/productconsumer`, and each
  consumer owns its *client adapter* and its own read model.
- **The product platform is reached through exactly one package.**
  `shared/productconsumer` is the sole importer of `product/v1`,
  `productlisting/v1`, `productsearch/v1` and `category/v1` (`internal/bootstrap`
  is the one exception: it dials the four connections). It maps every wire
  shape into one neutral `Core` and owns the error vocabulary (not-found /
  invalid / conflict / unavailable); each page aliases that into its own
  `ports.Err*`. So a page cannot grow a private gRPC client that re-implements
  the tenant stamp, the breaker, or the ID conversion — and the boundary is an
  architecture test, not a review convention.
- **Remote consumption is swappable by construction.** The home page only
  knows `ports.Listing` (a read-only List/Search port); the seller console only
  knows `ports.ProductCatalog`, `ports.Inventory` and `ports.Sales`. Tests
  reach the platform over a bufconn channel (`grpcx.InProcess`); production
  points the same client at a network address (`grpcx.Dial(ProductAddress)`) —
  a configuration swap, not a code change.
- Use case says what happens; handler decides HTTP; view decides HTML;
  render decides how the HTML is delivered (full page vs HTMX fragment);
  events keep cross-cutting effects (cache invalidation) decoupled.
- **One error table per module.** `domain.Classify(err)` maps an error to a
  transport-neutral `domain.Kind` (NotFound/Invalid/Conflict/Internal), and
  `domain.Message(err)` returns the stable client message. Each adapter is a
  thin projection of that table — `grpcservice.toStatus` picks a gRPC code,
  the module's `httperr.Map` picks an HTTP status — so the gRPC and HTML
  surfaces can never disagree on what an error means or how to phrase it.
  Internal errors are logged before the opaque status is returned.

Placement rule of thumb:

> If code is still useful when the module that uses it is deleted, it does not
> belong in that module. So each module keeps its own `httperr` and `islandkeys`
> at its own `presentation/http/` — and the home page and seller console have
> their own, consumer-side copies (home maps the platform's `Core` into its own
> product read model and names its own grid island; the seller console maps its
> peers' errors into a cashier-facing error table). `shared` is
> only for what has proven reusable across modules, not for what two modules
> happen to both use today — `productconsumer` earns its place by being the
> *only* legal path to four contracts, not by being the most convenient one.

## Quick start

Requirements: **Go 1.27+**, **Bun**, and the [templ CLI](https://templ.guide/quick-start/installation).

```sh
# 1. Install dependencies
bun install

# 2. Build CSS and run the dev watchers + server
make dev
```

Then open <http://localhost:8080> (storefront), <http://localhost:8080/seller> (management),
<http://localhost:8080/healthz> (ops) and <http://localhost:8080/module-registry> (module manifests).

### Make targets

| Target         | Description                                        |
| -------------- | -------------------------------------------------- |
| `make dev`     | Watch templ & CSS changes, run the server          |
| `make build`   | Generate templ + CSS, build `bin/gogo`             |
| `make run`     | Build and run the binary                           |
| `make test`    | Run the gateway tests with the race detector        |
| `make check`   | The whole gate: gofmt, templ + contracts up-to-date, vet and tests across **all six modules** |
| `make services-vet`  | `go vet` for the four platform services, on their own module graphs |
| `make services-test` | Race-enabled tests for the four platform services, including their architecture tests |
| `make templ`   | Regenerate `*_templ.go` from `.templ` sources      |
| `make css`     | Compile the Tailwind stylesheet                    |
| `make module`  | Scaffold a module: `make module name=billing desc="…"` |
| `make tidy`    | `go mod tidy`                                      |
| `make docker-build` | Build the production image (multi-stage, non-root) |
| `make docker-push`  | Build and push `$(IMAGE):$(IMAGE_TAG)`         |
| `make k8s-build`    | Render the Kustomize manifest set              |
| `make k8s-apply`    | Apply the whole stack (requires the `gogo-secrets` secret) |
| `make k8s-secrets CHANGEME=…` | Create the runtime secret from your DB password |
| `make k8s-delete`   | Tear down the stack                           |

## Configuration

Configuration is read from environment variables; a `.env` file is loaded if present (see `.env.example`). Values are parsed strictly — a malformed duration or an unknown log level fails **startup**, never quietly falls back, so a typo cannot ship to production. Every variable maps 1:1 onto a Kubernetes ConfigMap/Secret entry.

| Variable          | Default | Description                          |
| ----------------- | ------- | ------------------------------------ |
| `APP_ENV`         | dev     | `development`, `staging`, `production`, `test` |
| `HOST`            | 0.0.0.0 | Listen host                          |
| `PORT`            | 8080    | Listen port                          |
| `SERVICE_NAME`    | gogo    | Gateway identity in the module registry |
| `SERVICE_VERSION` | dev     | Version in the module registry; overridden by the link-time build version in containers |
| `LOG_LEVEL`       | info    | `debug`, `info`, `warn`, `error`     |
| `LOG_FORMAT`      | depends | `json` (machine) or `text` (dev); defaults to `json` when `APP_ENV=production` |
| `READ_TIMEOUT`    | 5s      | Server read timeout                  |
| `WRITE_TIMEOUT`   | 10s     | Server write timeout                 |
| `IDLE_TIMEOUT`    | 60s     | Server idle timeout                  |
| `REQUEST_TIMEOUT` | 15s     | Per-request deadline (middleware)    |
| `SHUTDOWN_TIMEOUT`| 10s     | Graceful shutdown deadline           |
| `READ_HEADER_TIMEOUT` | 5s   | Server timeout for reading request headers (slow-loris guard) |
| `SLOW_REQUEST_MS` | 500     | Requests slower than this are logged at WARN; a bare number is milliseconds, a suffixed value is a duration; `0` disables |
| `MAX_HEADER_BYTES` | 1 MiB  | Cap on a request's headers           |
| `MAX_BODY_BYTES`   | 1 MiB  | Cap on a request's body (`http.MaxBytesReader`) |
| `TRUSTED_PROXIES`  | none    | CIDRs honored for `X-Forwarded-For` (e.g. cluster/cloud ranges); `all` trusts every peer |
| `ENABLE_DEBUG_ENDPOINT` | dev-only | Mounts expvar at `/debug/vars`; disabled in `production` |
| `TENANT_DEFAULT`  | default | Tenant resolved when no host mapping matches. With no `TENANT_*` set the process serves a single tenant named `default`, so a single-site deployment needs no change |
| `TENANT_HOSTS`    | —       | Comma-separated `host=tenant` pairs (e.g. `shop.example.com=ecommerce`); matching is case-insensitive, ignores the port and tolerates a trailing dot |
| `TENANT_IDS`      | —       | Comma-separated tenants served without a host (API-only clients); when empty the served set is derived from `TENANT_DEFAULT` + `TENANT_HOSTS` |
| `TENANT_TRUST_HEADER` | false | Honor the `X-Tenant-ID` request header instead of the host mapping. Off by default — there is no authentication yet, so an unauthenticated header would let any caller claim any tenant. Enable only behind a trusted authenticating proxy |
| `DATABASE_URL`     | —       | Postgres DSN for what this process owns (the inventory ledger, the sales history); embedded migrations run at startup. Unset means the in-memory repositories |
| `DB_MAX_CONNS`     | 10      | Postgres pool ceiling                 |
| `DB_MIN_CONNS`     | 1       | Postgres pool floor                   |
| `DB_PING_TIMEOUT`  | 2s      | Postgres ping deadline (readiness)    |
| `PRODUCT_ADDRESS`          | **required** | gRPC target of `product-service` (writes: `product.v1`) |
| `PRODUCT_LISTING_ADDRESS`  | **required** | gRPC target of `product-listing-service` (browse: `productlisting.v1`) |
| `PRODUCT_SEARCH_ADDRESS`   | **required** | gRPC target of `product-search-service` (search: `productsearch.v1`) |
| `CATEGORY_ADDRESS`         | **required** | gRPC target of `category-service` (`category.v1`) |
| `PRODUCT_PLATFORM_SERVICE_TOKEN` | **required** | Bearer credential the gateway presents to authenticated product-platform gRPC services (at least 16 characters) |
| `KAFKA_BROKERS`    | —       | Comma-separated `host:port` of the product change feed. When set, `productevents` consumes `product.events.v1` and republishes it as `product.changed.*`, so a product changed elsewhere still drops this process's island caches. When empty, caches expire on their TTLs (30s for the storefront grid) |

**The four product addresses are required and have no default.** That is a
deliberate asymmetry with `KAFKA_BROKERS`: a missing address would otherwise
produce a permanently empty storefront that looks like a real answer — no
error, no log line an operator would notice. Failing at boot is loud and
immediate instead. `config.Validate()` also rejects a value that is not a
`host:port`, so a typo fails before a dial timeout.

## Production deployment (Kubernetes)

The repository ships a complete Kubernetes deployment under `deploy/k8s`
(namespace, ConfigMap, Secret template, the Postgres StatefulSet, the gateway
Deployment + Service + PodDisruptionBudget + HPA, an Ingress, and zero-trust
NetworkPolicies). `make docker-build` builds the image; `make k8s-apply` renders
and applies everything.

The product platform is **not** in this overlay. `product-service`,
`product-listing-service`, `product-search-service` and `category-service` are
deployed by the platform's own manifests, and the gateway reaches them by
address — see [D-018](ARCHITECTURE.md#d-018). Neither are Kafka or OpenSearch:
the platform's own outbox and feed replace the local CDC path, and its search
read model replaces a locally-maintained index.

### Architecture in the cluster

| Component  | Runs                                                        | Notes |
| ---------- | ----------------------------------------------------------- | ----- |
| `gogo-gateway` | the shell: home, seller, billing, ops; dials four product-platform contracts | scaled by HPA, `min=2`. Readiness aggregates every upstream breaker, so an instance that cannot reach its browse read model leaves the load balancer instead of serving a blank grid |
| `gogo-postgres` | Postgres 17 (StatefulSet, 8 GiB PVC)                    | the inventory ledger and sales history only; migrations run automatically at startup |

What remains here is deliberately small: the gateway and the two stores it
owns. There is no product authority in this namespace at all, so nothing in this
overlay can drift out of sync with the platform's product data — the failure
mode that ended the old two-writer arrangement.

The NetworkPolicies follow that shape. `gateway-ingress` allows egress to the
gateway's own Postgres, to the product-platform namespace **by namespace
selector** (the four gRPC ports, plus Kafka's advertised port for the change
feed), and to DNS. Cross-namespace egress cannot be expressed with a
single-namespace `podSelector`, which is why that rule names the namespace
instead of a pod label — the platform's deployments are not ours to label.

### Deploying

```sh
# 1. Build and push the image to your registry
make docker-build        # gogo:latest (currently built and smoke-tested)
docker tag gogo:latest <registry>/gogo:<tag> && docker push <registry>/gogo:<tag>

# 2. Point Kustomize at the image
cd deploy/k8s
kustomize edit set image gogo=<registry>/gogo:<tag>

# 3. Create the runtime secret (DB password / connection string)
kubectl create namespace gogo
kubectl -n gogo create secret generic gogo-secrets \
  --from-literal=POSTGRES_PASSWORD='change-me' \
  --from-literal=DATABASE_URL='postgres://gogo:change-me@gogo-postgres.gogo.svc.cluster.local:5432/gogo?sslmode=disable'

# 4. Apply the stack
kubectl apply --kustomize deploy/k8s
```

`make k8s-secrets CHANGEME=…` builds the same secret from one password.

Readiness/liveness use the built-in `/readyz` and `/healthz` endpoints.
Readiness is not just this process's own health: `ops` aggregates **every**
contributed upstream breaker, so a gateway that cannot reach its browse read
model is taken out of service until the platform recovers, instead of serving a
blank grid or a 503 to every user of that replica. The deployment uses
`maxUnavailable: 0` rolling updates, a `minAvailable: 1` PDB, and
resource/security defaults (`runAsNonRoot`, read-only root FS, dropped
capabilities, seccomp `RuntimeDefault`).

### Observability

- **Logs**: `APP_ENV=production` defaults to JSON log output via `LOG_FORMAT`,
  one object per line with `service`, `version` and `env` on every record —
  ready for fluent-bit/Loki/Datadog.
- **Metrics**: every pod serves Prometheus text at `/metrics`:
  request totals by method/status/route, an in-flight gauge, request-latency
  **histogram** buckets, Go runtime gauges (goroutines, GC cycles, heap), and
  **render-cache hit/miss counters** (`gogo_render_cache_hits_total` /
  `gogo_render_cache_misses_total`) — the ISR hit ratio tells you whether an
  island's TTL/invalidation policy is effective on a heavy-read storefront.
  `/debug/vars` (expvar) also exposes a `gogo_render_cache` snapshot; it is
  off in production unless explicitly enabled.
  Scrape annotations are set on the Services; pair with a ServiceMonitor if
  you run Prometheus Operator.
  One series group per upstream: `gogo_<upstream>_breaker_{state,successes_total,failures_total,rejects_total,trips_total}` — `product`, `product_listing`, `product_search`, `category`, plus the in-process `inventory`, `sales` and `auth`. Note the underscores: the services are hyphenated (`product-listing`) but a Prometheus metric name cannot contain a hyphen, so the exposition sanitizes it and keeps the real name in the HELP text.
  `deploy/prometheus/circuit_breaker_alerts.yml` alerts on all four product
  upstreams (open circuit, cluster-wide outage, pre-trip failure rate, rejects,
  trips) and `deploy/grafana/circuit_breaker_dashboard.json` graphs every
  upstream on one panel per signal.
- **Probes & registry**: `/module-registry` documents each running module
  and its version; `/readyz` aggregates every module health report *and* every
  upstream breaker.

### Notes and tuning

- `TRUSTED_PROXIES` must contain your ingress-controller/node ranges or the
  resolved client IP will always be the socket peer — adjust the default in
  `deploy/k8s/configmap.yaml`.
- The NetworkPolicies default-deny everything except the documented paths;
  if your ingress controller runs in a different namespace, you scrape from
  elsewhere, or the platform lives in a different namespace than `product`,
  update `networkpolicy.yaml` accordingly.
- **The product change feed is best-effort for caches, by design.** The Kafka
  consumer commits under its own group (`gogo.gateway`) and a record this
  version does not understand is skipped rather than fatal, so a platform that
  adds an event type cannot take the gateway's feed down. The consequence: a
  product changed while the gateway is disconnected leaves its island cache
  stale until the TTL expires (30s for the storefront grid). That is safe —
  the island is ISR with a TTL and every miss re-reads through the browse read
  model — which is why `KAFKA_BROKERS` is optional rather than required.
- HPA targets CPU/memory; it requires `metrics-server` (bundled with most
  managed clusters).
- The in-memory repository is the development/test fallback. Production must
  set `DATABASE_URL`, or multi-replica pods will each hold their own empty
  inventory ledger and sales history.
- **Tenant isolation does not cover product data yet.** The platform has no
  tenant concept, so product reads are global while everything this process
  owns is tenant-scoped. See [D-011](ARCHITECTURE.md#d-011) and
  [D-018](ARCHITECTURE.md#d-018) — it is an upstream fix, not a config one.

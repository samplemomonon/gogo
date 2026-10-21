# Gogo Architecture

Canonical governance document for the Gogo codebase. Its audience is every
maintainer — including the one reading this in 2046. If a rule here and a
piece of code disagree, the rule is the intent and the code is the debt to fix.

Companion docs:

- [`README.md`](README.md) — feature tour and routes.
- [`MIGRATION_GUIDE.md`](MIGRATION_GUIDE.md) — the forward migration roadmap
  toward the foundation packages. This is the **single** roadmap; do not fork
  it into other files.
- [`FOUNDATION_SUMMARY.md`](FOUNDATION_SUMMARY.md) and
  [`internal/shared/foundation/README.md`](internal/shared/foundation/README.md) —
  inventory of the foundation packages and their contracts.

---

## 1. System Shape

Gogo is a **federated modular monolith**. One process runs a shell/gateway
that discovers feature modules through their manifests (`/module-registry`) and
mounts them. Modules federate HTML fragments the way JavaScript remotes
federate components, and communicate through a shared in-process event bus.

Three kinds of units live under `gogo/internal`:

| Unit | Path | Role |
| ---- | ---- | ---- |
| Team-owned vertical | `internal/features/<feature>/**` | One team's whole unit ([D-023](#d-023)): the backend hexagon at the feature root (`domain`, `application`, `infrastructure`, `presentation`, `compose`), its UI under `ui/`, its `routes.go`, its `OWNERS.md`, and — when a real cross-team consumer exists — its `public/` surface ([D-021](#d-021)). A vertical owns its remote contracts (e.g. `inventory.v1` gRPC) and never imports another vertical. |
| Shared | `internal/shared/**` | Cross-cutting infrastructure: the event envelope (`events`), the kernel contract layer (`kernel` + `foundation`), module lifecycle (`module`), web helpers (`web`), the remote-client kits (`grpcconsumer`, `productconsumer`). |
| Composition root | `internal/shell` | The only place that mounts verticals. It owns the DI container, the wiring of the shared event bus, the in-process connections, and the gRPC connections to the product platform. |

Remote contracts that must never drift live in their own Go modules under
`contracts/` — not in `internal/`. Two families live there, and the distinction
matters:

| Family | Contracts | Who serves them |
| ------ | --------- | --------------- |
| In-process modules | `inventory/v1`, `sales/v1`, `auth/v1` | gogo itself. The vertical mounts in this process; the contract is the seam that keeps its own `ui/` — and everyone else — from importing the backend. |
| Product platform | `product/v1`, `productlisting/v1`, `productsearch/v1`, `category/v1` | Standalone services in another repository/deployment. gogo is a **client** of these and stores no product data ([D-018](#d-018)). |

The product platform's contracts are vendored from the services that own them,
with `go_package` rewritten to gogo's module path — the same precedent
`product-service` and `aggregator-service` already set. Do not hand-edit a
vendored contract to add a field gogo wants; add it upstream.

Two remote-client kits sit between callers and contracts:

- **`internal/shared/grpcconsumer`** — the convention every outbound call follows:
  tenant stamp, circuit breaker, transport-vs-business error classification
  ([D-016](#d-016)).
- **`internal/shared/productconsumer`** — the only package allowed to import a
  product-platform contract. It maps each wire shape into one neutral `Core`
  ([D-018](#d-018)).

> **Ownership re-home complete ([D-023](#d-023)).** Each route's team owns its
> backend and its UI in one directory,
> [`internal/features/<feature>`](docs/DECISION_TEAM_OWNED_VERTICALS.md) — the
> "team-owned vertical" (one team, one project, one image, the Keycloak model).
> `internal/modules/` and `internal/pages/` are retired; `internal/bootstrap` is
> `internal/shell`. The rules below are unchanged in responsibility — they are
> re-expressed on the vertical: a vertical is a closed module (only its own
> subtree, `internal/shared`, or `gogo/contracts`), and the layer matrix applies
> per vertical. `TestRetiredRootsStayRetired` fails the build if either retired
> root comes back, and `make feature` scaffolds a new vertical.

## 2. Import Rules (enforced)

These are the load-bearing boundaries. They are enforced by
`internal/architecture`, which reads a committed snapshot of the import graph —
`import_graph.txt`, derived from `go list` (the Bazel/Gazelle derivation this
paragraph used to describe rotted; the test file's header records why). Test
imports are excluded: tests may bridge, production code may not.

The snapshot can go stale, so staleness is itself a test
(`TestImportGraphIsCurrent`), which re-derives the graph from the working tree on
every run; `make check` fails if it is out of date. After adding a package or
changing an import, run:

```bash
make -C gogo arch-graph
```

and commit the result. A boundary change then shows up in review as a diff.

| # | Rule |
| - | ---- |
| 1 | Runtime code under `internal/features/<f>/**` may import only its own subtree, `internal/shared/**`, and `gogo/contracts/**` — never another vertical, and never a retired root. A vertical's `ui/` consumes its own backend and every other vertical over the wire contracts (gRPC clients in `<f>/ui/infrastructure/`), never by importing them. The one deliberate cross-team exception is `public/` (below). Enforced by `TestFeatureVerticalIsAClosedModule`. |
| 2 | A vertical's `ui/**` reaches no backend internals — not its own, not another vertical's — and the shell reaches a vertical only through that vertical's `compose/` package, its root package, or its `ui/`: never into `domain`, `application`, `presentation` or `infrastructure`. Enforced by `TestCompositionLayerReachesNoVerticalInternals`. |
| 3 | A vertical never imports another vertical's backend: cross-vertical contact is events on the bus — the shared event vocabulary (type constants and payload structs) lives in the versioned `contracts/<name>/v1` module that both sides import — and, for UI only, the declared `public/` surface ([D-021](#d-021)). Today no vertical imports another. |
| 4 | Only `internal/shell` imports verticals. It is the composition root. |
| 5 | The platform-owned `design-system/web/ui` Go module **never** imports application or domain packages. Its standalone module can depend only on the standard library and Templ; web pages, modules and shell compose it through `gogo.design/system/web/ui`. Cross-platform tokens and governance live at repository-root `design-system/`. |
| 6 | The product platform's contracts (`contracts/product/v1`, `productlisting/v1`, `productsearch/v1`, `category/v1`) are imported by exactly one package: `internal/shared/productconsumer`. `internal/shell` is the sole exception — it dials the four connections and hands them to the kit. No vertical's `ui/` or backend, and no other shared package, may import them. |
| 7 | No package under `internal/features/catalog/`, `internal/features/product*` or `internal/features/listing/` may exist, and the retired roots `internal/modules/` and `internal/pages/` stay retired (`TestRetiredRootsStayRetired`). The platform owns the product aggregate ([D-018](#d-018)); nothing here holds product state or writes a product. |

Rules 6 and 7 are what stop the product domain from quietly creeping back in.
They are structural rather than conventional: rule 7 fails the build the moment a
`catalog` or `product` vertical reappears under `internal/features/` — or either
retired root comes back — whether or not anyone remembered this paragraph.

Rule 1's `public` exception is the one deliberate cross-team UI contract
([D-021](#d-021)): a vertical's `public` package is its declared page-facing
surface, owned and compatibility-managed by the vertical's team, consumed by
other verticals' `ui/`. No vertical ships a `public` package today; none is
added speculatively, and the first one is extracted with its first real
consumer — when it appears, the closed-module rule
(`TestFeatureVerticalIsAClosedModule`, [D-023](#d-023)) is extended with the
grant. The module-shaped machinery that enforced the grant through D-023 (the
`public` grant predicates, `TestModuleToModulePublicIsNotGranted` and the
synthetic fixtures in `ui_contract_test.go`) was deleted in D-023 Phase 7
together with the retired roots: it had no subject, and the closed-module rule
currently admits no cross-vertical edge at all.

The classic hexagon rules short of the above still apply per vertical:
`presentation` and `infrastructure` both depend on `application` ports, never
the reverse; `domain` depends on nothing internal.

## 3. Events and the Kernel Contract Layer

Two event types exist on purpose — do not try to collapse them.

- **`events.Event`** (`internal/shared/events`): the **canonical in-process
  envelope** (`{Type, Payload}`). Mutations publish `events.Event`;
  `testutil.TestEventBus` records it; `productevents` produces it from the
  platform's Kafka feed.
- **`kernel.Event`** (`internal/shared/kernel`): the **consumer contract** of
  the foundation layer. The adapter
  (`internal/shared/foundation/adapter.EventBusAdapter`, which also implements
  `kernel.EventBus`) wraps a shared bus so foundation consumers subscribe with
  `kernel.EventBus.SubscribeTypes(ctx, handler, types...)` where `handler` is
  `func(ctx, kernel.Event) error`.

Decision recorded in [D-004](#d-004). Rule of thumb:

- **Producers stay on `events.Event`** — it is the canonical shape the bus and
  every consumer already speak; adapting it upstream buys nothing.
- **Consumers** that want foundation guarantees (subscription lifecycle,
  kernel-shaped handlers) **subscribe through the kernel contract**.

`kernel.EventBus` also declares `Subscribe(ctx, handler)` and returns
`(Subscription, error)`; `MemoryBus` and `AsyncBus`
(`internal/shared/foundation/eventbus`) and the adapter all satisfy it.

### Where product events come from now

gogo no longer produces product events, because it no longer owns the product
aggregate. `internal/shared/productevents` consumes the platform's Kafka topic
`product.events.v1` and republishes each record onto the in-process bus in
gogo's own vocabulary (`product.changed.created`, `.updated`, `.deleted`).
That is the only channel by which a product change reaches this process — and
it is what lets the billing activity feed report writes made *outside* gogo
(in product-service, by another deployment). `KAFKA_BROKERS` is optional:
unset means no consumer and no republication, so change-driven activity lags
until its next read; the storefront grid is TTL-bounded either way (30 s).

`ErrSkip` is the deliberate escape hatch: a record this version does not
understand is skipped and logged, not fatal, so a platform that adds an event
type cannot take the gateway's feed down.

## 4. Contract Stability Policy

Longevity comes from contracts that move slowly.

1. Every released contract is **additive-only**. New RPCs, new optional fields,
   new event types are safe. Renaming, removing or re-typing a released contract
   is a major breaking change and must go through a new versioned contract
   module.
2. Event type constants and payload structs are defined **once**, in the
   contracts module; the owning module re-exports them under
   `application/events` for its own internals. Consumers import the contract —
   never the owning module's internals, never string literals in business code.
3. Cross-service event payload decoding happens at the boundary (`Decode`),
   not one-off in each consumer.
4. **Vendored platform contracts are upstream's to change.** The four contracts
   in the product family are copied from the services that serve them; gogo does
   not own their evolution. A gogo-specific need is answered by a new optional
   field upstream, never by a local edit to a `.proto` it does not own — a local
   edit is silently reverted the next time the contract is re-vendored.

## 5. Decision Log

Why the codebase looks like it does. Keep entries terse; every future flagship
decision earns a new entry.

### D-001 · Federated modular monolith, not microservices
**Date:** original · **Status:** accepted
Single process, modules federated by manifest, shared in-process bus. Gives
module isolation and deployment simplicity; the remote contracts (`inventory.v1`,
`sales.v1`, …) already exist so any module can be lifted out later without
redesign. The product domain took the other path deliberately, and earlier than
planned — see [D-018](#d-018).

### D-002 · Hexagonal layout per module
**Date:** original · **Status:** accepted
`domain` → `application` (ports in, ports out) → `infrastructure`/`presentation`.
Use cases are independent packages under `application/use-cases/*`. Keeps the
core free of transport and storage so both the pages and the gRPC surface share
one facade.

### D-003 · `events.Event` is the canonical envelope; contract-keyed subscriptions
**Date:** original · **Status:** accepted
`SubscribeTypes(handler, types...)` returns a cancellable `Subscription`;
subscribers declare interest by type and never type-switch on unrelated events.
The bus is the transport-neutral seam between modules and pages.

### D-004 · Consumers on `kernel.Event`, producers on `events.Event`
**Date:** 2026-09-25 · **Status:** accepted
The foundation layer introduced a kernel event contract for consumers (billing
migrated first). The **producer side stays on `events.Event`**: the envelope
doubles as the in-process wire contract (`MarshalEvent`,
`testutil.TestEventBus`), so migrating the producer forces churn through the
transport without architectural gain. Kernel adoption is a **consumer-side**
concern.

### D-005 · Pages consume modules over remote contracts only
**Date:** original · **Status:** accepted
Every read-side renders its own read models from gRPC clients in its
`ui/infrastructure/` — [D-023](#d-023) re-homed the pages, so `home` and the
seller console live in `features/home/ui` and `features/sales/ui` — which
delegate wire/transport to the shared `productconsumer` / `grpcconsumer` kits
and map into vertical-local read models. Enforced by import rules
[1](#2-import-rules-enforced) and [6](#2-import-rules-enforced), incl. keeping
read-model mapping in each vertical rather than merging the clients.

### D-006 · Name-keyed DI container as composition root
**Date:** 2026-09 · **Status:** accepted
`internal/shell/di.go` wires config, buses (shared + kernel view), logger
and the module registry through `foundation/di` (`NewBuilder().AddSingleton(...)`).
`BuildGateway` resolves from the container so wiring is explicit, auditable and
tested (`di_test.go`).

### D-007 · Foundation packages are standalone and dependency-light
**Date:** 2026-09 · **Status:** accepted
`internal/shared/foundation/**` (kernel, di, eventbus, adapter, audit,
specification, validation) import only the standard library and `kernel`. A
package that needs a cross-cutting dependency must accept it as a parameter,
never import it. This keeps the foundation reusable for decades. (The generic
pattern packages this originally listed — repository, cache, modulefactory,
patterns, httpkit, example, cqrs, outbox, saga, idempotency, uow, domainevent —
were retired in [D-017](#d-017); the capabilities they named live concretely in
the modules.)

### D-008 · Bootstrap is the only composition root
**Date:** original · **Status:** accepted
Verticals never wire each other. `internal/shell` (renamed from
`internal/bootstrap` by [D-023](#d-023)) owns the full object graph and its
lifecycle; verticals volunteer their manifests and health and are drained in
reverse order on shutdown.

### D-009 · Reliable publication via a transactional outbox
**Date:** 2026-09-25 · **Status:** accepted · **Superseded in part by [D-018](#d-018)**
Event publication must not be lost between a commit and its event. The
(now-deleted) catalog's write path ran the mutation and the event record in **one
transaction** (`ports.Transaction`; `application.Service` owned the boundary),
and with Postgres the event landed in an `outbox` table
(`infrastructure/postgres.OutboxStore`) that a relay (`postgres.Relay`) drained
to the bus at-least-once with `FOR UPDATE SKIP LOCKED`, so replicas could relay
concurrently. The in-memory backend kept synchronous publication, so tests and
local development saw events before the request returned.

**What survives:** the pattern and the reasoning, which now belong to
product-service's own write path (D-018). **What went with the catalog:** the
implementation. gogo publishes no product events, so gogo has no outbox — the
platform does, and gogo consumes its feed ([section 3](#3-events-and-the-kernel-contract-layer)).
Any module added here that publishes events should follow this decision's shape
rather than re-deriving it.

### D-010 · *(retired number — no entry was ever written here)*
**Date:** — · **Status:** tombstone, number permanently vacant
The log jumps D-009 → D-011. That gap is original, not a loss: no revision of
this file ever contained a D-010 entry (`git log --all -S "D-010" --
ARCHITECTURE.md` returns nothing; the string elsewhere is only paperwork about
the gap — `docs/FOLLOW_UP.md` item 7, `docs/TEAM_TOPOLOGIES_PRD.md` §0.4), so
there is no text to recover. The number is
retired rather than reused — reusing it would make a decision un-findable under
a number that already has a history elsewhere, and future readers would have to
guess whether something was deleted. `docs/TEAM_TOPOLOGIES_PRD.md` §0.4 records
this as the closure of that prerequisite.

### D-011 · Multi-tenancy: shared schema with tenant_id + RLS, tenant on event payload
**Date:** 2026-09-27 · **Status:** accepted · **Carries a known gap: see [D-018](#d-018)**

**Problem:** A single backend must serve multiple sites (tenants) without their
data ever mingling. The choice of storage model and context propagation has
decade-scale consequences: migration cost grows super-linearly with row count.

**Decision:** One Postgres database, one schema, `tenant_id TEXT NOT NULL
DEFAULT 'default'` on every business table. Tenant is a trailing segment on
ISR cache keys (`<island>.<tenant>`) so family prefix invalidation keeps
working. The tenant is a **first-class field on every domain event payload**,
never inferred from ambient request context by a subscriber — this prevents
spoofing and makes a projection derive its partition key from the authoritative
event.

**Alternatives considered:**
- **Database-per-tenant / schema-per-tenant:** See Azure SaaS guidance — only
  justified when a tenant has hard data-residency/compliance requirements or
  dominates the workload. Operational cost (N migrations, N connection pools,
  N backups) is the tax nobody budgets for. Our default tenant id on every
  table keeps the door open to extract a large tenant later.
- **Tenant in protobuf metadata only:** Our events cross a gRPC stream and the
  Kafka CDC feed as JSON; the
  protobuf spec warns JSON/text deserialization may fail on new fields. A
  payload field is forward-compatible on both binary and JSON paths.
- **Ambient context for projections:** A consumer that reads tenant from
  inherited request context (instead of the event payload) creates a
  circular dependency: the projection needs the tenant to refetch the
  authoritative row, but the refetch is itself tenant-scoped. The tenant must
  be on the event.

**Defense in depth:** PostgreSQL Row-Level Security (RLS) as a second
enforcement layer — "belt and
suspenders" (a missed `WHERE tenant_id = ?` is a leak; RLS blocks it at the
database). RLS is enabled and tested; its planner overhead is accepted. Today it
covers the tables gogo still owns (`inventory_stock`, the movement ledger, sales
history).

> **Known gap (not a decision to defer — a fact about the platform).** The
> platform's product data has **no tenant concept**: product-service's
> migrations carry no `tenant_id`, and `product.v1` / `productlisting.v1` /
> `productsearch.v1` carry no tenant field. So gogo stamps a tenant on every
> outbound product call, and those services ignore it — product data is global,
> while everything gogo owns is tenant-scoped. (category-service is the one
> exception: merchant categories are tenant-scoped through the `x-tenant-id`
> metadata gogo already sends — see its `0002_merchant_categories.sql` and
> `internal/tenant`.) That is a real inconsistency, not a stylistic one, and it
> is invisible at runtime: one tenant's products are visible to all. Closing it
> is upstream work in the platform (add `tenant_id`, make the contracts carry
> it), not something gogo can enforce. See [D-018](#d-018) and
> `docs/FOLLOW_UP.md`.

**Protobuf field addition:** Adding a scalar `tenant` field (next unused
number, never reused) to `ProductEvent` is **wire-safe** per
[protobuf.dev](https://protobuf.dev/programming-guides/proto3/#updating-a-message-type)
("Adding new fields is safe. Old binaries simply ignore the new field"). We
reserve deleted numbers and pin forward-compatibility in
`events/wire_test.go` (unknown JSON field tolerated).

**Cache invalidation:** `Composer.Invalidate` is deliberately **family-wide**
(`InvalidatePrefix(island)`). A subscriber usually cannot tell which tenant a
mutation belonged to; over-invalidating costs a re-render, under-invalidating
serves stale HTML. The trailing tenant segment on the key keeps prefix
invalidation semantics intact.

**Open follow-up:** the platform's tenant gap (above) is the live item here;
tenant-scoping of the read models that remain is settled.

### D-012 · Listing read model fed by Debezium CDC → Kafka; no app broker
**Date:** 2026-09-27 · **Status:** accepted · **Overtaken by [D-018](#d-018)**

**Problem it solved, and it solved it:** the listing read model needed to stay
fresh in the split deployment, and the storefront needed real full-text search.
An earlier shape shipped a RabbitMQ topic exchange that the catalog *published*
to (a forwarder on the event bus) and the listing *consumed*. That duplicated a
transport the CDC path would need anyway, and it only carried the events the
application chose to emit. The decision was one change feed, at the data layer,
with Kafka as the only cross-process transport.

**What remains true:** the reasoning is the platform's now. product-listing-service
and product-search-service are fed by product-service's own outbox → Kafka feed
(`product.events.v1`), not by a local Debezium tail. The general point this
decision earned — *a projection is fed from an authoritative, replayable
stream, not from whatever events the application felt like emitting* — is why
that shape was acceptable to build on.

**What went with it:** the local CDC machinery. Debezium, the OpenSearch index and
the in-process listing module were removed with the catalog ([D-018](#d-018));
`deploy/k8s` no longer ships them and `gogo/docker-compose.yml` no longer runs
them. gogo consumes the resulting feed (`productevents`) but maintains no
projection of product data.

**Worth remembering for the next local read model:** a WAL carries the
*authoritative row*, so an out-of-band change projects too and the row travels
whole; Kafka's committed offsets make the feed replayable across a restart.

### D-013 · Retiring the catalog's name-search field without a v2 bump
**Date:** 2026-09-27 · **Status:** accepted · **The contract it applied to is gone ([D-018](#d-018))**

**What it decided:** `catalog.v1.ListProductsRequest.query` (field 4) was a name
filter no first-party consumer used, yet it forced a `pg_trgm` index on
`products` to keep the capability working. The capability was retired rather
than versioned: the field was removed and both its number and name reserved
(`reserved 4; reserved "query";`), so no tag could be reused and
JSON/TextFormat still parsed.

**What survives, and it is the useful part:** *reserving the number and the name
when deleting a field is the cheap way to make a breaking change wire-safe for
existing consumers.* Nothing in the current code needs the reservation trick —
`catalog.v1` went with the catalog — but the reasoning applies to every contract
in `contracts/`, and `buf breaking` still enforces it.

**Why not a v2 bump:** a parallel `catalog.v2` package would add a whole second
surface to protect a field nobody called. The contract-stability policy
([section 4](#4-contract-stability-policy)) reserves breaking changes for
consumers that exist outside this monorepo.

### D-014 · Seller console as a page; inventory owns stock
**Date:** 2026-09-27 · **Status:** accepted · **The catalog half is gone ([D-018](#d-018))**

**Problem:** The gateway needed the richer seller/storefront console (products
with SKU/category/supplier/pricing/unit, plus stock management) that the tiny
admin CRUD page did not provide. Two questions had decade-scale consequences:
where the console lives, and which unit owns stock.

**Decision:** Replace the `admin` page with a `seller` page
(`internal/features/sales/ui`, re-homed by [D-023](#d-023)) that consumes
remote contracts over the same bufconn/network transport a split deployment
uses, and add an **inventory module** (`internal/features/inventory`) that owns
the sellable stock quantity and
an append-only `inventory_movements` ledger. `AdjustStock` is atomic
(conditional `UPDATE … WHERE stock + delta >= 0`), idempotent on a non-empty
`reference`, and rejects oversell with `FailedPrecondition`.

**Alternatives considered:**
- **Keep stock in the product aggregate** (an `AdjustStock` RPC on the product
  contract): rejected — it welds an inventory concern into the product aggregate
  and forces the storefront's read model to be the write authority.
- **A second store / dual write**: rejected for the same reason as D-011 — two
  authorities drift.
- **Event-driven eventual decrement for POS**: deferred. The atomic synchronous
  `AdjustStock` is the correct primitive; the sales module composes it (D-015)
  without changing the contract.

**Trade-off accepted:** at the time, the catalog kept a denormalized `stock`
column that the storefront read while inventory wrote it. D-018 removed the
denormalization entirely — the platform's product contracts carry no stock, so
there is nothing to mirror and inventory's `inventory_stock` table is the only
copy. That is a strictly better position than this decision originally accepted.

**Revisit when:** a split `inventory` service is extracted (the contract already
exists), or WMS work (bins, stocktake, inbound, transfers) lands — it should
consume `inventory.v1` rather than re-implement stock.

**Lost with the catalog:** supplier management. Suppliers were product-adjacent
reference data held by the catalog, and no service in the platform owns them, so
the seller console has no supplier source. This is a deliberate, recorded gap
rather than an oversight — see [D-018](#d-018).

### D-015 · Sales/POS composes inventory; a sale's stock effect is the inventory's business
**Date:** 2026-09-27 · **Status:** accepted

**Problem:** A seller needs a point-of-sale (checkout → reserved stock → recorded transaction, void → stock restored). The POS must not re-implement stock logic or duplicate the ledger.

**Decision:** The sales module (`internal/features/sales`, re-homed by
[D-023](#d-023)) owns the completed transaction and its lines. It **composes
`inventory.v1.AdjustStock`** for the stock effect: on checkout it calls
`AdjustStock(-qty, "sale", reference)` per line (atomic, idempotent on the
transaction number); on store failure it compensates the lines already applied;
on void it calls `AdjustStock(+qty, "void", reference)` and then marks the
transaction voided. The inventory module remains the sole stock owner — sales
never writes `products.stock` directly.

**Contract surface:** `sales.v1` gRPC contract (`Checkout`, `Get`, `List`, `Void`). The seller console (`internal/features/sales/ui`) consumes it as a remote peer over bufconn, alongside `inventory.v1` and the product platform.

**Why not `inventory.v1` exposing a `Checkout` RPC:** stock adjustment is a primitive; a sale carries its own invariants (payment ≥ total, void rules, line-item audit trail) that belong in a dedicated sales aggregate. Composition keeps the inventory contract stable and reusable for future WMS consumers.

**Trade-off accepted:** a sale touches two modules in one logical operation (sales + inventory). In the monolith the bufconn hop is ~microseconds; in a split deployment the latency is the network RTT. The saga-style compensation (stock given back on failure) guarantees consistency without distributed transactions. A future split deployment may add an outbox on the sales side if at-least-once publication becomes necessary.

**Revisit when:** an outbox-relay is needed for the split, or a loyalty/discount engine composes `sales.v1`.

### D-016 · One shared gRPC call path for remote consumers (`shared/grpcconsumer`)
**Date:** 2026-09-28 · **Status:** accepted

**Problem:** Every outbound consumer call repeats the same four obligations:
stamp the request with the tenant, run it under a circuit breaker, count only a
*transport* failure against that breaker (a business rejection is the dependency
working), and project the answer into a transport-independent error vocabulary.
Hand-rolled, this produced six near-identical eight-line blocks across the
catalog and listing consumers, the seller's inventory and sales clients and the
two auth clients — six chances to get the open-circuit case wrong.

**Decision:** `internal/shared/grpcconsumer` owns the convention once, as a
`Caller` plus generic `Do[T]`/`DoUnit`, fed by a per-upstream `Vocabulary`. The
contract kits keep what is genuinely theirs — the wire↔core mapping and the
error vocabulary of their own contract — and call
`Do`/`DoUnit` for the mechanics. The package owns no wire types and imports no
page or module package, so it stays in the extractable platform layer.

**Consequence:** a consumer cannot forget the tenant stamp, cannot open a
breaker on a business answer, and cannot lose the outage sentinel through
wrapping — the sentinel survives `errors.Is` by construction. `productconsumer`
([D-018](#d-018)) follows the same split of concerns: mechanics from
`grpcconsumer`, vocabulary and mapping from the kit.

### D-017 · Retire the superseded generic foundation packages
**Date:** 2026-09-28 · **Status:** accepted

**Problem:** `internal/shared/foundation` carried a set of generic pattern
packages — `outbox`, `saga`, `idempotency`, `cqrs`, `uow`, `domainevent`,
`cache`, `patterns`, `modulefactory`, `repository`, `httpkit`, plus the
integration `example` — that **no runtime code imported**. Meanwhile the
project implemented each capability concretely where its data lives: a
transactional outbox ([D-009](#d-009)), saga-style idempotent
compensation in sales ([D-015](#d-015)), reference-keyed idempotency in
inventory/sales, a CQRS read model, and a unit-of-work
`ports.Transaction`. The generic packages were a second, unused
owner for concepts the codebase already owned — two places to change a
behavior, and neither of the generic ones wired.

**Decision:** delete the superseded generic packages. Keep only the foundation
that is actually wired (`di`, `eventbus`, `adapter`) and `kernel`. Keep `audit`,
`specification` and `validation` for the moment, because they name
capabilities/patterns the app has **not** adopted; their adoption-or-removal is
tracked in [`docs/FOLLOW_UP.md`](docs/FOLLOW_UP.md).

**Rationale:** an abstraction with no consumer makes ownership ambiguous — a
reader cannot tell whether `foundation/outbox` is *the* outbox or a discarded
one, and answers "where does this behavior live?" wrongly. The concrete
implementations are the source of truth. The project still follows the patterns
these packages named (transactional outbox, per-hop idempotency keys,
idempotent and commutative compensation) — see AWS Prescriptive Guidance,
"Transactional outbox pattern", and microservices.io, "Saga" — but implements
them where the data is, not as a generic framework.

**Revisit when:** a second module genuinely needs one of these patterns in a
form the concrete implementation cannot serve. Then extract it with a real,
named consumer, per [D-007](#d-007).

### D-018 · The product domain leaves gogo; the platform owns it
**Date:** 2026-10-01 · **Status:** accepted

**Problem:** gogo owned a product CRUD module (`internal/modules/catalog/product`)
while four standalone services — `product-service`, `product-listing-service`,
`product-search-service`, `category-service` — implemented the same aggregate,
in another repository, with their own database and their own contracts. Two
authorities for one row: gogo's write path and the platform's write path could
both claim to be current, and nothing in either codebase could tell. Every
product read the storefront served was answerable from two sources that could
disagree. This is the exact failure D-011 was written to prevent ("two
authorities drift"), reached by a different road.

**Decision:** the platform owns the product aggregate outright. gogo deletes its
product CRUD module and every piece of machinery that only existed to support
it:

| Removed | Replaced by |
| ------- | ----------- |
| `internal/modules/catalog/**` (incl. `product`) | `product.v1` writes, `productlisting.v1` / `productsearch.v1` reads |
| `internal/modules/listing/**` (its only source was `catalog.v1`) | `product-listing-service` / `product-search-service` |
| `contracts/catalog/v1`, `contracts/listing/v1` | vendored `contracts/{product,productlisting,productsearch,category}/v1` |
| `internal/shared/catalogconsumer`, `listingconsumer` | `internal/shared/productconsumer` |
| `cmd/catalog`, `cmd/listing` | nothing — one binary (`cmd/server`) |
| `internal/bootstrap/event_relay.go` (gRPC event relay) | `internal/shared/productevents` (Kafka `product.events.v1`) |
| `products.stock` mirror + the migrations that made it | `inventory_stock` (owned by the inventory module) |
| Supplier management | **nothing** — no platform service owns suppliers (see below) |

**Adapter layer, keeping gogo's shapes.** `productconsumer` maps each contract's
wire types into one neutral `Core`, so pages and modules keep gogo's existing
read models and every call still carries gogo's conventions (tenant stamp, breaker,
error vocabulary — `grpcconsumer`, D-016). Three conversions are load-bearing
and all are lossy in one direction only, which the package documents at the
type:

- `int64` product IDs → **decimal strings**, because gogo's own modules key
  products by `TEXT` (inventory's `(tenant_id, product_id)` primary key, the
  movement ledger). Decimal stringification is exact and round-trips.
- `price_cents` minor units are **kept as-is**. gogo's `money` package formats
  IDR without decimals, so the units already agree and no conversion factor is
  applied. Currency is carried, not assumed.
- A field a given contract does not carry stays at its zero value — `Price` is 0
  on a search hit, `Barcode` is empty on every product (no platform contract has
  one). **Callers must not read an absent field as a real zero**; that is the
  one way this mapping can mislead, so it is stated on the type.

**Why one kit for four contracts, when D-016 keeps independently versioned
contracts apart:** these are not four independent aggregates. They are four
*views* of one product domain, owned by one deployment, and a caller that can
write a product can also browse it. Four kits would mean four vocabularies and
four `Core` structs describing the same row, and a lowest-common-denominator
`Product` that omits whichever field the other three happened to agree on. So
`productconsumer.Core` is deliberately the *union* of what the contracts carry.
Import rule [6](#2-import-rules-enforced) makes it the only package that may
import any of them, so this cannot quietly become four.

**All five platform addresses are required configuration.** The gateway refuses
to boot without `PRODUCT_ADDRESS`, `PRODUCT_LISTING_ADDRESS`,
`PRODUCT_SEARCH_ADDRESS`, `CATEGORY_ADDRESS` and `MEDIA_ADDRESS`. The
alternative — dialing lazily and degrading — was
rejected because its failure mode is a *permanently empty storefront that looks
like a real answer*: no error, no log line the operator would notice, and search
results that are legitimately empty. Failing at boot is loud and immediate.
`KAFKA_BROKERS` is the opposite case and is optional: without it the gateway
serves on island-cache TTLs (30s for the storefront grid) instead of dropping
caches the moment a product changes elsewhere. That degradation is correct, just
staler, so it is allowed to be configured away.

**Failure behaviour is per-upstream, not global.** Browse reads, search and
product writes have separate breakers, so a search outage returns
`200 OK` with a usable storefront (search disabled) while a *browse* outage
returns **503** — the page that cannot be answered honestly is not served as if
it could. `pagekit.ClassifyError` maps the breaker sentinel to that status;
`WithErrorStatus` carries it to the handler. One breaker across all four would
have made every page 503 for any single outage, which is strictly worse.

**Trade-offs accepted:**

- **No supplier management.** Suppliers were product-adjacent reference data
  held by the catalog, and no service in the platform owns them. The seller
  console's supplier section is gone with the capability. Inventing a supplier
  store in gogo would recreate the two-authority problem this decision exists to
  end; the honest move is to record the gap.
- **No tenant isolation for product data.** gogo is multi-tenant
  ([D-011](#d-011)); the platform's **product data** services have no tenant
  concept — product-service's migrations carry no `tenant_id`, and no tenant
  field exists in `product.v1` / `productlisting.v1` / `productsearch.v1`
  (category-service is the exception: merchant categories are tenant-scoped via
  the `x-tenant-id` metadata — see [D-011](#d-011)). gogo stamps a tenant on
  every product call and those services ignore it. This is a real leak between
  tenants, it is invisible at runtime (one tenant's products are visible to all),
  and it **cannot be fixed in gogo** — it needs `tenant_id` added upstream. Tracked
  in `docs/FOLLOW_UP.md`.
- **Read-after-write lag.** A write goes to `product-service`; the storefront
  reads from the listing/search projections, so a product the seller just created
  may not appear in the grid for a moment. This is inherent to a read-model
  architecture and is the platform's documented projection contract, not a
  gogo bug.
- **gogo no longer publishes product events,** so it has no outbox and no event
  relay. The platform's outbox and Kafka feed replace both
  ([D-009](#d-009), [D-012](#d-012)).

**What survived, deliberately:** inventory, sales/POS, auth, billing and ops stay
as in-process verticals ([D-023](#d-023) re-homed them under
`internal/features/`). They are gogo's own aggregates and no platform service
duplicates them. Billing subscribes to `product.changed.*` for the activity
feed; the storefront grid is TTL-bounded (30 s) and deliberately does not
consume the event stream (`internal/features/home/ui/page.go`).

**What went, and why.** `media` and `reporting` were removed. Neither was gogo's
to own:

- `media` duplicated the external `media-service` — the same two-authorities-for
  one row that [D-018](#d-018) cured for product, reached by a different road.
  It was never mounted, and `contracts/media/v1` never existed, so no external
  consumer depended on it.
- `reporting` derived `dashboard_stats`, `low_stock_items` and `top_products`
  from Inventory, Sales and Catalogue state, in its own migration, written and
  read by nobody. A read model spanning three contexts cannot be stream-owned by
  any one team; it would have to be rebuilt inside whichever team needs it, and
  the honest place to rebuild it is where the data is. Its `reporting.v1`
  contract went with it, since the stubbed module was its only provider.

The rule that would have caught both is now the seventh import rule, extended to
media ([rule 7](#2-import-rules-enforced)).

**Guardrails.** Import rules [6](#2-import-rules-enforced) and
[7](#2-import-rules-enforced) are architecture tests, not conventions: rule 7
fails the build the moment a `catalog`/`product`/`listing` unit reappears
under `internal/features/` (or a retired root comes back), so the domain cannot
creep back in by accident.

**Revisit when:** the platform adds tenant support, or suppliers become a
platform concern. Until then gogo's product read path is defined entirely by
those four contracts.
### D-019 · Ports are registered once, and deployment publishes no host ports
**Date:** 2026-10-15 · **Status:** accepted

Every port lives in [`ports.txt`](ports.txt): the service, its kind, the
environment variable, the container port, the development host port, and — when
the port is also somebody else's documented default — *who*. The Go defaults,
the `EXPOSE` lines, the compose mappings and the aggregator's dependency targets
are checked against it by `internal/architecture/ports_test.go`.

**Staging and production publish no host ports.** Services reach each other over
the container network by service name. This is the load-bearing half of the
decision, because it makes most of the collision problem disappear: the registry
has a development column and no staging or production columns, because adding
them would imply a mapping nobody should create.

**Why one file and not documentation.** The port question was previously answered
by five config files, four Dockerfiles, five compose files and the aggregator's
defaults — seven places, no index, nothing that noticed when they disagreed. A
moving service changed its `GRPC_PORT` and the aggregator kept dialling the old
number, failing at request time with a connection error naming no configuration.

**Why collisions are recorded rather than fixed.** The gRPC range sits on
Prometheus's allocated range and, at 9092, Kafka's — Prometheus's own wiki marks
9092 `UNALLOCATED (to avoid collision with Kafka)`. Renumbering changes the
default every client and deployment manifest depends on, which is not worth the
blast radius while nothing is deployed. The gate therefore **refuses a new
collision that is not written down**, rather than refusing the four that are. That
is the same trade as `docs/ADR_AGGREGATOR_BOUNDARIES.md`: a rule that blocks a
known problem is a rule people route around, and routing around it is the failure.

**Known and recorded:** `category-service` and `media-service` have no module in
this repository, yet the aggregator's defaults dial `9090` and `9091` — Prometheus
and its Pushgateway. Run beside a running Prometheus, the aggregator sends gRPC
to Prometheus's HTTP port. They are described in `ports.txt` rather than listed as
rows, because the gate verifies services that exist.

### D-020 · Platform services authenticate their callers, on both transports
**Date:** 2026-10-15 · **Status:** accepted

Every platform service refuses callers that do not present a credential
configured with `INBOUND_SERVICE_TOKENS`, on HTTP and gRPC alike; the aggregator
presents `DOWNSTREAM_SERVICE_TOKEN`. A service with none configured does not
start.

This exists because [D-019's](#d-019) sibling decision — authenticating the
composition layer — was decorative without it. The services publish host ports in
development, and `product-service` exposed `POST /api/v1/products` with no
middleware, so everything the aggregator's `MUTATION_TOKENS` protected was
reachable directly.

Two consequences worth stating plainly. **Health and readiness probes are exempt**,
because an orchestrator answering 401 from a liveness probe restarts the
container, and restarting the process answering the probe turns a partial outage
into a total one. And **the credential is a shared secret, not an identity** — it
answers "may this caller talk to me" and nothing more; there is still no principal
and no per-caller authorisation.

The guard protects against *reachability*, not against an on-path attacker: the
gRPC connections use insecure transport credentials, so the bearer token travels
in the clear. That is recorded rather than discovered — see
[`ADR_SERVICE_TO_SERVICE_AUTH.md`](docs/ADR_SERVICE_TO_SERVICE_AUTH.md).

### D-021 · Pages may import a module's `public` surface, and nothing else
**Date:** 2026-10-16 · **Status:** accepted

[`docs/ADR_CROSS_TEAM_UI_CONTRACT.md`](docs/ADR_CROSS_TEAM_UI_CONTRACT.md)
narrows rule 1. A page may import `<module>/public` — the one package the owning
team declares and maintains as its cross-team UI surface, with integration tests
and a compatibility policy as its price — and no other runtime code under a
module: `domain`, `application`, `infrastructure`, `presentation` and `compose`
stay closed, and a page never imports another page. The owning team keeps
ownership of the feature when it publishes the contract; the page keeps the only
page-composition decision.

The grant is to **pages only**. Module-to-module imports stay event-based
(rule 3): a module may not consume another module's `public` either. A future
need for that is a separate ADR, not an edit to the import test.

Nothing uses the grant today — no `public` package exists, and none is added
speculatively. The change was enforcement-first: the page rule in
`TestImportBoundaries` was narrowed, not deleted, its failure message named the
ADR, `TestCompositionLayerReachesNoModuleInternals` extended the forbidden set
to `compose`, and because a negative rule that has never seen a positive case
cannot be distinguished from a broken parser, both predicates and the `public`
grant were pinned on synthetic graphs in
`internal/architecture/ui_contract_test.go`.

**Moved by [D-023](#d-023) (Phase 7).** The page tree is retired; the subject
of this decision is a vertical's `ui/`. The enforcement machinery was deleted
together with the retired roots — it had no subject, because the single
boundary rule (`TestFeatureVerticalIsAClosedModule`) currently admits no
cross-vertical edge at all — and the grant is re-extended into that rule when
the first real `public/` consumer appears. The decision itself (the grant's
shape and its limits) is unchanged.

### D-022 · The auth module owns the merchant relationship; only a merchant's members may change its products
**Date:** 2026-10-17 · **Status:** accepted

**Problem:** The authorization model is tenant-scoped RBAC
([ADR_IDENTITY_AUTHORIZATION.md](docs/ADR_IDENTITY_AUTHORIZATION.md)), and
tenant scope cannot answer "may this employee change *this* product?". Products
belong to a merchant, but no authoritative store-relationship model existed: no
aggregate recorded which employee owns which merchant or who its staff are. That
ADR deferred ReBAC until exactly that model had an owner and a lifecycle —
without it, "only the owning merchant's members" is unenforceable, and the
seller console's `seller.catalog.manage` check answers the wrong question
(tenant-wide) for a resource that is merchant-scoped.

**Decision:** the auth module owns three aggregates
(`internal/features/auth/domain`), and their tables are the store relationship
the ADR reserved:

| Aggregate | Answers | Lifecycle |
| --------- | ------- | --------- |
| `Merchant` | which shop, and which employee owns it | opened by an authenticated employee |
| `Membership` | that employee's role in the merchant: `owner` or `karyawan` | created with the merchant (owner) or on accepting an invitation (karyawan) |
| `MerchantInvitation` | an owner's offer to add a karyawan | pending → accepted \| revoked \| expired, one-time token, 7-day TTL |

Products in the platform carry `merchant_id` referencing these rows. There is
deliberately **no** foreign key to the auth database from product-service, for
the same reason `category_id` has none: a cross-service constraint couples two
deployables' migration order. Auth validates a merchant over `auth.v1` instead.

**The authority is merchant-scoped, and the PDP answers one question.**
`CanManageProduct(session, merchant)` allows the merchant's **owner** always, and
a **karyawan** only while the owner holds an active merchant-scoped
`seller.catalog.manage` grant. `grantableMerchantPermissions` in the auth
application layer is a code-owned allowlist that deliberately excludes the three
`authz.*` administrative keys, so a merchant owner can delegate selling work and
cannot delegate tenant policy administration through this path.

**Account roles are derived, never stored.** `ReservedRole`
(`pembeli` / `penjual` / `karyawan`) is a vocabulary, not a column: owning a
merchant *is* being a `penjual`, being a non-owner member *is* being a `karyawan`,
and every account is a `pembeli`. `domain.ReservedRoles` computes the labels
from the relationship data, so the account a customer registers with carries no
role row and granting a karyawan's permission changes no profile field. A
second role column would be a third authority that could disagree with the
relationships it summarizes.

**Enforcement sits at the seller console's write boundary.** Product create,
update and delete each resolve the target merchant and ask the PDP *before* the
write reaches the platform (`internal/features/sales/ui/handlers`). A deny is 403; an
auth outage is 503, so a failure is never read as a permission decision. Create
is additionally scoped to a merchant the employee may manage and stamps that
`merchant_id` on the new product; update and delete read the product's existing
`merchant_id` from the browse read model, because ownership is fixed at creation
(`UpdateProductRequest` carries no `merchant_id` — changing a product's merchant
is a separate, unbuilt operation).

**Two gaps recorded rather than hidden.** The seller console still *lists* the
whole tenant's browse page: product visibility was not narrowed to the member's
merchant, so the write gate is stricter than the read gate. And a karyawan's
merchant-scoped grant does not by itself open the console's tenant-level routes,
which still require a tenant RBAC capability (`make seed` grants one to the demo
`penjual`). Both are follow-ups, not claims.

**Verification found a shipped defect.** Running the seed against a live
database — the first time these `authorization_employee_roles` writes were
executed outside a unit test — failed with `column "revoked_by" does not exist`
(SQLSTATE 42703). `AssignRole` and `RevokeRoleAssignment` have always written a
column no migration created, so both PAP RPCs were broken against Postgres while
their in-memory and hermetic tests passed. Migration `0011` adds it. The same
exercise showed `merchant_memberships` had no employee foreign key while its
sibling `authorization_employee_roles` does, so a membership could outlive its
employee; migration `0012` adds it. Both are the argument for exercising an
adapter against a real database at least once: the unit tests could not see
either, and a green suite was not evidence.

**`make seed` provisions the two identities the model needs:** `pembeli@toko.com`
(a `pembeli`, no merchant) and `penjual@toko.com` (a `pembeli` **and** `penjual`,
owning `Toko Bunda`), both with PIN `123456`. It is idempotent, and it refuses
to run with `APP_ENV=production` because it creates a known-credential account
and a policy administrator.

### D-023 · Team-owned verticals: one team, one project, one image
**Date:** 2026-10-18 · **Status:** accepted — **implemented (Phases 0–7 complete, verified 2026-10-19)**

**Problem.** The governance is correct but **scattered**. A single feature team's
bounded context is spread across `internal/modules/<f>`, `internal/pages/<page>`,
`design-system/`, `internal/bootstrap`, the cross-team UI contract docs, and the
`internal/architecture` boundary tests plus its `import_graph.txt` snapshot. To
understand or re-own a feature, you read six files across three trees and reason
about five rules. That is a cognitive-load and ownership problem, not a
scalability one (7 modules, 4 pages, one team — see
[`DECISION_UI_OWNERSHIP.md`](docs/DECISION_UI_OWNERSHIP.md) §1.4).

**Decision.** Re-home ownership so each team owns **one vertical**: a route's
owning team owns its backend *and* its UI, together. The model is the
self-contained vertical Keycloak exemplifies — one deployable that ships both the
UI and the backend of a vertical. Under
[`internal/features/<feature>`](docs/DECISION_TEAM_OWNED_VERTICALS.md): the
existing hexagonal layers, plus `ui/` (the feature's pages/components, formerly
`pages/<page>`), `routes.go` (route ownership), `public/` (the D-021 cross-team
surface, declared in-vertical), `events.go`, `compose/` (its own graph), and
`OWNERS.md`. The composition root is `internal/shell` (renamed from
`internal/bootstrap`); it keeps only cross-cutting wiring. The old
`internal/pages/` and `internal/modules/` trees are retired, so a "page" is
either a vertical's own `ui/` or a shell entry that composes other verticals'
`public` surfaces.

**What is preserved, not changed.** UI ownership (a route has one owning team; the
owner decides what appears; cross-team UI only via the declared `public` surface;
slot/widget composition still not built), domain ownership (platform owns
product; inventory owns stock; sales composes `inventory.v1`; auth owns the
merchant relationship), and deployment ownership (one binary today; a vertical is
the lift-out seam — its `compose` is its root and the existing `*_connection.go`
in-process↔network dial swap makes promotion a config change).

**The single boundary mechanism.** The answer to "boundaries are scattered" is a
*containment* refactor, not more rules. A vertical is a **closed module to every
other vertical by construction** (Go `internal/` visibility) except for its
declared `public` package. Ownership is `OWNERS.md` at `features/<f>/` plus
`CODEOWNERS` at the root. Cross-team rules collapse to one boundary test plus the
`internal/` compiler rule and the in-vertical `public` contact surface.

**The re-home is complete (Phases 2–7).** ops → `features/ops`; sales + the
seller console → `features/sales` (backend hexagon at the feature root,
read-side under `ui/`); auth → `features/auth`; home/profile/contentcms →
`features/*/ui`; inventory/billing → `features/*`. `internal/modules/` and
`internal/pages/` are deleted, `internal/bootstrap` is `internal/shell`, and the
module scaffolder (`cmd/modulegen` + `make module`) is replaced by `make
feature`. Each vertical owns its `OWNERS.md`; every vertical imports only its
own subtree, `internal/shared`, and `gogo/contracts/*` — clamped by
`internal/architecture/vertical_boundary_test.go`
(`TestFeatureVerticalIsAClosedModule`,
`TestFeatureVerticalHexagonDependencyDirection`,
`TestCompositionLayerReachesNoVerticalInternals`) — and
`TestRetiredRootsStayRetired` fails the build if either retired root comes back.
The full program, its verification log and the execution findings live in
[`docs/DECISION_TEAM_OWNED_VERTICALS.md`](docs/DECISION_TEAM_OWNED_VERTICALS.md)
§4–§5. Every step was kept green: `go build ./...`, `go vet ./...`,
`go test ./... -race`, the architecture tests, and `make arch-graph`.

**Execution finding — the DI container keyed types by short name.** Merging
`auth` (Phase 4) made two verticals' `ui.Page` types coexist, and
`foundation/di` — which built its type keys from `reflect.Type.String()`, i.e.
the *package name*, not the import path — resolved the second registration over
the first: wrong wiring with no compile error and no DI error.
`typeKey`/`qualifiedName` in `foundation/di/provide.go` now key by full import
path, with a regression fixture (`foundation/di/typetest/{one,two}`) and test
(`TestTypeKeysDistinguishSameNamedTypesFromDifferentPackages`). Recorded here
because any future vertical adding a same-named type would have hit it too.

**Revisit when:** a second authority for one row, an independent release cadence,
or a hard resource-isolation requirement makes a vertical worth splitting into its
own service/process. The consolidation does not create that split; it makes the
eventual one a config change.

### D-024 · Server-rendered UI by default; React only as owned islands
**Date:** 2026-10-19 · **Status:** accepted

**Problem.** The React-vs-server-render question is where UI ownership and
operational weight go to die. The industry default after 2020 is a client-side
app per feature; adopted here, it would (a) re-split the vertical — the team
would no longer own its UI as one artifact shipped with its backend — and (b)
make a Node toolchain and a real JavaScript bundle mandatory even where the UI
is a navigation, a list and a form. Every client-side runtime is also something
to ship, hydrate and observe, in a codebase whose server binary is already the
unit of deployment ([D-001](#d-001), [D-023](#d-023)).

**Decision.** The gateway renders HTML in Go, with Templ views and htmx
fragments; the rendering contract (`render.Respond`: a document for a
navigation, a fragment for an in-page swap) chooses the representation per
request. **React is used only as an island**: a self-contained interactive
region whose behaviour is genuinely complex — a rich editor, a drag-and-drop
surface, a live map or graph — where hand-written DOM code would be the
expensive part. The policy in full:

- **Owned by the vertical that renders it.** Island code lives under
  `<feature>/ui/`, obeys the same closed-module rule as the rest of the
  vertical ([rule 1](#2-import-rules-enforced)), and ships in the same image as
  its backend: one team, one project, one image — the [D-023](#d-023) property,
  not a separate frontend deployment.
- **Server-rendered first, island second.** The page renders and works without
  JavaScript wherever the island's job allows it; the island is
  progressive-enhancement, lazy-loaded, and never requires a client-side
  router. No global SPA shell: navigation stays server-side, and static content
  is not hydrated.
- **A budget, reviewed like a dependency.** An island's bundle size is part of
  the review that adds it and is measured: an island that grows until it is a
  de-facto SPA is the failure mode, not the goal.
- **No shared frontend build.** There is no cross-vertical JS pipeline that
  would couple teams' release cadence; two verticals with islands are two
  island bundles, each owned with its vertical.
- **Extract with its consumer.** Today zero React islands exist in the gateway,
  and none is added speculatively ([D-007](#d-007)'s rule applied to UI). The
  first island arrives with its first real use case. The "islands" named by the
  rendering contract (the revalidated storefront grid, the product-detail
  region) are server-side cached regions — an island does not imply React.
- **Docs and demos are build-time only.** A React-based site (e.g. Rspress) may
  build documentation out of this repository; it is not part of the runtime
  image and no application code imports it.

**Why not "no JavaScript at all".** htmx covers the interaction win cheaply, but
a rich editor or canvas re-implemented by hand is a worse maintenance bargain
than a scoped React island. Banning React outright would push those cases into
worse custom code; leaving the door open unbudgeted is how SPAs happen. The
policy makes the exception expensive to declare and cheap to keep.

**Why not "SPA per vertical".** For the team topology gogo decided (one team,
one vertically-owned route, server-rendered HTML), a SPA per vertical buys
client-side routing the server already does, duplicates state the server
already holds, and adds a second deployment artifact per vertical — exactly the
scattering [D-023](#d-023) removed.

**Revisit when:** a second client runtime (mobile/desktop native) needs to share
the UI, or an island's interaction is complex enough that React is the smaller
total cost across several screens — the same "extract with the real consumer"
rule applies.

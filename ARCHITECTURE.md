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

Four kinds of units live under `gogo/internal`:

| Unit | Path | Role |
| ---- | ---- | ---- |
| Modules | `internal/modules/<module>/**` | Self-contained features, hexagonal per module (`domain`, `application`, `infrastructure`, `presentation`). They own their routes and their remote contracts (e.g. `inventory.v1` gRPC). |
| Pages | `internal/pages/<page>/**` | Gateway-side read-side consumers. They render their own pages from their own read models and consume modules **only over their remote contracts** (gRPC clients in `<page>/infrastructure/`), never over in-process imports. |
| Shared | `internal/shared/**` | Cross-cutting infrastructure: the event envelope (`events`), the kernel contract layer (`kernel` + `foundation`), module lifecycle (`module`), web helpers (`web`), the remote-client kits (`grpcconsumer`, `productconsumer`). |
| Composition root | `internal/bootstrap` | The only place that mounts modules **and** pages. It owns the DI container, the wiring of the shared event bus, and the gRPC connections to the product platform. |

Remote contracts that must never drift live in their own Go modules under
`contracts/` — not in `internal/`. Two families live there, and the distinction
matters:

| Family | Contracts | Who serves them |
| ------ | --------- | --------------- |
| In-process modules | `inventory/v1`, `sales/v1`, `auth/v1` | gogo itself. The module mounts in this process; the contract is the seam that keeps pages from importing it. |
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
| 1 | Runtime code under `internal/pages/**` may import a module **only through that module's `public` package** — the surface the owning team declares and maintains ([D-021](#d-021), [`ADR_CROSS_TEAM_UI_CONTRACT.md`](docs/ADR_CROSS_TEAM_UI_CONTRACT.md) §4.1) — and **never** any other runtime code under `internal/modules/**`. Everything else is reached through remote contracts. A page never imports another page. |
| 2 | Runtime code under `internal/modules/**` **never** imports `internal/pages/**`. Modules do not know the gateway's read side exists. |
| 3 | Runtime code under `internal/modules/**` **never** imports another module. Modules communicate through events on the bus, and the shared event vocabulary — type constants and payload structs — lives in the versioned `contracts/<name>/v1` module that both sides import. Today no module imports another. |
| 4 | Only `internal/bootstrap` imports both `internal/pages/**` and `internal/modules/**`. It is the composition root. |
| 5 | The platform-owned `design-system/web/ui` Go module **never** imports application or domain packages. Its standalone module can depend only on the standard library and Templ; web pages, modules and shell compose it through `gogo.design/system/web/ui`. Cross-platform tokens and governance live at repository-root `design-system/`. |
| 6 | The product platform's contracts (`contracts/product/v1`, `productlisting/v1`, `productsearch/v1`, `category/v1`) are imported by exactly one package: `internal/shared/productconsumer`. `internal/bootstrap` is the sole exception — it dials the four connections and hands them to the kit. No page, module or other shared package may import them. |
| 7 | No package under `internal/modules/catalog/`, `internal/modules/product*` or `internal/modules/listing/` may exist. The platform owns the product aggregate ([D-018](#d-018)); nothing here holds product state or writes a product. |

Rules 6 and 7 are what stop the product domain from quietly creeping back in.
They are structural rather than conventional: rule 7 fails the build the moment a
`catalog` or `product` module reappears under `internal/modules/`, whether or not
anyone remembered this paragraph.

Rule 1's `public` exception is the one deliberate cross-team UI contract
([D-021](#d-021)): a module's `public` package is its page-facing surface, owned
and compatibility-managed by the module's team, while the page still decides
what appears on a page and in what order. The exception is granted to pages
only — a module importing another module's `public` is still a rule 3 violation,
because module-to-module communication stays event-based. No module ships a
`public` package today; none is added speculatively, and the first one is
extracted with its first real consumer — the rules that will govern it are
already enforced —
`TestImportBoundaries` (`pages import modules only through a module's public
surface`, `pages must not import other pages`), `TestCompositionLayerReachesNoModuleInternals`,
and the synthetic-graph fixtures in `internal/architecture/ui_contract_test.go`.

The classic hexagon rules short of the above still apply per module:
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
Home and seller render their own read models from gRPC clients in
`<page>/infrastructure/`, which delegate wire/transport to the shared
`productconsumer` / `grpcconsumer` kits and map into page-local read models.
Enforced by import rules [1](#2-import-rules-enforced) and
[6](#2-import-rules-enforced), incl. keeping page-specific read-model mapping in
each page rather than merging the clients.

### D-006 · Name-keyed DI container as composition root
**Date:** 2026-09 · **Status:** accepted
`internal/bootstrap/di.go` wires config, buses (shared + kernel view), logger
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
Modules and pages never wire each other. `internal/bootstrap` owns the full
object graph and its lifecycle; modules volunteer their manifests and health
and are drained in reverse order on shutdown.

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
(`internal/pages/seller`) that consumes remote contracts over the same
bufconn/network transport a split deployment uses, and add an **inventory
module** (`internal/modules/inventory`) that owns the sellable stock quantity and
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

**Decision:** The sales module (`internal/modules/sales`) owns the completed transaction and its lines. It **composes `inventory.v1.AdjustStock`** for the stock effect: on checkout it calls `AdjustStock(-qty, "sale", reference)` per line (atomic, idempotent on the transaction number); on store failure it compensates the lines already applied; on void it calls `AdjustStock(+qty, "void", reference)` and then marks the transaction voided. The inventory module remains the sole stock owner — sales never writes `products.stock` directly.

**Contract surface:** `sales.v1` gRPC contract (`Checkout`, `Get`, `List`, `Void`). The seller console (`internal/pages/seller`) consumes it as a remote peer over bufconn, alongside `inventory.v1` and the product platform.

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

**All four addresses are required configuration.** The gateway refuses to boot
without `PRODUCT_ADDRESS`, `PRODUCT_LISTING_ADDRESS`, `PRODUCT_SEARCH_ADDRESS`
and `CATEGORY_ADDRESS`. The alternative — dialing lazily and degrading — was
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
as in-process modules. They are gogo's own aggregates and no platform service
duplicates them. Billing subscribes to `product.changed.*` for the activity
feed; the storefront grid is TTL-bounded (30 s) and deliberately does not
consume the event stream (`internal/pages/home/page.go`).

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
fails the build the moment a `catalog`/`product`/`listing` module reappears
under `internal/modules/`, so the domain cannot creep back in by accident.

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
speculatively. The change is enforcement-first: the page rule in
`TestImportBoundaries` was narrowed, not deleted, its failure message names the
ADR, `TestCompositionLayerReachesNoModuleInternals` extends the forbidden set to
`compose`, and because a negative rule that has never seen a positive case
cannot be distinguished from a broken parser, both predicates and the `public`
grant are pinned on synthetic graphs in
`internal/architecture/ui_contract_test.go`.

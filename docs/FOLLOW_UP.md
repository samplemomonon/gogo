# Follow-Up Backlog

Work that is deliberately **not** done yet: unfinished scaffolds, unadopted
foundation packages, upstream gaps this repo cannot close, and consistency
cleanups deferred out of the current refactor. Each item names an owner surface
and an exit condition so it can be picked up without re-deriving context.

Items that a refactor has since resolved are kept as **Resolved** entries with
the reason, rather than deleted: "we deliberately did not do this, and here is
why" is the useful part of a backlog, and deleting the entry loses it.

Companion docs: [`ARCHITECTURE.md`](../ARCHITECTURE.md) (governance + decision
log), [`MIGRATION_GUIDE.md`](../MIGRATION_GUIDE.md) (foundation adoption — see
item 6), [`FOUNDATION_SUMMARY.md`](../FOUNDATION_SUMMARY.md).

---

## 1. ~~`media` module — scaffold, not mounted~~ — **Resolved**

*Closed by [D-018](ARCHITECTURE.md#d-018).* The module and its scaffold were
deleted — it duplicated the external `media-service` (the same
two-authorities-for-one-row problem D-018 cured for product), was never
mounted, and no contract or consumer depended on it. The path can never return —
not under `internal/modules` (retired) nor as an `internal/features/media*`
successor: the architecture test refuses the aggregate name outright
(`TestNoPlatformAggregateIsOwnedInProcess`, extended from product to media,
category and search).

## 2. ~~`reporting` module — scaffold, not mounted~~ — **Resolved**

*Closed by [D-018](ARCHITECTURE.md#d-018).* The module and `reporting.v1` were
deleted. It derived `dashboard_stats`, `low_stock_items` and `top_products`
across three contexts in its own migration, written and read by nobody — a read
model no single stream team can own. Whatever team needs those numbers rebuilds
them where the data is.

## 3. Foundation packages awaiting adoption

After [D-017](ARCHITECTURE.md#d-017) retired the superseded generics, three
packages remain unwired:

| Package | Capability | Decision needed |
| --- | --- | --- |
| `foundation/audit` | Structured audit log (compliance for payments / PII) | Logger safety is hardened, but the adapter is still in-memory and unused. Adopt only after the organization approves event scope, actor identity source, transaction boundaries, access, and retention; then write durably on selected money/PII paths. Do not treat inventory movement records as general audit coverage. |
| `foundation/specification` | Predicate composition for business rules | Adopt for storefront/seller filters, or delete (YAGNI). |
| `foundation/validation` | Rule/struct-tag validator | The codebase validates in domain constructors (e.g. `domain.NewEmployee`). Adopt, or delete in favour of that style. |

**Kernel trimming — done.** `internal/shared/kernel/interfaces.go` now declares
only the interfaces with a live consumer: `EventBus`, `Event`, `EventHandler`,
`Subscription`, `Logger` and `Validator` (consumed via `validation`). The
unimplemented remainder — `Repository`, `UnitOfWork`, `Transaction`,
`EventPublisher`, `EventSubscriber`, `CacheProvider`, `Serializer`,
`IDGenerator`, `TimeProvider`, `MetricsCollector`, `Factory`, `Provider`,
`Closer`, `HealthChecker` — is deleted, and the unused `ServiceManager`
lifecycle helper went with the `Service` interface. Re-adding an interface means
naming its consumer in the same change.

`UnitOfWork` in particular has no implementor and no near-term buyer: the
catalog's `ports.Transaction` went with the module (D-018), and sales and
inventory get atomicity from a single conditional `UPDATE` rather than from a
unit-of-work — the better shape for their one-statement mutations.

## 4. ~~Consolidate ID generation~~ — **Resolved**

*Closed by the follow-up pass.* ID generation had drifted into four places;
with the catalog and media modules gone it was three: `shared/idgen`
(base64 crypto/rand), `modules/auth/application.generateID` (hex crypto/rand),
and `google/uuid` directly in inventory and sales.

The convention is now **`google/uuid` everywhere**: `shared/idgen` was deleted
— it existed only for the never-mounted `media` module that item 1 records, and
no committed revision ever imported it (`git grep` over `15cf32e` returns only
the package itself) — and auth's hand-rolled `generateID` was replaced with
`uuid.NewString()` (inventory and sales already used that directly). One
library, no bespoke generator to maintain.

The key-space note this item carried is worth keeping as a recorded decision,
not an accident: product IDs arrive from the platform as `int64` and
`productconsumer` carries them as **decimal strings**
([D-018](ARCHITECTURE.md#d-018)), while inventory and sales generate their own
IDs as **UUIDs**. They are different key spaces, and that is deliberate — a
seller must not mint a product ID locally, because the platform's contract is
`int64`.

## 5. ~~Billing subscription lifecycle~~ — **Resolved**

*Closed by the D-018 refactor.* `module.go` now returns an error from
construction if a subscription fails, stores the `Subscription`, and exposes
`Start`/`Stop` as `module.Lifer` so the gateway drains it in reverse order on
shutdown. A subscription failure is now a boot failure instead of a silently
dead feed; the activity package's tests pin the event→read-model mapping.

The feed side carries the same discipline without being a module: the Kafka
consumer's poll loop is started with a stop function the composition root wires
into the lifecycle — cancel the run context, wait for the loop to drain
(`bootstrap/product_connection.go`, `startProductChangeFeed`) — and an
undecodable record is logged, committed and skipped rather than replayed
forever (`productevents/kafka.go`, `dispatch`).

## 6. ~~`MIGRATION_GUIDE.md` is stale~~ — **Resolved**

*Closed by the follow-up pass.* The guide now describes the tree: the DI excerpt
is copied from `apps/web/internal/shell/di.go` (`ProvideInstance` for
process primitives, `ProvideType` for ports), the subscription step shows
billing's real `kernel.EventBus.SubscribeTypes` pattern, the island step shows
`render.ValidateIslandName`'s owner-namespaced rule, and every path is
`apps/web/...` after the monorepo reorganisation. The one forward-looking
snippet (publishing) is labelled as a shape rather than a copy, because no
module publishes product events any more (D-018).

`ARCHITECTURE.md` still names it the single roadmap; if its snippets ever drift
again, the honest move is to delete the guide and keep
`apps/web/internal/shared/foundation/README.md`, not to let it teach a shape
that does not build.

## 7. ~~Decision log gap: `D-010`~~ — **Resolved**

*Closed by the follow-up pass.* No revision of `ARCHITECTURE.md` ever contained
a `D-010` entry (`git log --all -S "D-010" -- ARCHITECTURE.md` returns
nothing), so there was nothing to restore — the only occurrences of the string
are documents recording the gap. The number is now retired with a tombstone
entry in [`ARCHITECTURE.md`](../ARCHITECTURE.md) so a future reader does not
mistake the skip for a deleted decision, satisfying the prerequisite
`docs/TEAM_TOPOLOGIES_PRD.md` §0.4 records.

## 8. Product data has no tenant concept upstream — **cannot be fixed in this repo**

gogo is multi-tenant: `tenant_id` on every table, RLS as a second enforcement
layer, tenant stamped on every outbound gRPC call
([D-011](ARCHITECTURE.md#d-011); the stamp is `X-Tenant-ID` metadata, sent by
`grpcx`'s outgoing interceptor). What the platform does with the stamp is
per-service, and the split is verified file by file:

- **`product-service`, `product-listing-service` and `product-search-service`
  ignore it.** None of the three has a tenant column in its migrations or a
  tenant field in its contract, so product writes, browse and search are
  global: **one tenant's products are visible to all tenants.**
- **`category-service` honors it.** Merchant categories are tenant-scoped —
  `0002_merchant_categories.sql` adds `tenant_id`, `internal/tenant` reads the
  `x-tenant-id` metadata through an incoming interceptor wired in
  `cmd/server/main.go`, and admin-managed global categories stay `NULL`
  (`category_repository.go`). This is the one merchant-owned surface upstream
  that is actually isolated.

- **Why it is here and not silently absorbed:** the product-data leak is
  invisible at runtime (there is no error to observe) and gogo cannot enforce
  it — filtering happens in a process that receives the tenant and drops it.
- **Exit:** upstream, in the product data services. Add `tenant_id` to their
  tables and read models, carry it on the contract messages, and index the read
  models by it. The gateway already sends the metadata, and the feed decoder
  already tolerates a `tenant_id` on a product event payload
  (`productevents.decodePayload`) — the producer sends none today.
- **Interim:** document it in any deployment that serves more than one tenant.
  A single-tenant deployment (the default `TENANT_DEFAULT`) is unaffected.

## 9. ~~Ownership re-home: team-owned verticals~~ — **Resolved**

*Closed by [D-023](../ARCHITECTURE.md#d-023) Phases 4–7.* The program ran phased
(ops → sales/seller → auth → home/profile/contentcms → inventory/billing) and
stayed green at every step. Every unit now lives under
`apps/web/internal/features/<f>` — backend hexagon at the vertical root,
read-side under `ui/`, `OWNERS.md` + `CODEOWNERS` per team — while
`internal/modules/` and `internal/pages/` are deleted, `internal/bootstrap` is
`internal/shell`, and the retired `cmd/modulegen` / `make module` scaffolder is
replaced by `make feature` (`scripts/feature-vertical.sh`). The boundary is
enforced by the architecture suite (`TestFeatureVerticalIsAClosedModule`,
`TestFeatureVerticalHexagonDependencyDirection`,
`TestCompositionLayerReachesNoVerticalInternals`, `TestRetiredRootsStayRetired`)
and `import_graph.txt` is the regenerated baseline (`make arch-graph`).

One thread stays deliberately open, recorded in the program's own document
rather than here: the `public/` cross-vertical grant
([D-021](../ARCHITECTURE.md#d-021)) is extended into the closed-module rule when
its first genuine consumer appears — see
[`DECISION_TEAM_OWNED_VERTICALS.md`](./DECISION_TEAM_OWNED_VERTICALS.md),
"Remaining".

---

## Reference material (ecommerce consistency patterns)

The concrete implementations in this repo (sales idempotent compensation,
inventory/sales reference-keyed idempotency) follow these documented patterns.
The transactional-outbox reference moved with the product domain: the catalog's
implementation went with it, and the pattern now lives in product-service's write
path (D-018).

- AWS Prescriptive Guidance — *Transactional outbox pattern* (dual-write
  problem; at-least-once relay; consumers must be idempotent):
  <https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html>
- microservices.io — *Saga* (choreography vs orchestration; compensations are
  semantic undos that must be idempotent and commutative):
  <https://microservices.io/patterns/data/saga.html>
- Stripe — *Idempotent requests* (client sends a stable idempotency key on
  every retry; server returns the original result, guarded by a unique
  constraint):
  <https://docs.stripe.com/api/idempotent_requests>
- Debezium — *Outbox Event Router* (the CDC path D-012 adopted; the platform's
  feed is now outbox → Kafka rather than a local WAL tail, but the routing rule
  is the same one):
  <https://debezium.io/documentation/reference/stable/transformations/outbox-event-router.html>

The rule these all share: enforce idempotency **where the data is committed**
(a DB unique constraint / conditional `UPDATE`), not in a process-local store —
which is exactly what the inventory `(tenant_id, reference)` partial unique
index and the sales `AdjustStock` composition already do.

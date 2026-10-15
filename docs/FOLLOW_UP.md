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

## 1. `media` module — scaffold, not mounted

`internal/modules/media` has a domain entity, application ports/use cases, and
Postgres + filesystem adapters, but **no presentation layer and no composition
root wiring**; `RegisterRoutes` is a TODO. The gateway serves no media surface.

- **Exit:** either wire it end to end (a page or module that uploads/lists/
  deletes media, registered in `internal/bootstrap`), or delete the module.
- **Note:** it is the only user of `shared/idgen` and `database/sql` (every
  other adapter uses pgx). If it is wired, align it with `pgxkit` and the
  `idgen` decision in item 4.
- **New consideration after D-018:** product images are the obvious thing to
  hang a media surface on, but the products now live in the platform and
  `product.v1` carries no media list. Wiring `media` here means building a
  media surface with no product to attach media to — which may settle the
  delete option rather than the wire-it option.

## 2. `reporting` module — scaffold, not mounted

`internal/modules/reporting` has a dashboard read model, application service, a
Postgres adapter, `reporting.v1` proto and gRPC service, but **nothing composes
it**; no page consumes `reporting.v1`.

- **Exit:** either build the reporting page that consumes `reporting.v1` over
  gRPC (mirroring how home consumes the product platform's browse contract), or
  delete the module + its contract.
- **Related:** the dashboard metrics (totals, low stock, top products) overlap
  the sales/inventory read models; decide whether reporting composes them or
  reads its own projection. Note that "top products" now needs product names
  from the platform, so reporting composes `productconsumer` for the label and
  keeps the numbers itself — the same split home uses.

## 3. Foundation packages awaiting adoption

After D-017 retired the superseded generics, three packages remain unwired:

| Package | Capability | Decision needed |
| --- | --- | --- |
| `foundation/audit` | Structured audit log (compliance for payments / PII) | **Recommended to adopt** for an ecommerce system: who changed what, when. Wire it on the money/PII paths (checkout, void, employee/PIN changes) or delete it. |
| `foundation/specification` | Predicate composition for business rules | Adopt for storefront/seller filters, or delete (YAGNI). |
| `foundation/validation` | Rule/struct-tag validator | The codebase validates in domain constructors (e.g. `domain.NewEmployee`). Adopt, or delete in favour of that style. |

Also: `kernel` still exports ~15 interfaces (`Repository`, `UnitOfWork`,
`CacheProvider`, `Validator`, `IDGenerator`, …) whose former implementors were
retired. Only `EventBus`, `Event`, `EventHandler`, `Subscription`, `Logger`
(and `Validator`, via `validation`) have consumers. **Exit:** trim
`internal/shared/kernel/interfaces.go` to the interfaces with a live consumer.

D-018 removed one more implementor without touching this list: the catalog's
unit-of-work `ports.Transaction` went with the module, and no surviving module
adopted it. Sales and inventory still get atomicity from a single conditional
`UPDATE` rather than from a unit-of-work, which is the better shape for their
one-statement mutations — so `UnitOfWork` has no implementor and no near-term
buyer.

## 4. Consolidate ID generation

ID generation has drifted into three places:

- `shared/idgen` (base64 crypto/rand) — used only by the unmounted `media`
  module.
- `modules/auth/application.generateID` (hex crypto/rand), used for session IDs.
- `modules/inventory` and `modules/sales` call `google/uuid` directly for
  movement and transaction IDs.

*(The catalog's UUID generator port was the fourth site; it went with the
product domain, D-018.)*

**Exit:** pick one convention and route the rest through it — either
`shared/idgen` behind its `Generator` port, or plain `google/uuid` everywhere
and delete `shared/idgen`. Do this together with item 1 so `media` lands on the
same convention.

Note the collision this creates with product IDs, which is worth deciding
rather than inheriting by accident: product IDs arrive from the platform as
`int64` and `productconsumer` carries them as **decimal strings**
([D-018](ARCHITECTURE.md#d-018)), while inventory and sales generate their own
IDs as **UUIDs**. That is fine — they are different key spaces — but the
decision should be deliberate, because the first thing anyone will want next is
to let a seller mint a product ID locally, which would break the platform's
`int64` contract.

## 5. ~~Billing subscription lifecycle~~ — **Resolved**

*Closed by the D-018 refactor.* `module.go` now returns an error from
construction if a subscription fails, stores the `Subscription`, and exposes
`Start`/`Stop` as `module.Lifer` so the gateway drains it in reverse order on
shutdown. A subscription failure is now a boot failure instead of a silently
dead feed; the activity package's tests pin the event→read-model mapping.

The same pattern is what `productevents` follows: the Kafka consumer runs under
`module.Lifer` semantics and an undecodable record is skipped, not fatal.

## 6. `MIGRATION_GUIDE.md` is stale

It still teaches `di.NewBuilder().AddSingleton("name", …)` with string keys,
`kernel.Logger`, `events.TypedBus` and an `order` module — none of which exist.
It is referenced as "the single roadmap" by `ARCHITECTURE.md`.

- **Exit:** rewrite it to the current foundation (`ProvideType`/`ProvideNamed`/
  `Contribute`, `kernel.EventBus`, the module contract), or fold the useful
  parts into `internal/shared/foundation/README.md` and delete it.
- **Extra work now:** D-018 removed the catalog and listing services from
  `MIGRATION_GUIDE.md`'s examples, so its deployment walkthrough no longer
  matches the repository. That widens the gap — it is no longer only the DI API
  that is out of date.

## 7. Decision log gap: `D-010`

The decision log jumps D-009 → D-011. If the missing entry is recoverable from
git history, restore it; otherwise add a one-line tombstone explaining the gap
so future readers do not assume a lost decision.

## 8. The platform has no tenant concept — **cannot be fixed in this repo**

gogo is multi-tenant: `tenant_id` on every table, RLS as a second enforcement
layer, tenant stamped on every outbound gRPC call ([D-011](ARCHITECTURE.md#d-011)).
The product platform has **no tenant concept at all** — no `tenant_id` in its
migrations, no tenant field in `product.v1`, `productlisting.v1`,
`productsearch.v1` or `category.v1`. So gogo stamps a tenant on every product
call and the platform ignores it: **one tenant's products are visible to all
tenants.**

- **Why it is here and not silently absorbed:** it is a real cross-tenant leak.
  It is invisible at runtime (there is no error to observe) and gogo cannot
  enforce it — filtering happens in a process that never receives the tenant.
- **Exit:** upstream, in the platform. Add `tenant_id` to the product tables,
  put it on the contract messages, and index the read models by it. The gateway
  already sends it, so no gogo change is needed beyond a test that asserts the
  stamp reaches the wire.
- **Interim:** document it in any deployment that serves more than one tenant.
  A single-tenant deployment (the default `TENANT_DEFAULT`) is unaffected.

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
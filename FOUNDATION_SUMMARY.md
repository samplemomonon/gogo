# Foundation Summary

Inventory of the still-live foundation packages. Governance (import boundaries,
decision log) lives in [`ARCHITECTURE.md`](ARCHITECTURE.md); deferred work lives
in [`docs/FOLLOW_UP.md`](docs/FOLLOW_UP.md).

> The generic e-commerce pattern packages originally inventoried here (outbox,
> saga, idempotency, cqrs, uow, domainevent, cache, patterns, modulefactory,
> repository, httpkit, example) were retired — see ARCHITECTURE.md
> [D-017](ARCHITECTURE.md#d-017--retire-the-superseded-generic-foundation-packages).
> The capabilities they named live concretely in the modules: catalog
> transactional outbox (D-009), sales compensation (D-015), inventory/sales
> reference idempotency, listing CQRS read model, catalog unit-of-work port.

## Kernel (`internal/shared/kernel/`)

The consumer contract layer: `EventBus`, `Event`/`EventHandler`,
`Subscription`, `Logger`, and the base event. Producing modules publish the
shared `events.Event`; consumers subscribe through `kernel.EventBus` (D-004).

## Foundation (`internal/shared/foundation/`)

| Package | Contract | Wired by |
| --- | --- | --- |
| `di` | Name-/type-/group-keyed DI container + builder (`ProvideType`, `ProvideNamed`, `Contribute`) with lifecycle | `internal/shell` and every unit's `Register(b)` |
| `eventbus` | `MemoryBus` / `AsyncBus` implementing `kernel.EventBus` | `internal/shell/di.go` |
| `adapter` | `EventBusAdapter` (shared bus → `kernel.EventBus`), `LoggerAdapter` (slog → `kernel.Logger`) | `internal/shell/di.go` |

## Awaiting adoption

| Package | Capability | Status |
| --- | --- | --- |
| `audit` | Structured audit logging (compliance for payments / user data) | Not wired; tracked in `docs/FOLLOW_UP.md` |
| `specification` | Predicate composition for business rules | Not wired; tracked in `docs/FOLLOW_UP.md` |
| `validation` | Rule / struct-tag validation framework | Not wired — modules validate in domain constructors; tracked in `docs/FOLLOW_UP.md` |

## Import rule

Every package here imports only the standard library and `kernel`. A package
that needs a cross-cutting dependency accepts it as a parameter, never imports
it (D-007).

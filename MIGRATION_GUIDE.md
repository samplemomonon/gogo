# Migration Guide: Adopting the Foundation Packages

How to bring a new module or page up to the foundation conventions the codebase
already follows. Every "after" snippet here is copied from working code, so if
one does not compile, that is a bug in this guide rather than in the example.

**What this is not:** a description of the current state. The previous version
of this file was written against an earlier API (`AddSingleton` with string
keys, `patterns.NewCircuitBreaker`, an `order` module that no longer exists) and
was actively misleading. This version describes what is in the tree.

Companion docs: [`ARCHITECTURE.md`](ARCHITECTURE.md) (the rules these steps
serve — read the decision log first), [`docs/FOLLOW_UP.md`](docs/FOLLOW_UP.md)
(what is deliberately *not* adopted), and
[`internal/shared/foundation/README.md`](internal/shared/foundation/README.md)
(inventory of the foundation packages).

## What the foundation actually provides

After [D-017](ARCHITECTURE.md#d-017) retired the superseded generics, this is
the whole list. Adopting more than this is not on the table; the patterns remain
useful as ideas, but each is implemented where its data lives.

| Package | Provides | Wired? |
| --- | --- | --- |
| `foundation/di` | the name- and type-keyed DI container | **yes** — every module and page |
| `foundation/eventbus` | `MemoryBus` / `AsyncBus`, kernel-shaped subscriptions | **yes** — `adapter.EventBusAdapter` wraps the shared bus |
| `foundation/adapter` | `EventBusAdapter` (shared bus → `kernel.EventBus`), `LoggerAdapter` | **yes** |
| `kernel` | `EventBus`, `Event`, `EventHandler`, `Subscription`, `Logger` | **yes** — consumer-side |
| `foundation/audit` | structured audit log | no — see FOLLOW_UP item 3 |
| `foundation/specification` | predicate composition | no — see item 3 |
| `foundation/validation` | rule/struct-tag validator | no — the codebase validates in domain constructors |

Deliberately **not** here: `repository`, `httpkit`, `patterns`, `outbox`,
`saga`, `idempotency`, `cqrs`, `uow`, `domainevent`, `cache`, `modulefactory`.
The codebase has a concrete implementation of each idea where the data lives —
a conditional `UPDATE` for atomic stock, a `(tenant_id, reference)` unique index
for idempotency, `pagekit` for response intent, `grpcconsumer` for resilience —
and a second, unused owner for a concept is worse than no owner at all.

---

## Step 1: Declare the graph, type-keyed

The container is built in the composition root (`internal/bootstrap/di.go`) and
built once. Use `ProvideType` — keying by *type* is what lets a caller say
`ResolveType[ports.Inventory]` without naming a service string, so renaming a
service is a one-line change instead of a search across the tree.

```go
// internal/bootstrap/di.go — the composition root declares the whole graph
b := fdi.NewBuilder()
fdi.ProvideType[config.Config](b, func(*fdi.Container) (config.Config, error) {
    return cfg, nil
})
fdi.ProvideType[sellerports.Inventory](b, func(c *fdi.Container) (sellerports.Inventory, error) {
    return fdi.ResolveAs[*sellerinventoryclient.Client](c, serviceSellerInventoryClient)
})
```

`ProvideType` cannot register two providers of the same type in one builder —
which is the point. Two implementations of `ports.Inventory` in one process is a
design question, not something to resolve by picking whichever registered last.

## Step 2: Resolve by type

```go
inventory, err := fdi.ResolveType[ports.Inventory](c)
```

Nothing names a module or a page. That is what lets `home.Register(b)` and
`seller.Register(b)` each declare only the capabilities they need: the graph
grows without the composition root knowing which surface will use what.

## Step 3: Build a module: infrastructure → ports → use cases → HTTP

In the module's `module.go`:

```go
func Register(b *fdi.Builder) error {
    repo, err := postgres.NewRepository(pool)          // infrastructure
    if err != nil {
        return err
    }
    svc := application.NewService(repo, clock)          // owns the event contract
    handlers.RegisterRoutes(r, svc, composer, logger)   // presentation
    return nil
}
```

The `application.Service` facade wires the use cases, owns the event contract,
and exposes only narrow reader/writer ports to presentation. Each use case
receives exactly the single-method repository port it needs — never the whole
repository.

## Step 4: Publish events from the application layer, not the handler

```go
func (s *Service) Adjust(ctx context.Context, in ports.AdjustInput) (results.Adjustment, error) {
    out, err := s.stock.AdjustStock(ctx, in)
    if err != nil {
        return results.Adjustment{}, err   // no event on a rejected mutation
    }
    if err := s.events.Publish(ctx, events.Event{
        Type:    applicationevents.StockAdjustedType,
        Payload: applicationevents.StockAdjusted{TenantID: in.TenantID, ProductID: in.ProductID},
    }); err != nil {
        return results.Adjustment{}, err
    }
    return out, nil
}
```

Exactly one event per accepted mutation, none on failure — and the invariant is
testable without a transport. Publishing from `presentation` instead would mean
a new transport (a queue consumer, a second endpoint) silently drops the event
contract.

**Tenant on the payload, not in context.** The tenant is a field on every event
payload, never read from the ambient request context by a subscriber. A
projection that infers its partition key from ambient context has a circular
dependency: it needs the tenant to refetch the row, and the refetch is itself
tenant-scoped.

## Step 5: Subscribe with a cancellable, contract-keyed subscription

```go
added := bus.SubscribeTypes(
    func(ctx context.Context, e events.Event) {
        composer.Invalidate(islandkeys.ProductGrid)
    },
    productevents.UpdatedType,
)
```

`SubscribeTypes` only delivers the declared types, so the type assertion inside a
handler is a contract check rather than a blind cast.

**Hold the subscription and cancel it.** If a module keeps subscribing without
keeping the `Subscription`, the feed outlives the module and a subscription
failure is silent. `billing` is the worked example: `NewModule` returns an error
if a subscription fails, and `Stop` (via `module.Lifer`) cancels it — the gateway
drains `Lifer`s in reverse order on shutdown.

## Step 6: Consume remote contracts through the shared kit

Never call gRPC directly from a page or module. `grpcconsumer` owns the four
obligations every outbound call has — stamp the tenant, run under the breaker,
count only a *transport* failure against it, preserve `breaker.ErrOpen` through
wrapping — and the contract kits add their own vocabulary and wire→core mapping.

```go
// Wrong: every call site re-implements the convention, and forgets one of them.
resp, err := catalogClient.GetProduct(ctx, &catalogv1.GetProductRequest{Id: id})

// Right: the kit owns the mechanics; you own your read model.
core, err := c.product.Get(ctx, id, true)
```

`productconsumer` is the **only** package permitted to import a product-platform
contract (import rule 6, enforced by an architecture test). `bootstrap` is the
one exception — it dials the connections.

The outage sentinel is what makes a page's error handling honest:

```go
switch {
case errors.Is(err, breaker.ErrOpen):
    // The dependency is down, and we did not even try. 503 — not an empty page.
case errors.Is(err, ports.ErrNotFound):
    // A real answer. The dependency is working.
}
```

`pagekit.ClassifyError` / `WithErrorStatus` carry that through to the handler so
a browse outage is a 503 while a search outage is a working storefront.

## Step 7: Map remote failures to your own error vocabulary

Each page owns its `httperr` table and its `ports.Err*` aliases. `productconsumer`
publishes one vocabulary (not-found / invalid / conflict / unavailable); aliasing
it keeps the page's contract independent of the kit's, so the kit can change
without touching the page's error handling.

Map **unavailable** to 503 and never to 200-with-empty. An empty grid is a lie
the user cannot distinguish from a real answer.

## Step 8: Name your islands, and let events invalidate them

```go
// internal/modules/<m>/presentation/http/islandkeys/islandkeys.go
func ProductGrid() string { return "m.product-grid" }
```

Each surface owns its own island keys — a module's and a page's must not
collide, because invalidation is by prefix. Invalidation is a **subscriber**,
never a call into the composer from a handler: a handler that reaches into the
composer forgets to invalidate the day it is rewritten.

Invalidate family-wide (`InvalidatePrefix`). Over-invalidating costs a re-render;
under-invalidating serves stale HTML.

## Step 9: Declare response intent once

Response *how* a use case answers is a renderer decision, not a per-handler one.
Put it in `pagekit` (the generic `ListPage`) or in a shared handler helper
(the seller's per-surface files), never inline in every handler:

- full page vs fragment — from `HX-Request`
- validation errors — always 200 with an out-of-band alert, because htmx does
  not swap 4xx/5xx responses
- upstream outage — 503 via `ClassifyError`

---

## Testing after migration

The container is built once in production and per-test in tests. Two patterns
are worth copying.

**A page against a fake peer.** Build the container with the fake, register the
page, resolve the handler and drive it over `httptest`:

```go
func TestHomeGridReadsThroughTheKit(t *testing.T) {
    b := fdi.NewBuilder()
    require.NoError(t, home.Register(b))
    c, err := b.Build()
    require.NoError(t, err)
    h, err := fdi.ResolveType[http.Handler](c)
    require.NoError(t, err)

    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
    require.Equal(t, http.StatusOK, rec.Code)
}
```

**The real thing over a bufconn.** `internal/bootstrap/fakestack_test.go` stands
up the *actual* gRPC servers (the generated stubs, not a hand-written fake) on an
in-memory listener and points the real client kit at them. That covers the
tenant stamp, the breaker classification, the `Core` mapping and cache
invalidation across the event feed — the parts a hand-rolled fake would skip,
and the parts most likely to be wrong.

## Checklist for a new module or page

- [ ] `module.go` (or `page.go`) registers its graph; nothing outside the
      composition root names it.
- [ ] Ports are single-method and defined in `application/ports`, not as one wide
      repository interface.
- [ ] Use cases return `results` DTOs — never HTML, never SQL types.
- [ ] One event per accepted mutation, published from `application.Service`,
      with `TenantID` on the payload.
- [ ] Subscriptions are stored and cancelled in `Stop`.
- [ ] Remote calls go through `grpcconsumer` + a contract kit; no generated stub
      is imported outside it.
- [ ] `breaker.ErrOpen` maps to 503, not to an empty 200.
- [ ] Own `httperr` table with `ports.Err*` aliases.
- [ ] Own `islandkeys`; invalidation is a subscriber, not a handler call.
- [ ] `make check` green, including `go test -race ./...`.

## Common pitfalls

1. **Resolving by service name.** `ResolveAs[*Client](c, "seller-catalog")`
   spreads a rename across every caller. Use `ProvideType`/`ResolveType`.
2. **Registering two providers of one type.** It is a compile-time refusal, not a
   silent override — which is the desired behaviour.
3. **Forgetting to cancel a subscription.** It outlives the module and fails
   silently. Store it; cancel in `Stop`.
4. **Publishing from `presentation`.** A new transport then drops the event.
5. **Treating a business rejection as an outage.** `NotFound` is the dependency
   working. Only transport failures count against a breaker.
6. **Importing a generated contract outside its kit.** The architecture test
   fails; that is the point of the test.
7. **Invalidating a family from the wrong surface.** Island keys are
   prefix-invalidated, so a shared name lets one module evict another's cache.
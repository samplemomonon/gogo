# Modular Architecture Refactoring

This document describes the comprehensive modular enhancements implemented to improve scalability, maintainability, and reduce coupling across the codebase.

> **Status (2026-09-28):** the generic packages this document describes — `cache`,
> `cqrs`, `domainevent`, `example`, `httpkit`, `idempotency`, `modulefactory`,
> `outbox`, `patterns`, `repository`, `saga`, `uow` — were **retired** because no
> runtime code imported them; their capabilities live concretely in the modules.
> See [`ARCHITECTURE.md`](../../../ARCHITECTURE.md) D-017 and
> [`docs/FOLLOW_UP.md`](../../../docs/FOLLOW_UP.md). The live foundation is
> `di`, `eventbus`, `adapter` and `kernel`; `audit`, `specification` and
> `validation` await adoption. The **DI** and **Event Bus** sections below remain
> accurate; the sections for retired packages are kept only as historical
> context.

> Governance: see [`ARCHITECTURE.md`](../../../ARCHITECTURE.md) for the import
> boundaries, the event contract boundary (D-004) and the decision log.

## Overview

The refactoring introduces a layered architecture with clear separation of concerns:

```
internal/shared/
├── kernel/              # Core abstractions and interfaces
├── foundation/          # Reusable implementations
│   ├── adapter/         # Adapters for existing code
│   ├── cache/           # Caching implementations
│   ├── di/              # Dependency injection
│   ├── eventbus/        # Event bus implementations
│   ├── httpkit/         # HTTP handler utilities
│   ├── modulefactory/   # Module factory patterns
│   ├── patterns/        # Common design patterns
│   ├── repository/      # Repository base classes
│   └── validation/      # Validation framework
```

## Key Components

### 1. Kernel Interfaces (`shared/kernel/`)

Defines core abstractions that establish contracts across the application:

- **Service**: Lifecycle management (Start/Stop)
- **EventBus**: Event publishing and subscription
- **Repository**: Data access patterns
- **Logger**: Structured logging
- **CacheProvider**: Caching abstraction
- **Validator**: Model validation

**Benefits:**
- Loose coupling through interfaces
- Easy mocking and testing
- Swappable implementations

### 2. Dependency Injection (`foundation/di/`)

A lightweight DI container supporting:

- Singleton and transient services
- Factory functions
- Automatic dependency resolution
- Scoped containers for request-level dependencies
- Struct field injection via tags

**Usage:**
```go
container := di.NewContainer()

// Register singleton
container.RegisterSingleton("logger", func(c *di.Container) (interface{}, error) {
    return NewLogger(), nil
})

// Resolve dependency
logger, _ := di.ResolveAs[Logger](container, "logger")
```

#### Four ways to key a registration

The container resolves by string name, by Go type, by type-plus-name, or as a
group. Each exists because the previous one could not express a real wiring
need, and each removes a reason for two packages to agree on a string constant.

| Key | API | Use it when |
| --- | --- | --- |
| name | `Provide` / `ResolveAs` | The graph is flat and one instance is enough. |
| type | `ProvideType` / `ResolveType` | A unit publishes a collaborator, or itself, and no caller should have to name it. |
| type + name | `ProvideNamed` / `ResolveNamedAs` | **Several instances of one type** must stay individually addressable — two `*grpc.ClientConn`, two `*breaker.Breaker`. |
| group | `Contribute` / `ResolveGroupAs` | A consumer wants **every** implementation of a type, not a fixed set. |

A type-plus-name key keeps the type checked, so a mistyped name fails at the
call site rather than resolving to an unrelated registration at boot:

```go
di.ProvideNamed(b, "catalog", func(c *di.Container) (*grpc.ClientConn, error) { ... })
di.ProvideNamed(b, "listing", func(c *di.Container) (*grpc.ClientConn, error) { ... })
conn, err := di.ResolveNamedAs[*grpc.ClientConn](c, "catalog")
```

A group is resolved as an ordered `[]Member[T]`, each member carrying the name
the root gave it. A group nobody contributed to resolves to an **empty slice,
not an error**, so a consumer may legitimately have zero of a dependency:

```go
di.Contribute(b, "catalog", func(c *di.Container) (*breaker.Breaker, error) { ... })
upstreams, err := di.ResolveGroupAs[*breaker.Breaker](c)
```

Groups are built once, on first resolution, and `Contribute` must therefore be
called while wiring. Because a consumer depends on the group rather than on a
listed set, **adding an implementation never edits the consumer** — the
contribution alone is enough.

#### Self-registration: the root is a list of units

A unit that owns its own graph declares it and is wired by being listed, rather
than hand-wired by the composition root. This is what keeps
`internal/shell/di.go` a list instead of a second copy of every unit's
dependencies:

```go
// inside the unit's own package
func Register(b *di.Builder) {
    di.ProvideType(b, func(c *di.Container) (*Module, error) {
        bus, err := di.ResolveType[kernel.EventBus](c)
        if err != nil {
            return nil, fmt.Errorf("billing: resolve kernel.EventBus: %w", err)
        }
        return NewModule(bus), nil
    })
}

// in the composition root
home.Register(b)
seller.Register(b)
billing.Register(b)
ops.Register(b)
```

Two rules make this work, and both are load-bearing rather than stylistic:

- A unit declares its inputs **by type or by group**, never by a name string
  owned elsewhere. That is what stops a self-registered unit from depending on
  the composition root's private vocabulary.
- Prefer a **group** over a resolved concrete collaborator. A unit that names
  the specific adapters it needs tends to reach for types another unit owns;
  asking for the group expresses "every upstream" instead, and keeps the
  dependency direction legal (`modules` must not import `pages`, enforced by
  `internal/architecture`).

The ops module is the worked example: its readiness needs the state of every
upstream the gateway renders from, but the clients that own those breakers
belong to pages. Resolving the group gives ops what it needs without either
importing a page or hardcoding a list of upstream names.

### 3. Event Bus (`foundation/eventbus/`)

Enhanced event bus with:

- Synchronous (MemoryBus) and asynchronous (AsyncBus) delivery
- Type-based filtering
- Subscription management
- Worker pool for async processing
- Panic recovery

**Benefits:**
- Decouples modules through events
- Configurable delivery semantics
- Better testability

### 4. Repository Patterns (`foundation/repository/`)

Generic base repositories with:

- In-memory storage with indexes
- Thread-safe operations
- Pagination support
- Query abstractions

**Usage:**
```go
repo := repository.NewInMemoryRepository[string, Product]()
repo.Save(ctx, "123", product)
products, _ := repo.FindAll(ctx)
```

### 5. Cache Implementations (`foundation/cache/`)

Multiple caching strategies:

- **InMemoryCache**: TTL-based with automatic cleanup
- **LRUCache**: Size-limited with least-recently-used eviction

### 6. Design Patterns (`foundation/patterns/`)

Reusable patterns:

- **Retry**: Exponential backoff retry logic
- **CircuitBreaker**: Fault tolerance pattern
- **Pool**: Object pooling for resource reuse
- **Chain**: Sequential execution chain
- **Pipeline**: Data transformation pipeline

### 7. HTTP Handler Kit (`foundation/httpkit/`)

HTTP utilities reducing boilerplate:

- Enhanced handlers with error returns
- Middleware chaining
- Request/Response helpers
- JSON encoding/decoding
- Standard error handling

### 8. Validation Framework (`foundation/validation/`)

Extensible validation:

- Rule-based validation
- Struct tag support
- Built-in rules (required, min/max length, email, patterns)
- Custom rule registration

### 9. Module Factory (`foundation/modulefactory/`)

Factory patterns for module initialization:

- Automatic dependency injection
- Lifecycle management
- Configuration management
- Repository/Service/Handler factories

### 10. Adapters (`foundation/adapter/`)

Bridges between existing code and new interfaces:

- EventBusAdapter: Adapts existing events.Bus to kernel.EventBus
- LoggerAdapter: Adapts slog.Logger to kernel.Logger
- Backwards compatible integration

### 11. CQRS (`foundation/cqrs/`)

Command Query Responsibility Segregation for e-commerce:

- `CommandBus` dispatches write operations, `QueryBus` dispatches reads
- Generic `CommandHandler[C, R]` and `QueryHandler[Q, R]` interfaces
- Optional validation on dispatch
- Logging and metrics middleware

```go
type PlaceOrderCommand struct{}
func (PlaceOrderCommand) CommandName() string { return "order.place" }

type PlaceOrderHandler struct{}
func (h *PlaceOrderHandler) Handle(ctx context.Context, cmd PlaceOrderCommand) (string, error) {
    return "order-123", nil
}

bus := cqrs.NewCommandBus(eventBus, validator)
cqrs.Register(bus, &PlaceOrderHandler{})
orderID, err := bus.Dispatch(ctx, PlaceOrderCommand{})
```

### 12. Transactional Outbox (`foundation/outbox/`)

Reliable event publishing for e-commerce transactions:

- `Outbox` interface for storing/publishing event messages
- `Publisher` background worker polls and publishes to the event bus
- `InMemoryOutbox` for testing and single-instance deployments
- Prevents event loss when transactions commit but publish crashes

### 13. Saga (`foundation/saga/`)

Distributed transaction orchestration for multi-service workflows:

- Fluent `Builder` for defining ordered steps
- Automatic reverse-order compensation on failure
- Lifecycle events published to the bus (started/completed/compensated)
- Ideal for checkout: reserve inventory → process payment → create shipment

```go
s := saga.NewBuilder("checkout", eventBus, logger).
    Step("reserve-inventory", reserveInventory, releaseInventory).
    Step("process-payment", processPayment, refundPayment).
    Build()

if err := s.Execute(ctx); err != nil { /* handle */ }
```

### 14. Idempotency (`foundation/idempotency/`)

Protection against duplicate operations (retries, double-submits):

- Deterministic `GenerateKey` from operation name + payload hash
- Middleware caches results and errors per key
- `InMemoryStore` for testing/single-instance
- Critical for payment processing and webhook handlers

### 15. Specification (`foundation/specification/`)

Reusable business rules with logical composition:

- `Specification[T]` predicate-based rules
- `.And()`, `.Or()`, `.Not()` combinators
- `Composite` with "all"/"any" modes
- Filters products: in-stock, affordable, high-rated, etc.

### 16. Unit of Work (`foundation/uow/`)

Atomic multi-repository transactions:

- `WorkUnit` registers new/dirty/deleted entities
- `TransactionalRepository[T]` decorates any repository
- `Manager` coordinates a single active UoW per request
- Guarantees consistency across order + inventory + payment writes

### 17. Audit (`foundation/audit/`)

Compliance logging for sensitive operations:

- Structured entries (user, action, entity, status, changes)
- Middleware wraps operations, records success/failure
- Before/after change diffing
- Filterable query interface

### 18. Domain Events (`foundation/domainevent/`)

DDD aggregate event modeling:

- `AggregateRoot` with uncommitted event ledger
- `DomainEvent` carries aggregate ID/type/version metadata
- `Publisher` flushes aggregate events to the event bus
- Foundation for event-sourced order/checkout flows

## Migration Strategy

### Phase 1: Foundation (Complete)
✓ Core kernel interfaces
✓ DI container
✓ Event bus implementations
✓ Repository patterns
✓ Cache providers
✓ Design patterns
✓ HTTP utilities
✓ Validation framework
✓ Module factories
✓ Adapters
✓ CQRS
✓ Transactional outbox
✓ Saga
✓ Idempotency
✓ Specification
✓ Unit of work
✓ Audit logging
✓ Domain events

### Phase 2: Integration (In Progress)
✓ Bootstrap composition root uses the foundation DI container
✓ Kernel.EventBus/Logger adapters wired into the gateway bus
✓ Shared-bus/kernel-bus bridge pinned by tests
✓ kernel.EventBus gained contract-keyed SubscribeTypes
✓ Billing module migrated: consumes catalog events via kernel.EventBus
  (contract-keyed SubscribeTypes over the shared in-process bus)
- Replace duplicate catalog clients with shared implementation
- Apply factory patterns to module initialization
- Wire outbox + saga + unit of work into a checkout flow

### Phase 3: Enhancement
- Add metrics collection
- Implement distributed caching
- Add query builder for repositories
- Extend validation rules
- Add circuit breaker to external calls

## Benefits Achieved

1. **Reduced Coupling**: Modules depend on interfaces, not concrete implementations
2. **Improved Testability**: Easy mocking through interfaces
3. **Code Reuse**: Common patterns extracted into reusable components
4. **Scalability**: Clear extension points for new features
5. **Maintainability**: Consistent patterns across codebase
6. **Flexibility**: Swappable implementations (e.g., in-memory vs PostgreSQL)

## Usage Examples

### Creating a Module with DI

Resolve by type, and return errors rather than panicking — a provider that
panics poisons its singleton, because the container's `sync.Once` is already
spent. This is the shape a real unit uses (see `billing.Register`):

```go
func Register(b *di.Builder) {
    di.ProvideType(b, func(c *di.Container) (*CatalogModule, error) {
        logger, err := di.ResolveType[kernel.Logger](c)
        if err != nil {
            return nil, fmt.Errorf("catalog: resolve kernel.Logger: %w", err)
        }
        eventBus, err := di.ResolveType[kernel.EventBus](c)
        if err != nil {
            return nil, fmt.Errorf("catalog: resolve kernel.EventBus: %w", err)
        }

        repo := repository.NewInMemoryRepository[string, Product]()
        return &CatalogModule{
            service: NewCatalogService(repo, eventBus, logger),
            logger:  logger,
        }, nil
    })
}
```

A unit that needs *every* implementation of a type resolves the group instead
of naming them — that is what keeps its dependency direction legal:

```go
upstreams, err := di.ResolveGroupAs[*breaker.Breaker](c)
```

### Using Event Bus

```go
// Publish event
event := kernel.NewBaseEvent("catalog.product.created", product)
eventBus.Publish(ctx, event)

// Subscribe to events
sub, _ := eventBus.SubscribeTypes(ctx, func(ctx context.Context, event kernel.Event) error {
    // Handle event
    return nil
}, "catalog.product.created", "catalog.product.updated")
defer sub.Unsubscribe()
```

### HTTP Handler with Utilities

```go
func HandleCreateProduct(w http.ResponseWriter, r *http.Request) error {
    req := httpkit.NewRequest(r)
    resp := httpkit.NewResponse(w)
    
    var input CreateProductInput
    if err := req.DecodeJSON(&input); err != nil {
        return resp.BadRequest("Invalid request body")
    }
    
    product, err := service.CreateProduct(r.Context(), input)
    if err != nil {
        return resp.InternalError(err)
    }
    
    return resp.Created("/products/"+product.ID, product)
}
```

## Architecture Principles

1. **Dependency Inversion**: Depend on abstractions (interfaces), not concretions
2. **Single Responsibility**: Each component has one reason to change
3. **Open/Closed**: Open for extension, closed for modification
4. **Interface Segregation**: Small, focused interfaces
5. **DRY**: Extract common patterns into reusable components

## Testing Strategy

The new architecture improves testability:

- Mock kernel interfaces for unit tests
- Use in-memory implementations for integration tests
- Inject test doubles through DI container
- Isolated component testing without full system bootstrap

## Performance Considerations

- In-memory cache with configurable cleanup intervals
- LRU cache for bounded memory usage
- Async event bus with worker pools
- Connection pooling in repository factories
- Lazy initialization through factories

## Next Steps

1. Update bootstrap package to use DI container
2. Refactor existing modules to use new patterns
3. Extract duplicate code into shared implementations
4. Add comprehensive tests for foundation packages
5. Update documentation with migration guides
6. Add metrics and observability hooks

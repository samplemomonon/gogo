// Package kernel provides core abstractions and interfaces that form the
// foundation of the modular architecture. These interfaces establish contracts
// for cross-cutting concerns, enabling loose coupling and high cohesion across
// the application.
package kernel

import (
	"context"
	"io"
)

// Service represents a lifecycle-aware component that can be started and stopped.
// Modules, background workers, and infrastructure components should implement
// this interface to participate in graceful startup and shutdown orchestration.
type Service interface {
	// Start initializes the service. Returns an error if initialization fails.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the service. Implementations should respect
	// the context deadline for graceful termination.
	Stop(ctx context.Context) error
}

// HealthChecker reports the health status of a component. Used for readiness
// and liveness probes in production deployments.
type HealthChecker interface {
	// HealthCheck returns nil if the component is healthy, or an error
	// describing the health problem.
	HealthCheck(ctx context.Context) error
}

// Repository is the base interface for all data access patterns. It establishes
// the contract for persistence boundaries without prescribing implementation details.
type Repository interface {
	// Close releases any resources held by the repository.
	Close() error
}

// UnitOfWork coordinates transactional operations across multiple repositories.
// Implementations should ensure ACID properties for the enclosed operations.
type UnitOfWork interface {
	// Begin starts a new unit of work transaction.
	Begin(ctx context.Context) (Transaction, error)
}

// Transaction represents an active database transaction that can be committed
// or rolled back.
type Transaction interface {
	// Commit persists all changes made within the transaction.
	Commit(ctx context.Context) error

	// Rollback discards all changes made within the transaction.
	Rollback(ctx context.Context) error

	// Context returns a context that carries the transaction state.
	Context() context.Context
}

// EventPublisher publishes domain events to interested subscribers.
// This interface decouples event producers from the event bus implementation.
type EventPublisher interface {
	// Publish sends an event to all registered subscribers.
	Publish(ctx context.Context, event Event)
}

// EventSubscriber receives published events.
type EventSubscriber interface {
	// Subscribe registers a handler for events. Returns a subscription
	// that can be cancelled.
	Subscribe(ctx context.Context, handler EventHandler) (Subscription, error)
}

// EventBus combines publishing and subscribing capabilities. Implementations
// may also support contract-keyed subscriptions via EventBus.SubscribeTypes
// (the adapter over the shared in-process bus and both foundation buses do),
// letting consumers receive only the event types they declare.
type EventBus interface {
	EventPublisher
	EventSubscriber

	// SubscribeTypes registers a handler for exactly the given event types.
	// An empty types list is equivalent to Subscribe. Returns a subscription
	// that can be cancelled.
	SubscribeTypes(ctx context.Context, handler EventHandler, types ...string) (Subscription, error)
}

// Event represents a domain event with a type identifier and payload.
type Event interface {
	// Type returns the event type identifier (e.g., "catalog.product.created").
	Type() string

	// Payload returns the event data.
	Payload() interface{}

	// Timestamp returns when the event occurred.
	Timestamp() int64
}

// EventHandler processes published events.
type EventHandler func(ctx context.Context, event Event) error

// Subscription represents an active event subscription that can be cancelled.
type Subscription interface {
	// Unsubscribe stops receiving events for this subscription.
	Unsubscribe() error
}

// Logger provides structured logging capabilities.
type Logger interface {
	// Debug logs a debug message with optional fields.
	Debug(msg string, fields ...interface{})

	// Info logs an informational message with optional fields.
	Info(msg string, fields ...interface{})

	// Warn logs a warning message with optional fields.
	Warn(msg string, fields ...interface{})

	// Error logs an error message with optional fields.
	Error(msg string, fields ...interface{})

	// With returns a new logger with the given fields attached.
	With(fields ...interface{}) Logger
}

// MetricsCollector records application metrics.
type MetricsCollector interface {
	// IncrCounter increments a counter metric.
	IncrCounter(name string, value int64, tags ...string)

	// RecordHistogram records a histogram value.
	RecordHistogram(name string, value float64, tags ...string)

	// SetGauge sets a gauge metric to the specified value.
	SetGauge(name string, value float64, tags ...string)
}

// CacheProvider abstracts cache storage implementations.
type CacheProvider interface {
	// Get retrieves a value from the cache.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores a value in the cache with the specified TTL.
	Set(ctx context.Context, key string, value []byte, ttl int64) error

	// Delete removes a value from the cache.
	Delete(ctx context.Context, key string) error

	// Clear removes all entries from the cache.
	Clear(ctx context.Context) error
}

// Serializer handles object serialization and deserialization.
type Serializer interface {
	// Serialize converts an object to bytes.
	Serialize(v interface{}) ([]byte, error)

	// Deserialize converts bytes back to an object.
	Deserialize(data []byte, v interface{}) error
}

// Validator validates domain objects and requests.
type Validator interface {
	// Validate checks if the given value is valid according to business rules.
	Validate(ctx context.Context, v interface{}) error
}

// IDGenerator generates unique identifiers.
type IDGenerator interface {
	// Generate creates a new unique identifier.
	Generate() string
}

// TimeProvider abstracts time operations for testability.
type TimeProvider interface {
	// Now returns the current time.
	Now() int64

	// Since returns the duration since the given time.
	Since(timestamp int64) int64
}

// Closer represents resources that need cleanup.
type Closer interface {
	io.Closer
}

// Factory creates instances of a specific type.
type Factory[T any] interface {
	// Create builds a new instance with the given configuration.
	Create(config interface{}) (T, error)
}

// Provider retrieves instances, potentially with caching or pooling.
type Provider[T any] interface {
	// Get retrieves an instance.
	Get(ctx context.Context) (T, error)

	// Release returns an instance to the pool if applicable.
	Release(instance T) error
}

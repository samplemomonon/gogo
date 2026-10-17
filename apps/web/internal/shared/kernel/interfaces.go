// Package kernel provides the consumer-facing contracts of the foundation
// layer: the event contract consumers subscribe through, and the logging
// interface adapters project onto.
//
// The package is deliberately small. It once declared ~20 interfaces
// (Repository, UnitOfWork, CacheProvider, IDGenerator, …) whose implementors
// were retired; docs/FOLLOW_UP.md item 3 requires the file to keep only the
// interfaces with a live consumer, because an interface with no implementor
// makes a reader answer "where does this behavior live?" wrongly. The
// survivors are EventBus, Event, EventHandler, Subscription, Logger and
// Validator — each with at least one current implementor or consumer.
// Re-adding an interface means naming its consumer in the same change.
package kernel

import "context"

// EventBus is the consumer contract over the shared in-process bus. Producers
// stay on the shared events.Event envelope (D-004); a consumer that wants
// foundation guarantees subscribes through this contract instead.
type EventBus interface {
	// Publish sends an event to all registered subscribers.
	Publish(ctx context.Context, event Event)

	// Subscribe registers a handler for every event. Returns a subscription
	// that can be cancelled.
	Subscribe(ctx context.Context, handler EventHandler) (Subscription, error)

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

// Validator validates domain objects and requests. Consumed by
// foundation/validation; kept until that package's adoption-or-removal
// decision lands (docs/FOLLOW_UP.md item 3).
type Validator interface {
	// Validate checks if the given value is valid according to business rules.
	Validate(ctx context.Context, v interface{}) error
}

// Package events provides a minimal in-process event bus. It is the
// "events" corner of the module contract: modules publish domain events and
// subscribe to events without importing each other — the server-side
// counterpart to the custom DOM events htmx dispatches on the client.
//
// A plain subscription receives every event (subscribers filter by event
// type/payload, e.g. a type switch). Contract subscriptions, via
// events.Subscribe or Bus.SubscribeTypes, receive only the event TYPES the
// subscriber declares — the same contract as a custom DOM event name, so
// subscribers never type-switch on events they do not care about:
//
//	bus := events.NewMemoryBus()
//	events.Subscribe(bus, func(ctx context.Context, event events.Event) {
//	    // only runs for catalog.product.updated
//	}, catalog.ProductUpdatedType)
//
//	sub := bus.SubscribeTypes(func(ctx context.Context, event events.Event) {}, "a", "b")
//	...
//	sub.Cancel() // no more events for this subscriber
//
// A subscription may be cancelled; async delivery (PublishAsync) is
// available to publishers that want to release the caller without waiting.
package events

import (
	"context"
	"sync"
)

// Event is one occurrence of a domain event. Type is the stable contract
// name (e.g. "catalog.product.created"); Payload carries the module's own
// event struct. Across processes, events travel over the catalog.v1 gRPC
// contract's WatchProducts stream; the in-process payload struct is the
// local, typed form (wire.go keeps a transport-agnostic JSON envelope for
// a future broker-backed bus).
type Event struct {
	Type    string
	Payload any
}

// Handler receives published events.
type Handler func(ctx context.Context, event Event)

// Bus decouples publishers from subscribers. The in-process MemoryBus is
// synchronous: effects (like island invalidation) are visible before the
// publishing request returns.
type Bus interface {
	Publish(ctx context.Context, event Event)
	Subscribe(handler Handler)
}

// TypedBus is a Bus whose subscribers may declare interest in specific
// event types only. MemoryBus implements it.
type TypedBus interface {
	Bus
	SubscribeTypes(handler Handler, types ...string) *Subscription
}

// Subscription is a handle to a registered handler. Cancel removes the
// handler so it stops receiving events; it is safe to call more than once
// and from any goroutine.
type Subscription struct {
	bus *MemoryBus
	id  int
}

// Cancel unregisters the subscription. Subscriptions from a Bus that does
// not Support typed delivery are no-ops.
func (s *Subscription) Cancel() {
	if s.bus != nil {
		s.bus.cancel(s.id)
	}
}

// subscribe is the subscription bookkeeping entry of the bus.
type subscription struct {
	id       int
	handler  Handler
	types    map[string]struct{} // nil = receive every event
	canceled bool
}

// MemoryBus is the synchronous, concurrency-safe in-process implementation.
type MemoryBus struct {
	mu     sync.RWMutex
	nextID int
	subs   []subscription

	// onPanic, when set, observes every subscriber panic the bus contains.
	// It is called outside the dispatch so a reporter may do I/O without
	// stalling publishers or other subscribers.
	onPanic func(event Event, recovered any)
}

// NewMemoryBus returns an empty bus.
func NewMemoryBus() *MemoryBus {
	return &MemoryBus{}
}

// WithPanicReporter installs a callback invoked whenever a subscriber panics.
// Without one the bus contains the panic silently (the historical default,
// which keeps a broken subscriber from crashing a request); with one, the
// failure becomes observable — wire this to a logger in the composition root
// so a panicking subscriber is never invisible.
func (b *MemoryBus) WithPanicReporter(report func(event Event, recovered any)) *MemoryBus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onPanic = report
	return b
}

// Subscribe registers a handler for every event. Subscribing after events
// have started flowing is safe; existing subscribers keep their order.
func (b *MemoryBus) Subscribe(handler Handler) {
	b.subscribe(handler)
}

// SubscribeTypes registers a handler for exactly the given event types. An
// empty types list is equivalent to Subscribe. The returned subscription
// lets the caller Cancel delivery later.
func (b *MemoryBus) SubscribeTypes(handler Handler, types ...string) *Subscription {
	return b.subscribe(handler, types...)
}

// subscribe appends a handler. types nil means "all events".
func (b *MemoryBus) subscribe(handler Handler, types ...string) *Subscription {
	var wanted map[string]struct{}
	if len(types) > 0 {
		// Index the declared types so dispatch is O(1) per subscriber rather
		// than a linear scan of the type list under every publish.
		wanted = make(map[string]struct{}, len(types))
		for _, typ := range types {
			wanted[typ] = struct{}{}
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	b.subs = append(b.subs, subscription{
		id:      id,
		handler: handler,
		types:   wanted,
	})
	return &Subscription{bus: b, id: id}
}

// cancel marks the subscription with the given id as removed. Cancelling an
// already-cancelled or unknown id is a no-op.
func (b *MemoryBus) cancel(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i := range b.subs {
		if b.subs[i].id == id {
			if !b.subs[i].canceled {
				b.subs[i].canceled = true
				b.compactLocked()
			}
			return
		}
	}
}

// compactLocked drops cancelled subscriptions so a long-lived bus (the
// gateway's module bus) never accumulates stale handlers. Callers hold the
// write lock.
func (b *MemoryBus) compactLocked() {
	live := b.subs[:0]
	for _, sub := range b.subs {
		if !sub.canceled {
			live = append(live, sub)
		}
	}
	b.subs = live
}

// Len reports the number of live (non-cancelled) subscribers. It is useful
// for observability: the gateway can surface how many modules are listening
// on the shared bus without allocating.
func (b *MemoryBus) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	count := 0
	for _, sub := range b.subs {
		if !sub.canceled {
			count++
		}
	}
	return count
}

// Publish runs every matching handler inline. Subscribers are copied under
// the lock so a handler may subscribe, cancel or publish during dispatch
// without deadlocks. A panicking subscriber is contained so it can never
// crash a request; if a panic reporter is installed (WithPanicReporter) the
// contained panic is handed to it so the failure is not silent.
func (b *MemoryBus) Publish(ctx context.Context, event Event) {
	b.mu.RLock()
	subs := make([]subscription, len(b.subs))
	copy(subs, b.subs)
	reporter := b.onPanic
	b.mu.RUnlock()

	for _, sub := range subs {
		if sub.canceled || !sub.matches(event.Type) {
			continue
		}
		handler := sub.handler
		b.dispatch(ctx, handler, event, reporter)
	}
}

// dispatch runs one handler, containing any panic. A nil reporter keeps the
// historical silent behavior; otherwise the recovered value is reported.
func (b *MemoryBus) dispatch(ctx context.Context, handler Handler, event Event, reporter func(Event, any)) {
	defer func() {
		recovered := recover()
		if recovered != nil && reporter != nil {
			reporter(event, recovered)
		}
	}()
	handler(ctx, event)
}

// PublishAsync delivers the event in a background goroutine, so the caller
// is not blocked by subscribers. Delivery semantics inside the goroutine
// are identical to Publish (synchronous, ordered per subscriber, panics
// contained). Use it only where the caller must not wait for effects —
// e.g. audit logging or telemetry — never for cache invalidation, which
// must complete before the request resumes.
func (b *MemoryBus) PublishAsync(ctx context.Context, event Event) {
	b.mu.RLock()
	reporter := b.onPanic
	b.mu.RUnlock()

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil && reporter != nil {
				reporter(event, recovered)
			}
		}()
		b.Publish(ctx, event)
	}()
}

// matches reports whether a subscription receives the event type: no types
// declared (nil) means every event.
func (s subscription) matches(eventType string) bool {
	if s.types == nil {
		return true
	}
	_, ok := s.types[eventType]
	return ok
}

// Subscribe registers handler on bus for exactly the given event types,
// falling back to all-event delivery when the bus does not support typed
// subscriptions. It is the ergonomic entry point for contract-keyed
// subscribers: modules declare the event types they care about and never
// receive anything else.
func Subscribe(b Bus, handler Handler, types ...string) *Subscription {
	if typed, ok := b.(TypedBus); ok {
		return typed.SubscribeTypes(handler, types...)
	}
	b.Subscribe(handler)
	return &Subscription{}
}

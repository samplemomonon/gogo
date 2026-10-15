// Package eventbus provides an improved event bus implementation with
// interface-based design for better extensibility and testability.
package eventbus

import (
	"context"
	"fmt"
	"sync"

	"gogo/internal/shared/kernel"
)

// Config configures the event bus behavior.
type Config struct {
	// MaxSubscribers limits the number of concurrent subscribers.
	MaxSubscribers int
	// BufferSize sets the channel buffer size for async delivery.
	BufferSize int
	// PanicHandler is called when a subscriber panics.
	PanicHandler func(event kernel.Event, recovered any)
}

// DefaultConfig returns sensible defaults for the event bus.
func DefaultConfig() *Config {
	return &Config{
		MaxSubscribers: 1000,
		BufferSize:     100,
		PanicHandler:   nil,
	}
}

// subscriber holds subscription information.
type subscriber struct {
	id         string
	handler    kernel.EventHandler
	types      map[string]bool
	matchAll   bool
	cancelFunc context.CancelFunc
}

// MemoryBus implements kernel.EventBus with in-memory synchronous delivery.
type MemoryBus struct {
	mu          sync.RWMutex
	subscribers map[string]*subscriber
	nextID      int
	config      *Config
}

// NewMemoryBus creates a new in-memory event bus.
func NewMemoryBus(config *Config) *MemoryBus {
	if config == nil {
		config = DefaultConfig()
	}
	return &MemoryBus{
		subscribers: make(map[string]*subscriber),
		config:      config,
	}
}

// Publish sends an event to all matching subscribers synchronously.
func (b *MemoryBus) Publish(ctx context.Context, event kernel.Event) {
	b.mu.RLock()
	subs := make([]*subscriber, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		if sub.matchAll || sub.types[event.Type()] {
			subs = append(subs, sub)
		}
	}
	b.mu.RUnlock()

	for _, sub := range subs {
		b.deliverToSubscriber(ctx, sub, event)
	}
}

// deliverToSubscriber safely delivers an event to a subscriber with panic recovery.
func (b *MemoryBus) deliverToSubscriber(ctx context.Context, sub *subscriber, event kernel.Event) {
	defer func() {
		if r := recover(); r != nil {
			if b.config.PanicHandler != nil {
				b.config.PanicHandler(event, r)
			}
		}
	}()

	_ = sub.handler(ctx, event)
}

// Subscribe registers a handler for all events.
func (b *MemoryBus) Subscribe(ctx context.Context, handler kernel.EventHandler) (kernel.Subscription, error) {
	return b.subscribeWithTypes(ctx, handler, nil)
}

// SubscribeTypes registers a handler for specific event types.
func (b *MemoryBus) SubscribeTypes(ctx context.Context, handler kernel.EventHandler, types ...string) (kernel.Subscription, error) {
	typeMap := make(map[string]bool)
	for _, t := range types {
		typeMap[t] = true
	}
	return b.subscribeWithTypes(ctx, handler, typeMap)
}

// subscribeWithTypes is the internal subscription implementation.
func (b *MemoryBus) subscribeWithTypes(ctx context.Context, handler kernel.EventHandler, types map[string]bool) (kernel.Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.subscribers) >= b.config.MaxSubscribers {
		return nil, fmt.Errorf("max subscribers reached: %d", b.config.MaxSubscribers)
	}

	id := fmt.Sprintf("sub-%d", b.nextID)
	b.nextID++

	ctx, cancel := context.WithCancel(ctx)
	sub := &subscriber{
		id:         id,
		handler:    handler,
		types:      types,
		matchAll:   types == nil || len(types) == 0,
		cancelFunc: cancel,
	}

	b.subscribers[id] = sub

	return &subscription{
		bus: b,
		id:  id,
	}, nil
}

// unsubscribe removes a subscriber by ID.
func (b *MemoryBus) unsubscribe(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	sub, exists := b.subscribers[id]
	if !exists {
		return nil
	}

	if sub.cancelFunc != nil {
		sub.cancelFunc()
	}
	delete(b.subscribers, id)
	return nil
}

// subscription implements kernel.Subscription.
type subscription struct {
	bus *MemoryBus
	id  string
}

// Unsubscribe removes this subscription from the bus.
func (s *subscription) Unsubscribe() error {
	return s.bus.unsubscribe(s.id)
}

// Ensure MemoryBus implements kernel.EventBus.
var _ kernel.EventBus = (*MemoryBus)(nil)

// AsyncBus delivers events asynchronously via goroutines and channels.
type AsyncBus struct {
	*MemoryBus
	workers int
	queue   chan asyncEvent
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

type asyncEvent struct {
	ctx   context.Context
	event kernel.Event
}

// NewAsyncBus creates an asynchronous event bus with worker pool.
func NewAsyncBus(config *Config, workers int) *AsyncBus {
	if config == nil {
		config = DefaultConfig()
	}
	if workers <= 0 {
		workers = 4
	}

	ctx, cancel := context.WithCancel(context.Background())
	bus := &AsyncBus{
		MemoryBus: NewMemoryBus(config),
		workers:   workers,
		queue:     make(chan asyncEvent, config.BufferSize),
		ctx:       ctx,
		cancel:    cancel,
	}

	// Start worker pool
	for i := 0; i < workers; i++ {
		bus.wg.Add(1)
		go bus.worker()
	}

	return bus
}

// worker processes events from the queue.
func (b *AsyncBus) worker() {
	defer b.wg.Done()
	for {
		select {
		case <-b.ctx.Done():
			return
		case evt := <-b.queue:
			b.MemoryBus.Publish(evt.ctx, evt.event)
		}
	}
}

// Publish sends an event asynchronously to the queue.
func (b *AsyncBus) Publish(ctx context.Context, event kernel.Event) {
	select {
	case b.queue <- asyncEvent{ctx: ctx, event: event}:
	default:
		// Queue full, drop event or handle synchronously
		b.MemoryBus.Publish(ctx, event)
	}
}

// Close stops the async bus and waits for workers to finish.
func (b *AsyncBus) Close() error {
	b.cancel()
	close(b.queue)
	b.wg.Wait()
	return nil
}

// Ensure AsyncBus implements kernel.EventBus.
var _ kernel.EventBus = (*AsyncBus)(nil)

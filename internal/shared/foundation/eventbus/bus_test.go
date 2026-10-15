package eventbus_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"gogo/internal/shared/foundation/eventbus"
	"gogo/internal/shared/kernel"
)

func TestMemoryBus_PublishAndSubscribe(t *testing.T) {
	bus := eventbus.NewMemoryBus(nil)
	ctx := context.Background()

	var received kernel.Event
	var wg sync.WaitGroup
	wg.Add(1)

	handler := func(ctx context.Context, event kernel.Event) error {
		received = event
		wg.Done()
		return nil
	}

	sub, err := bus.Subscribe(ctx, handler)
	if err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	event := kernel.NewBaseEvent("test.event", "payload")
	bus.Publish(ctx, event)

	wg.Wait()

	if received == nil {
		t.Fatal("expected to receive event")
	}
	if received.Type() != "test.event" {
		t.Errorf("expected event type 'test.event', got %v", received.Type())
	}
}

func TestMemoryBus_SubscribeTypes(t *testing.T) {
	bus := eventbus.NewMemoryBus(nil)
	ctx := context.Background()

	receivedCount := 0
	handler := func(ctx context.Context, event kernel.Event) error {
		receivedCount++
		return nil
	}

	sub, err := bus.SubscribeTypes(ctx, handler, "event.a", "event.b")
	if err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	bus.Publish(ctx, kernel.NewBaseEvent("event.a", nil))
	bus.Publish(ctx, kernel.NewBaseEvent("event.b", nil))
	bus.Publish(ctx, kernel.NewBaseEvent("event.c", nil)) // Should not be received

	if receivedCount != 2 {
		t.Errorf("expected to receive 2 events, got %d", receivedCount)
	}
}

func TestMemoryBus_Unsubscribe(t *testing.T) {
	bus := eventbus.NewMemoryBus(nil)
	ctx := context.Background()

	receivedCount := 0
	handler := func(ctx context.Context, event kernel.Event) error {
		receivedCount++
		return nil
	}

	sub, err := bus.Subscribe(ctx, handler)
	if err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}

	bus.Publish(ctx, kernel.NewBaseEvent("test", nil))

	sub.Unsubscribe()

	bus.Publish(ctx, kernel.NewBaseEvent("test", nil))

	if receivedCount != 1 {
		t.Errorf("expected to receive 1 event, got %d", receivedCount)
	}
}

func TestMemoryBus_PanicRecovery(t *testing.T) {
	var panicHandled bool

	config := eventbus.DefaultConfig()
	config.PanicHandler = func(event kernel.Event, recovered any) {
		panicHandled = true
	}

	bus := eventbus.NewMemoryBus(config)
	ctx := context.Background()

	handler := func(ctx context.Context, event kernel.Event) error {
		panic("test panic")
	}

	sub, err := bus.Subscribe(ctx, handler)
	if err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	bus.Publish(ctx, kernel.NewBaseEvent("test", nil))

	if !panicHandled {
		t.Error("expected panic to be handled")
	}
}

func TestAsyncBus_PublishAsync(t *testing.T) {
	bus := eventbus.NewAsyncBus(nil, 2)
	defer bus.Close()
	ctx := context.Background()

	var mu sync.Mutex
	receivedCount := 0

	handler := func(ctx context.Context, event kernel.Event) error {
		mu.Lock()
		receivedCount++
		mu.Unlock()
		return nil
	}

	sub, _ := bus.Subscribe(ctx, handler)
	defer sub.Unsubscribe()

	// Publish multiple events
	for i := 0; i < 10; i++ {
		bus.Publish(ctx, kernel.NewBaseEvent("test", i))
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := receivedCount
	mu.Unlock()

	if count != 10 {
		t.Errorf("expected to receive 10 events, got %d", count)
	}
}

func TestMemoryBus_MaxSubscribers(t *testing.T) {
	config := &eventbus.Config{
		MaxSubscribers: 2,
	}
	bus := eventbus.NewMemoryBus(config)
	ctx := context.Background()

	handler := func(ctx context.Context, event kernel.Event) error {
		return nil
	}

	// Subscribe up to limit
	sub1, err := bus.Subscribe(ctx, handler)
	if err != nil {
		t.Fatalf("first subscription failed: %v", err)
	}
	defer sub1.Unsubscribe()

	sub2, err := bus.Subscribe(ctx, handler)
	if err != nil {
		t.Fatalf("second subscription failed: %v", err)
	}
	defer sub2.Unsubscribe()

	// Third subscription should fail
	_, err = bus.Subscribe(ctx, handler)
	if err == nil {
		t.Error("expected error when exceeding max subscribers")
	}
}

package events

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSubscribeAndPublish(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	got := make(chan Event, 1)
	bus.Subscribe(func(ctx context.Context, event Event) {
		got <- event
	})

	const typ = "catalog.product.created"
	bus.Publish(context.Background(), Event{Type: typ, Payload: "id-1"})

	select {
	case event := <-got:
		if event.Type != typ {
			t.Errorf("Type = %q, want %q", event.Type, typ)
		}
		if event.Payload != "id-1" {
			t.Errorf("Payload = %v, want id-1", event.Payload)
		}
	default:
		t.Fatal("subscriber did not receive the event")
	}
}

func TestSynchronousDelivery(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	observed := 0
	bus.Subscribe(func(ctx context.Context, event Event) {
		observed++
	})

	// Publish must return only after every handler ran — consumers such as
	// cache invalidation must be effective before the request resumes.
	bus.Publish(context.Background(), Event{Type: "t"})
	if observed != 1 {
		t.Errorf("observed = %d, want 1 (synchronous delivery)", observed)
	}
}

func TestMultipleSubscribers(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	var mu sync.Mutex
	counts := make(map[string]int)
	bus.Subscribe(func(ctx context.Context, event Event) {
		mu.Lock()
		counts["a"]++
		mu.Unlock()
	})
	bus.Subscribe(func(ctx context.Context, event Event) {
		mu.Lock()
		counts["b"]++
		mu.Unlock()
	})

	bus.Publish(context.Background(), Event{Type: "t"})
	bus.Publish(context.Background(), Event{Type: "t"})

	mu.Lock()
	defer mu.Unlock()
	if counts["a"] != 2 || counts["b"] != 2 {
		t.Errorf("counts = %v, want a:2 b:2", counts)
	}
}

func TestPanickingSubscriberIsContained(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	bus.Subscribe(func(ctx context.Context, event Event) {
		panic("boom")
	})
	bus.Subscribe(func(ctx context.Context, event Event) {
		t.Log("second subscriber still ran")
	})

	// Must not panic despite the first subscriber panicking.
	bus.Publish(context.Background(), Event{Type: "t"})
}

func TestPanicReporterObservesContainedPanics(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	var (
		mu       sync.Mutex
		reported []any
	)
	bus.WithPanicReporter(func(_ Event, recovered any) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, recovered)
	})

	bus.Subscribe(func(ctx context.Context, event Event) {
		panic("boom")
	})
	bus.Publish(context.Background(), Event{Type: "t"})

	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 1 || reported[0] != "boom" {
		t.Errorf("reported = %v, want [\"boom\"]", reported)
	}
}

func TestConcurrentPublish(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	var mu sync.Mutex
	total := 0
	bus.Subscribe(func(ctx context.Context, event Event) {
		mu.Lock()
		total++
		mu.Unlock()
	})

	const publishers = 8
	const perPublisher = 100

	var wg sync.WaitGroup
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perPublisher; j++ {
				bus.Publish(context.Background(), Event{Type: "t"})
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if total != publishers*perPublisher {
		t.Errorf("total = %d, want %d", total, publishers*perPublisher)
	}
}

func TestSubscribeTypesFiltersByContract(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	var got []string
	var mu sync.Mutex
	bus.SubscribeTypes(func(_ context.Context, event Event) {
		mu.Lock()
		got = append(got, event.Type)
		mu.Unlock()
	}, "catalog.product.created", "catalog.product.deleted")

	bus.Publish(context.Background(), Event{Type: "catalog.product.created", Payload: "a"})
	bus.Publish(context.Background(), Event{Type: "catalog.product.updated", Payload: "b"})
	bus.Publish(context.Background(), Event{Type: "catalog.product.deleted", Payload: "c"})

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != "catalog.product.created" || got[1] != "catalog.product.deleted" {
		t.Errorf("received = %v, want only the subscribed types", got)
	}
}

// plainBus is a Bus without typed delivery, used to pin the Subscribe
// fallback path.
type plainBus struct {
	handler Handler
}

func (p *plainBus) Publish(ctx context.Context, event Event) {
	if p.handler != nil {
		p.handler(ctx, event)
	}
}

func (p *plainBus) Subscribe(handler Handler) { p.handler = handler }

func TestSubscribeHelperFallsBackToAllEvents(t *testing.T) {
	t.Parallel()

	// A plain Bus (no typed support) still receives everything through the
	// Subscribe helper: typed subscriptions are an optimization, never a
	// behavioural change.
	bus := &plainBus{}
	received := 0

	Subscribe(bus, func(_ context.Context, _ Event) { received++ }, "a")

	bus.Publish(context.Background(), Event{Type: "a"})
	bus.Publish(context.Background(), Event{Type: "b"})
	if received != 2 {
		t.Errorf("received = %d, want 2 (fallback handler receives every event)", received)
	}

	// Typed path returns a cancellable handle.
	typed := NewMemoryBus()
	sub := Subscribe(typed, func(_ context.Context, _ Event) {}, "catalog.product.created")
	if sub == nil {
		t.Fatal("Subscribe on a typed bus should return a cancellation handle")
	}
	sub.Cancel() // must not panic
}

func TestCancelStopsDelivery(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	count := 0
	var mu sync.Mutex
	sub := bus.SubscribeTypes(func(_ context.Context, _ Event) {
		mu.Lock()
		count++
		mu.Unlock()
	}, "t")

	bus.Publish(context.Background(), Event{Type: "t"})
	sub.Cancel()
	sub.Cancel() // idempotent
	bus.Publish(context.Background(), Event{Type: "t"})

	mu.Lock()
	defer mu.Unlock()
	if count != 1 {
		t.Errorf("count = %d, want 1 (cancelled subscription must stop receiving)", count)
	}
}

// TestCancelCompactsSubscriptions pins that cancelled handlers are dropped
// from the bus, so a long-lived module bus never accumulates stale entries.
func TestCancelCompactsSubscriptions(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	subs := make([]*Subscription, 0, 5)
	for i := 0; i < 5; i++ {
		subs = append(subs, bus.SubscribeTypes(func(_ context.Context, _ Event) {}))
	}
	if got := bus.Len(); got != 5 {
		t.Fatalf("Len() = %d after subscribing 5, want 5", got)
	}

	for _, sub := range subs {
		sub.Cancel()
	}
	if got := bus.Len(); got != 0 {
		t.Errorf("Len() = %d after cancelling all, want 0 (stale entries compacted)", got)
	}

	// New subscriptions after compaction still work.
	var received bool
	bus.SubscribeTypes(func(_ context.Context, _ Event) { received = true }, "again")
	bus.Publish(context.Background(), Event{Type: "again"})
	if !received {
		t.Error("post-compaction subscription must still receive events")
	}
}

// TestLenCountsLiveSubscribersOnly pins the observability contract.
func TestLenCountsLiveSubscribersOnly(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()
	first := bus.SubscribeTypes(func(_ context.Context, _ Event) {})
	second := bus.SubscribeTypes(func(_ context.Context, _ Event) {})

	if got := bus.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	first.Cancel()
	if got := bus.Len(); got != 1 {
		t.Errorf("Len() = %d after cancelling one, want 1", got)
	}
	second.Cancel()
	if got := bus.Len(); got != 0 {
		t.Errorf("Len() = %d after cancelling both, want 0", got)
	}
}

func TestPublishAsyncDeliversInBackground(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	delivered := make(chan Event, 1)
	bus.Subscribe(func(_ context.Context, event Event) {
		delivered <- event
	})

	bus.PublishAsync(context.Background(), Event{Type: "async", Payload: "v"})

	select {
	case event := <-delivered:
		if event.Type != "async" {
			t.Errorf("Type = %q, want async", event.Type)
		}
		if event.Payload != "v" {
			t.Errorf("Payload = %v, want v", event.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async subscriber did not receive the event")
	}
}

func TestPublishAsyncAfterCancel(t *testing.T) {
	t.Parallel()

	bus := NewMemoryBus()

	received := make(chan struct{}, 1)
	sub := bus.SubscribeTypes(func(_ context.Context, _ Event) {
		received <- struct{}{}
	}) // no types = every event; gives a cancellable handle
	sub.Cancel()

	// Cancelled before async dispatch: the goroutine must not panic and
	// must not deliver.
	bus.PublishAsync(context.Background(), Event{Type: "t"})

	select {
	case <-received:
		t.Fatal("cancelled async subscriber should not receive events")
	case <-time.After(50 * time.Millisecond):
	}
}

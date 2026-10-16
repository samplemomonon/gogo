package adapter

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"gogo/internal/shared/events"
	"gogo/internal/shared/kernel"
)

func TestEventBusAdapterPublish(t *testing.T) {
	t.Parallel()

	bus := events.NewMemoryBus()
	var got []events.Event
	bus.Subscribe(func(_ context.Context, event events.Event) {
		got = append(got, event)
	})

	adapted := NewEventBusAdapter(bus)
	adapted.Publish(context.Background(), kernel.NewBaseEvent("order.created", map[string]string{"id": "o-1"}))

	if len(got) != 1 {
		t.Fatalf("published %d events, want 1", len(got))
	}
	if got[0].Type != "order.created" {
		t.Errorf("Type = %q, want %q", got[0].Type, "order.created")
	}
}

func TestEventBusAdapterSubscribe(t *testing.T) {
	t.Parallel()

	bus := events.NewMemoryBus()
	adapted := NewEventBusAdapter(bus)

	var got []kernel.Event
	sub, err := adapted.Subscribe(context.Background(), func(_ context.Context, event kernel.Event) error {
		got = append(got, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if sub == nil {
		t.Fatal("Subscribe returned a nil subscription")
	}

	bus.Publish(context.Background(), events.Event{Type: "order.created", Payload: "payload-1"})

	if len(got) != 1 {
		t.Fatalf("handler saw %d events, want 1", len(got))
	}
	// The adapter converts the shared envelope into the kernel's contract:
	// same type, same payload, now carrying a timestamp.
	if got[0].Type() != "order.created" {
		t.Errorf("Type() = %q, want %q", got[0].Type(), "order.created")
	}
	if got[0].Payload() != "payload-1" {
		t.Errorf("Payload() = %v, want %v", got[0].Payload(), "payload-1")
	}
	if got[0].Timestamp() == 0 {
		t.Error("Timestamp() = 0; the adapter must stamp converted events")
	}

	t.Run("Unsubscribe stops delivery", func(t *testing.T) {
		if err := sub.Unsubscribe(); err != nil {
			t.Fatalf("Unsubscribe: %v", err)
		}
		bus.Publish(context.Background(), events.Event{Type: "order.created"})
		if len(got) != 1 {
			t.Errorf("handler saw %d events after Unsubscribe, want 1", len(got))
		}
	})
}

func TestEventBusAdapterSubscribeTypes(t *testing.T) {
	t.Parallel()

	bus := events.NewMemoryBus()
	adapted := NewEventBusAdapter(bus)

	var got []string
	_, err := adapted.SubscribeTypes(context.Background(), func(_ context.Context, event kernel.Event) error {
		got = append(got, event.Type())
		return nil
	}, "order.created")
	if err != nil {
		t.Fatalf("SubscribeTypes: %v", err)
	}

	bus.Publish(context.Background(), events.Event{Type: "order.created"})
	bus.Publish(context.Background(), events.Event{Type: "order.cancelled"})

	// The type filter must survive the adaptation: a subscriber for one type
	// that receives every type would be a correctness bug, not a perf one.
	if len(got) != 1 || got[0] != "order.created" {
		t.Errorf("handler saw %v, want only [order.created]", got)
	}
}

func TestLoggerAdapter(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := NewLoggerAdapter(slog.New(slog.NewTextHandler(&buf, nil)))

	logger.Info("started", "port", 8080)

	out := buf.String()
	if !strings.Contains(out, "started") || !strings.Contains(out, "port=8080") {
		t.Errorf("log output %q should carry the message and the field", out)
	}

	buf.Reset()
	logger.With("service", "gateway").Warn("degraded")
	out = buf.String()
	if !strings.Contains(out, "degraded") || !strings.Contains(out, "service=gateway") {
		t.Errorf("log output %q should carry the derived field", out)
	}
}

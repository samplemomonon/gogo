package kernel

import (
	"testing"
	"time"
)

func TestNewBaseEvent(t *testing.T) {
	t.Parallel()

	type payload struct{ ID string }

	before := time.Now().Unix()
	event := NewBaseEvent("order.created", payload{ID: "o-1"})
	after := time.Now().Unix()

	if got := event.Type(); got != "order.created" {
		t.Errorf("Type() = %q, want %q", got, "order.created")
	}

	got, ok := event.Payload().(payload)
	if !ok {
		t.Fatalf("Payload() = %T, want payload", event.Payload())
	}
	if got.ID != "o-1" {
		t.Errorf("Payload().ID = %q, want %q", got.ID, "o-1")
	}

	// The timestamp is stamped at construction, so it must fall inside the
	// window the test observed — a zero or wildly future value would break
	// every consumer that orders events by it.
	ts := event.Timestamp()
	if ts < before || ts > after {
		t.Errorf("Timestamp() = %d, want within [%d, %d]", ts, before, after)
	}
}

func TestBaseEventSatisfiesEvent(t *testing.T) {
	t.Parallel()

	var _ Event = NewBaseEvent("x", nil)
}

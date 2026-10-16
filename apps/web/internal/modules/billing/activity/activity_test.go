package activity

import (
	"context"
	"testing"

	"gogo/internal/shared/events"
	"gogo/internal/shared/productevents"
)

func TestTheProjectionRecordsEveryProductChange(t *testing.T) {
	t.Parallel()

	feed := New(10)
	ctx := context.Background()

	feed.OnEvent(ctx, events.Event{Type: productevents.ProductCreated, Payload: productevents.Payload{ID: "1"}})
	feed.OnEvent(ctx, events.Event{Type: productevents.ProductUpdated, Payload: productevents.Payload{ID: "2"}})
	feed.OnEvent(ctx, events.Event{Type: productevents.ProductDeleted, Payload: productevents.Payload{ID: "3"}})
	feed.OnEvent(ctx, events.Event{Type: productevents.ProductStockAdjusted, Payload: productevents.Payload{ID: "4"}})

	recent := feed.Recent()
	if len(recent) != 4 {
		t.Fatalf("len(Recent) = %d, want 4", len(recent))
	}

	// Newest first.
	want := []struct {
		id     string
		action Action
	}{
		{"4", ActionStockAdjusted},
		{"3", ActionDeleted},
		{"2", ActionUpdated},
		{"1", ActionCreated},
	}
	for i, entry := range recent {
		if entry.ProductID != want[i].id || entry.Action != want[i].action {
			t.Errorf("Recent[%d] = %+v, want id %s action %s", i, entry, want[i].id, want[i].action)
		}
	}
}

// An event outside the vocabulary, or one carrying a payload this projection cannot
// read, must be ignored rather than recorded as something unknown. A feed that showed
// "created" for an event that was not a product creation would be worse than one that
// showed nothing.
func TestForeignEventsAndPayloadsAreIgnored(t *testing.T) {
	t.Parallel()

	feed := New(10)
	ctx := context.Background()

	feed.OnEvent(ctx, events.Event{Type: "billing.charge.attempted", Payload: productevents.Payload{ID: "4"}})
	feed.OnEvent(ctx, events.Event{Type: productevents.ProductCreated, Payload: "not-a-payload"})
	feed.OnEvent(ctx, events.Event{Type: productevents.ProductUpdated, Payload: nil})

	// A real event, to prove the projection still works after being handed rubbish.
	feed.OnEvent(ctx, events.Event{Type: productevents.ProductCreated, Payload: productevents.Payload{ID: "1"}})

	recent := feed.Recent()
	if len(recent) != 1 {
		t.Fatalf("len(Recent) = %d, want 1 (foreign events and payloads must be ignored)", len(recent))
	}
	if recent[0].ProductID != "1" || recent[0].Action != ActionCreated {
		t.Errorf("Recent()[0] = %+v, want the one real product creation", recent[0])
	}
}

// An empty id is not a product. Recording it would put a blank row in the feed that no
// one can act on, and it means the decoder let something through that it should have
// rejected.
func TestAnEntryWithNoProductIDIsNotRecorded(t *testing.T) {
	t.Parallel()

	feed := New(10)
	feed.OnEvent(context.Background(), events.Event{
		Type: productevents.ProductCreated, Payload: productevents.Payload{ID: ""},
	})

	if got := len(feed.Recent()); got != 0 {
		t.Errorf("len(Recent) = %d, want 0 for an event with no product id", got)
	}
}

func TestRetentionLimit(t *testing.T) {
	t.Parallel()

	feed := New(2)
	for i := 0; i < 5; i++ {
		feed.OnEvent(context.Background(), events.Event{
			Type: productevents.ProductCreated, Payload: productevents.Payload{ID: "1"},
		})
	}

	if got := len(feed.Recent()); got != 2 {
		t.Errorf("len(Recent) = %d, want 2 (retention limit)", got)
	}
}

// Recent hands back a copy so a caller cannot reorder or rewrite the feed it is
// rendering.
func TestRecentReturnsCopy(t *testing.T) {
	t.Parallel()

	feed := New(10)
	feed.OnEvent(context.Background(), events.Event{
		Type: productevents.ProductCreated, Payload: productevents.Payload{ID: "1"},
	})

	first := feed.Recent()
	first[0].ProductID = "mutated"

	if got := feed.Recent()[0].ProductID; got != "1" {
		t.Errorf("Recent()[0].ProductID = %q, want 1 (the copy must protect the projection)", got)
	}
}

// Every action the projection can name must be reachable from an event type it
// subscribes to, so the productevents vocabulary and the feed cannot drift apart.
func TestEveryActionHasAnEventType(t *testing.T) {
	t.Parallel()

	feed := New(10)
	ctx := context.Background()
	for _, typ := range productevents.Types() {
		feed.OnEvent(ctx, events.Event{Type: typ, Payload: productevents.Payload{ID: "1"}})
	}

	actions := map[Action]bool{}
	for _, entry := range feed.Recent() {
		actions[entry.Action] = true
	}
	for _, want := range []Action{ActionCreated, ActionUpdated, ActionDeleted, ActionStockAdjusted} {
		if !actions[want] {
			t.Errorf("no event type in productevents.Types() produced action %q", want)
		}
	}
}

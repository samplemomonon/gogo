// Package activity is the billing module's projection of product change events. It is
// the server-side analogue of an htmx island reacting to a custom DOM event: billing
// subscribes to the gateway's product-changed vocabulary on the shared bus and keeps a
// small read model in sync — no import of a product service, no polling, no coupling.
package activity

import (
	"context"
	"sync"
	"time"

	"gogo/internal/shared/events"
	"gogo/internal/shared/productevents"
)

// Action describes the product change that was observed.
type Action string

const (
	// ActionCreated records that a product was created.
	ActionCreated Action = "created"
	// ActionUpdated records that a product was updated.
	ActionUpdated Action = "updated"
	// ActionDeleted records that a product was deleted.
	ActionDeleted Action = "deleted"
	// ActionStockAdjusted records that a product's stock changed.
	ActionStockAdjusted Action = "stock_adjusted"
)

// Entry is one product change as observed by this module.
type Entry struct {
	ProductID string
	Action    Action
	When      time.Time
}

// Activity keeps the most recent product changes, newest first.
type Activity struct {
	mu      sync.Mutex
	entries []Entry
	limit   int
}

// New returns an activity projection that retains at most limit entries.
func New(limit int) *Activity {
	return &Activity{limit: limit}
}

// OnEvent records any product change. It is written as one handler over the whole
// vocabulary rather than one method per event type, because every type means the same
// thing to this projection: something about a product changed.
//
// An event whose payload this module cannot read is ignored rather than recorded as
// something unknown. The payload is validated here so a misrouted event can never
// corrupt the feed.
func (a *Activity) OnEvent(_ context.Context, event events.Event) {
	payload, ok := event.Payload.(productevents.Payload)
	if !ok {
		return
	}
	if payload.ID == "" {
		// No product id means the decoder let through something it should have
		// rejected. Recording it would put a blank row in the feed that no one can act
		// on, and it would read as a real change that happened.
		return
	}

	var action Action
	switch event.Type {
	case productevents.ProductCreated:
		action = ActionCreated
	case productevents.ProductUpdated:
		action = ActionUpdated
	case productevents.ProductDeleted:
		action = ActionDeleted
	case productevents.ProductStockAdjusted:
		action = ActionStockAdjusted
	default:
		// Not a product change. The subscription is contract-keyed, so this is
		// unreachable in practice; ignoring it keeps the projection honest if a caller
		// ever wires it to a wider vocabulary.
		return
	}

	a.record(Entry{ProductID: payload.ID, Action: action, When: time.Now()})
}

// record prepends an entry, honoring the retention limit.
func (a *Activity) record(entry Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append([]Entry{entry}, a.entries...)
	if len(a.entries) > a.limit {
		a.entries = a.entries[:a.limit]
	}
}

// Recent returns the recorded entries, newest first.
func (a *Activity) Recent() []Entry {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]Entry, len(a.entries))
	copy(out, a.entries)
	return out
}

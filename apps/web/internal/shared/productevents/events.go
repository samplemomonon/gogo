// Package productevents is the gateway's observer over the product platform's change
// stream.
//
// The platform publishes every product mutation to Kafka as `product.events.v1`
// (product-service's transactional outbox, relayed after commit). This package
// consumes that topic and republishes each record onto the gateway's in-process bus
// under a vocabulary local to this process, so a module that reacts to "a product
// changed" needs no knowledge of Kafka, of the platform's envelope, or of which
// service performed the write.
//
// Why the gateway consumes the stream at all, given it no longer owns the write side:
// the billing vertical's activity feed reports product activity, which is by
// definition about writes made elsewhere. Island-cache invalidation is the other
// intended consumer — the pagekit InvalidateOnEvents/InvalidatePrefixOnEvents
// helpers are the seam — but no island subscribes in this build: the storefront
// grid is TTL-bounded instead, and the reason is recorded in
// internal/features/home/ui/page.go.
//
// What this is not: a source of truth. The payload is a notification (id and what
// happened), following Fowler's Event Notification. A consumer that needs product state
// asks the browse read model, exactly as it would for any other read.
package productevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gogo/internal/shared/events"
)

// Topic is the Kafka topic product-service publishes every product event to. The
// trailing v1 is the schema version: a breaking change means a new topic rather than
// a new field on this one.
const Topic = "product.events.v1"

// SchemaVersion is the envelope version this package understands. A record declaring
// anything else is skipped rather than misread.
const SchemaVersion = 1

// In-process event types. These are this process's own vocabulary, deliberately not
// the platform's wire strings: the bus is in-process, so nothing outside can observe
// these names, and keeping them separate means a platform-side rename does not reach
// into module code. A module subscribes to these constants and never to a raw string.
const (
	ProductCreated       = "product.changed.created"
	ProductUpdated       = "product.changed.updated"
	ProductDeleted       = "product.changed.deleted"
	ProductStockAdjusted = "product.changed.stock_adjusted"
)

// Types is every in-process type a consumer may subscribe to. A module that wants to
// know "something about this product changed" subscribes to all of them via
// events.SubscribeTypes, and never sees an event outside this set.
func Types() []string {
	return []string{ProductCreated, ProductUpdated, ProductDeleted, ProductStockAdjusted}
}

// Payload is the published body of a product change, mapped onto this process's own
// struct.
//
// ID is the product the change is about. A variant change carries the *product* id,
// because a variant has no meaning without its product and every cache keyed by product
// must be dropped when one changes.
type Payload struct {
	// ID is the product id as this process names products: the decimal string form of
	// the platform's int64 key, matching what the consumer clients hand back.
	ID string
	// TenantID is the tenant whose catalogue changed, or "" when the platform does not
	// say. The platform is single-tenant today, so it does not carry one; the field
	// exists because this process is multi-tenant and a consumer that builds a cache
	// key per tenant must be able to distinguish "no tenant on this event" from "a
	// tenant I guessed at". A consumer that needs a concrete tenant resolves it from
	// its own request context; it must not read one from the event.
	TenantID string
	// AggregateType and AggregateID are carried for logging. AggregateID is the
	// variant's id on a variant change.
	AggregateType string
	AggregateID   int64
}

// envelope is the published wrapper. Only the fields this package routes on are
// decoded; the event-specific body is read as raw JSON and mapped by decodePayload,
// because a consumer of a notification needs the id and nothing else.
type envelope struct {
	EventID       string          `json:"event_id"`
	EventType     string          `json:"event_type"`
	SchemaVersion int             `json:"schema_version"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   int64           `json:"aggregate_id"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
}

// ErrUndecodable is returned when a record cannot be understood. It is deliberately
// distinct from a transport error: an undecodable record is committed and skipped,
// because blocking a partition on one malformed message would stall every consumer
// behind it forever.
var ErrUndecodable = errors.New("productevents: undecodable record")

// ErrSkip is returned for a record this package deliberately ignores — a tombstone
// from a compacted topic, or an event type outside the product aggregate. It is not a
// failure.
var ErrSkip = errors.New("productevents: record skipped")

// Decode turns one Kafka record value into an in-process event.
//
// It returns ErrSkip for a tombstone (a nil value, which a compacted topic emits on
// delete) and for an event type this build does not map, ErrUndecodable for a payload
// it cannot parse, and an events.Event otherwise. A nil error always means the returned
// event is safe to publish.
func Decode(value []byte) (events.Event, error) {
	if len(value) == 0 {
		// A tombstone: a compacted topic emits one on delete. There is nothing to
		// publish and nothing to retry.
		return events.Event{}, ErrSkip
	}

	var env envelope
	if err := json.Unmarshal(value, &env); err != nil {
		return events.Event{}, fmt.Errorf("%w: envelope: %w", ErrUndecodable, err)
	}
	if env.SchemaVersion != SchemaVersion {
		return events.Event{}, fmt.Errorf("%w: schema version %d, want %d", ErrUndecodable, env.SchemaVersion, SchemaVersion)
	}

	eventType, ok := eventTypes[env.EventType]
	if !ok {
		// An event type this build does not know. Skipping it is correct: the stream is
		// versioned by topic, so an unknown type means a newer producer, and guessing at
		// its payload would be worse than ignoring it.
		return events.Event{}, ErrSkip
	}

	payload, err := decodePayload(env)
	if err != nil {
		return events.Event{}, err
	}

	return events.Event{Type: eventType, Payload: payload}, nil
}

// eventTypes maps every published event type onto this process's vocabulary. The
// mapping is explicit rather than a prefix rewrite so a new platform event type cannot
// be adopted by accident: it has to be added here, where a reader sees what it will
// mean to this process.
var eventTypes = map[string]string{
	"product.v1.product.created":        ProductCreated,
	"product.v1.product.updated":        ProductUpdated,
	"product.v1.product.deleted":        ProductDeleted,
	"product.v1.product.stock_adjusted": ProductStockAdjusted,

	// A variant change invalidates the same caches a product change does, so it maps to
	// the product vocabulary. That is the point of keying every event by product id.
	"product.v1.variant.created": ProductUpdated,
	"product.v1.variant.updated": ProductUpdated,
	"product.v1.variant.deleted": ProductDeleted,
}

// decodePayload pulls the product id out of an envelope's body.
//
// Two shapes have to be handled and the difference is load-bearing. A product snapshot
// carries its id at the top level. A *stock adjustment* carries {id, stock, delta} and
// a *variant* event carries {product_id, variant_id} — so their product id sits under a
// different key. Reading only payload.id would silently invalidate nothing for those
// event types, which shows up as a stale page rather than as an error.
func decodePayload(env envelope) (Payload, error) {
	if len(env.Payload) == 0 {
		return Payload{}, fmt.Errorf("%w: %s has no payload", ErrUndecodable, env.EventType)
	}

	var body struct {
		ID        *int64  `json:"id"`
		ProductID *int64  `json:"product_id"`
		TenantID  *string `json:"tenant_id"`
	}
	if err := json.Unmarshal(env.Payload, &body); err != nil {
		return Payload{}, fmt.Errorf("%w: %s payload: %w", ErrUndecodable, env.EventType, err)
	}

	payload := Payload{
		AggregateType: env.AggregateType,
		AggregateID:   env.AggregateID,
	}
	switch {
	case body.ProductID != nil:
		payload.ID = strconv.FormatInt(*body.ProductID, 10)
	case body.ID != nil:
		payload.ID = strconv.FormatInt(*body.ID, 10)
	default:
		return Payload{}, fmt.Errorf("%w: %s payload carries no product id", ErrUndecodable, env.EventType)
	}

	if body.TenantID != nil {
		payload.TenantID = *body.TenantID
	}
	return payload, nil
}

// Publisher republishes decoded platform events onto the gateway's in-process bus.
//
// The bus is in-process and best-effort, which is the right shape here: an event that
// cannot be delivered to a local subscriber is a cache that revalidates on its TTL
// instead. The durable replay path is Kafka itself — the consumer group commits
// offsets, so a gateway that was down resumes from where it stopped.
type Publisher struct {
	bus events.Bus
}

// NewPublisher returns a publisher over the shared in-process bus.
func NewPublisher(bus events.Bus) *Publisher { return &Publisher{bus: bus} }

// Publish decodes and republishes one record. A tombstone, an unknown event type or an
// undecodable payload returns an error the caller should treat as "committed and
// skipped" — see Decode's contract.
func (p *Publisher) Publish(ctx context.Context, value []byte) error {
	event, err := Decode(value)
	if err != nil {
		return err
	}
	p.bus.Publish(ctx, event)
	return nil
}

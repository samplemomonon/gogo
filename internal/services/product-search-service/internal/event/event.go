// Package event decodes the domain events published by product-service.
//
// These types are this service's half of a contract owned by the other service,
// so they are declared here rather than imported. Sharing the Go types would mean
// either a dependency between the two modules or a third shared module, and both
// would couple the deploy of the write side to the deploy of the read side. A
// duplicated struct is a cheap price for that independence: the two sides can
// then be built and released separately, and a mismatch shows up as a decode
// error in the consumer rather than as a compile error that forces a coordinated
// change.
//
// The wire format is the same JSON envelope product-service writes into its
// outbox: a header naming the event type, and a payload that is the aggregate as
// a full snapshot rather than a diff.
//
// The variant types are decoded even though this service indexes products only. A
// search result is a product, not a product with its variant list, so a variant
// event changes nothing here - but it still has to be parsed. Treating an
// unparseable variant payload as "not interesting" would hide a broken producer
// behind a silent no-op, whereas decoding it makes the failure permanent, which
// sends it to the dead-letter topic where someone can see it.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Topic is the topic product-service publishes to.
const Topic = "product.events.v1"

// SchemaVersion is the envelope version this service understands. A message
// claiming a different version is skipped rather than guessed at.
const SchemaVersion = 1

// The event types this service understands. They must match product-service's
// internal/event exactly.
//
// Only the product types change the index; the variant types are recognised so
// they can be decoded and deliberately ignored. See the package comment.
const (
	TypeProductCreated       = "product.v1.product.created"
	TypeProductUpdated       = "product.v1.product.updated"
	TypeProductDeleted       = "product.v1.product.deleted"
	TypeProductStockAdjusted = "product.v1.product.stock_adjusted"

	TypeVariantCreated = "product.v1.variant.created"
	TypeVariantUpdated = "product.v1.variant.updated"
	TypeVariantDeleted = "product.v1.variant.deleted"
)

// Envelope is the outer message shape. A consumer switches on EventType and
// decodes Payload into the matching struct, so nothing but the envelope needs to
// be understood in order to route.
type Envelope struct {
	// EventID is the deduplication key. It is a database-generated UUID, unique
	// per event, so seeing the same one twice means a redelivery rather than two
	// distinct changes.
	EventID string `json:"event_id"`
	// EventType is one of the Type* constants above.
	EventType string `json:"event_type"`
	// SchemaVersion is the envelope version the producer wrote.
	SchemaVersion int `json:"schema_version"`
	// AggregateType and AggregateID identify what the event is about.
	AggregateType string `json:"aggregate_type"`
	AggregateID   int64  `json:"aggregate_id"`
	// OccurredAt is when the change committed, which is not necessarily when the
	// event was published: the producer relays through an outbox, so a backlog
	// makes these differ.
	OccurredAt time.Time `json:"occurred_at"`
	// Payload is the event-specific body.
	Payload json.RawMessage `json:"payload"`
}

// ProductSnapshot is a product as the producer saw it when the change committed.
//
// Every event about a product carries one, which is what makes the projection
// idempotent: re-applying an event writes the same values again rather than
// needing to know what changed.
type ProductSnapshot struct {
	ID             int64     `json:"id"`
	CategoryID     int64     `json:"category_id"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	Description    *string   `json:"description"`
	Brand          *string   `json:"brand"`
	PriceCents     int64     `json:"price_cents"`
	Currency       string    `json:"currency"`
	Stock          int       `json:"stock"`
	Status         string    `json:"status"`
	PrimaryMediaID *int64    `json:"primary_media_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// VariantSnapshot is a variant as the producer saw it.
type VariantSnapshot struct {
	ID         int64     `json:"id"`
	ProductID  int64     `json:"product_id"`
	SKU        string    `json:"sku"`
	Name       string    `json:"name"`
	PriceCents int64     `json:"price_cents"`
	Currency   string    `json:"currency"`
	Stock      int       `json:"stock"`
	MediaID    *int64    `json:"media_id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ProductDeletedPayload is the body of a product delete. It carries no snapshot:
// the consumer only needs the id to drop the row.
type ProductDeletedPayload struct {
	ID int64 `json:"id"`
}

// ProductStockAdjustedPayload is the body of a stock adjustment. It carries the
// resulting stock rather than only the delta, so a consumer that missed an
// earlier adjustment still converges on the right value.
type ProductStockAdjustedPayload struct {
	ID    int64 `json:"id"`
	Stock int   `json:"stock"`
	Delta int64 `json:"delta"`
}

// VariantDeletedPayload is the body of a variant delete.
type VariantDeletedPayload struct {
	ProductID int64 `json:"product_id"`
	VariantID int64 `json:"variant_id"`
}

// ErrUndecodable marks a message this version of the service can never
// interpret: malformed JSON, a missing event id, an envelope from a future schema
// version, or a payload that does not match its event type.
//
// It is distinct from an ordinary error because the consumer must treat it
// differently. Retrying such a record produces the identical failure forever,
// which would hold its partition and stop every product behind it from ever being
// readable. It is sent to the dead-letter topic instead, where someone can look at
// it once the code that could handle it has been deployed.
type ErrUndecodable struct{ err error }

func (e *ErrUndecodable) Error() string { return e.err.Error() }

func (e *ErrUndecodable) Unwrap() error { return e.err }

// Permanent marks err as a failure no retry can fix.
func Permanent(err error) error { return &ErrUndecodable{err: err} }

// IsPermanent reports whether err can never succeed on a retry. The consumer uses
// this to decide between a dead letter and a backoff.
func IsPermanent(err error) bool {
	var target *ErrUndecodable
	return errors.As(err, &target)
}

// Decode reads an envelope and returns it with the payload already validated as
// the type the event type calls for.
//
// Validating the payload here rather than in the projector means a malformed
// message is rejected at the edge of the system, where it can be logged and sent
// to the dead-letter topic, instead of half-applying and leaving the read model
// inconsistent.
//
// Every failure from this function is permanent by construction: the message bytes
// are fixed, so a retry decodes to the same failure. Callers can check
// IsPermanent rather than having to enumerate the cases.
func Decode(body []byte) (*Envelope, any, error) {
	var envelope Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, nil, Permanent(fmt.Errorf("decode event envelope: %w", err))
	}
	if envelope.EventID == "" {
		return nil, nil, Permanent(errors.New("event envelope has no event_id"))
	}
	if envelope.SchemaVersion != SchemaVersion {
		return nil, nil, Permanent(fmt.Errorf(
			"unsupported event schema version %d, this service understands %d",
			envelope.SchemaVersion, SchemaVersion,
		))
	}

	payload, err := decodePayload(&envelope)
	if err != nil {
		return nil, nil, Permanent(err)
	}
	return &envelope, payload, nil
}

// decodePayload unmarshals the body into the struct its event type implies.
func decodePayload(envelope *Envelope) (any, error) {
	decode := func(target any) (any, error) {
		if err := json.Unmarshal(envelope.Payload, target); err != nil {
			return nil, fmt.Errorf("decode %s payload: %w", envelope.EventType, err)
		}
		return target, nil
	}

	switch envelope.EventType {
	case TypeProductCreated, TypeProductUpdated:
		return decode(&ProductSnapshot{})
	case TypeProductDeleted:
		return decode(&ProductDeletedPayload{})
	case TypeProductStockAdjusted:
		return decode(&ProductStockAdjustedPayload{})
	case TypeVariantCreated, TypeVariantUpdated:
		return decode(&VariantSnapshot{})
	case TypeVariantDeleted:
		return decode(&VariantDeletedPayload{})
	default:
		// An unknown type is a forward-compatible producer, not an error: a newer
		// product-service may emit events this version does not know. Returning
		// nil payload with a nil error lets the caller skip it and keep going.
		return nil, nil
	}
}

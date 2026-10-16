// Package projection applies product events to the search index.
//
// It sits between the Kafka consumer and the database. The consumer's job is to
// fetch records and commit offsets; this package's job is to decide what a record
// means and to make the change. Keeping them apart means the idempotency rules,
// which are the subtle part, are testable without a broker.
//
// Variant events are recognised and then ignored. A search result is a product,
// not a product with its variant list, so a variant has nothing to update here.
// They are still decoded rather than skipped blindly, so an unparseable variant
// payload is a permanent failure that gets dead-lettered instead of being mistaken
// for an event type from a future producer.
package projection

import (
	"context"
	"fmt"
	"log/slog"

	"product-search-service/internal/event"
)

// Writer is the index mutation this package needs. It is an interface so the
// projector can be tested without a database, and so the consumer does not need
// to know how the writes are made.
//
// It is declared as a transaction runner plus the mutations available inside it,
// rather than as a flat list of writes, because recording the event as processed
// and indexing it have to commit together. Exposing the writes individually would
// let a caller drive them in any order, and the dangerous order — mark processed,
// then fail to index — is exactly the one that loses an event permanently.
type Writer interface {
	// InTx runs fn inside a single transaction, committing if fn returns nil and
	// rolling back otherwise.
	InTx(ctx context.Context, fn func(Mutations) error) error
}

// Mutations are the index writes available inside a Writer's transaction.
type Mutations interface {
	// ApplyWithPosition records the event as processed along with where it was read
	// from, and reports whether it was new. A false return means this event has
	// been seen before, which is a normal outcome of at-least-once delivery rather
	// than an error.
	ApplyWithPosition(ctx context.Context, envelope *event.Envelope, topic string, partition int32, offset int64) (bool, error)

	IndexProduct(ctx context.Context, snapshot *event.ProductSnapshot, eventID string) error
	RemoveProduct(ctx context.Context, productID int64) error
	SetProductStock(ctx context.Context, productID int64, stock int, eventID string) error
}

// Outcome describes what happened to one event.
type Outcome int

const (
	// OutcomeApplied means the index changed.
	OutcomeApplied Outcome = iota
	// OutcomeDuplicate means the event had already been projected and was skipped.
	OutcomeDuplicate
	// OutcomeSkipped means the event type is not one this service understands, so
	// it was ignored without being marked processed.
	OutcomeSkipped
)

func (o Outcome) String() string {
	switch o {
	case OutcomeApplied:
		return "applied"
	case OutcomeDuplicate:
		return "duplicate"
	case OutcomeSkipped:
		return "skipped"
	default:
		return "unknown"
	}
}

// Projector turns decoded events into index writes.
type Projector struct {
	writer Writer
	log    *slog.Logger
}

// NewProjector returns a projector writing through w.
func NewProjector(w Writer, log *slog.Logger) *Projector {
	return &Projector{writer: w, log: log}
}

// Position is where a record was read from. It is recorded with the event so a
// replay can be traced back to a point in the log, which is the difference
// between "the index says X" and "the index says X because of offset 1,234 on
// partition 2".
type Position struct {
	Topic     string
	Partition int32
	Offset    int64
}

// Apply decodes one message and applies it.
//
// The order of the checks is the whole design. Deduplication happens first, so a
// redelivered event costs one indexed lookup and no write. An unrecognised type
// is skipped without being recorded, because a newer producer may legitimately
// emit events this version does not know about, and marking them processed would
// silently discard them if this service were later rolled back to understand
// them.
//
// The dedupe marker and the index write share one transaction. Recording the event
// and indexing it as two autocommit statements loses the event when the second
// fails: the marker is committed, so the redelivery is dismissed as a duplicate
// and the offset is committed, and the product is never searchable. One transaction
// rolls the marker back with the failed write, so the redelivery applies cleanly.
func (p *Projector) Apply(ctx context.Context, position Position, body []byte) (Outcome, error) {
	envelope, payload, err := event.Decode(body)
	if err != nil {
		return OutcomeSkipped, err
	}

	if payload == nil {
		// A known envelope carrying an event type this version does not handle.
		p.log.Debug("skipping unknown event type",
			"event_type", envelope.EventType, "event_id", envelope.EventID)
		return OutcomeSkipped, nil
	}

	var outcome Outcome
	err = p.writer.InTx(ctx, func(m Mutations) error {
		// The position is recorded with the event rather than left out: it is what
		// makes the projection auditable against the log it was built from.
		applied, err := m.ApplyWithPosition(ctx, envelope,
			position.Topic, position.Partition, position.Offset)
		if err != nil {
			return err
		}
		if !applied {
			// The event id has been seen, so this is a redelivery. The writes below
			// would produce the same document anyway, but skipping them keeps a
			// redelivery from looking like a fresh write in projected_at.
			p.log.Debug("skipping duplicate event",
				"event_type", envelope.EventType, "event_id", envelope.EventID)
			outcome = OutcomeDuplicate
			return nil
		}

		if err := p.applyPayload(ctx, m, envelope, payload); err != nil {
			return err
		}
		outcome = OutcomeApplied
		return nil
	})
	if err != nil {
		return OutcomeSkipped, err
	}
	return outcome, nil
}

// applyPayload dispatches on the event type and performs the write.
//
// It writes through the Mutations handed to it rather than through p.writer, so the
// write joins the caller's transaction instead of committing on its own.
func (p *Projector) applyPayload(ctx context.Context, m Mutations, envelope *event.Envelope, payload any) error {
	switch body := payload.(type) {
	case *event.ProductSnapshot:
		// A created and an updated product are applied identically: both carry a
		// full snapshot, so the upsert does not care which it was. This is also
		// the path that has to maintain search_text, which is generated from the
		// name, brand and slug the snapshot carries.
		return m.IndexProduct(ctx, body, envelope.EventID)

	case *event.ProductDeletedPayload:
		// Removing the document is what stops a deleted product turning up in
		// search results.
		return m.RemoveProduct(ctx, body.ID)

	case *event.ProductStockAdjustedPayload:
		// Only the stock column is written. The event does not carry the rest of
		// the row, and overwriting it from this event would clobber a newer product
		// update that was already indexed.
		return m.SetProductStock(ctx, body.ID, body.Stock, envelope.EventID)

	case *event.VariantSnapshot:
		// A variant is not searchable on its own. The product event that follows
		// carries the new product state, so there is nothing to do here.
		p.log.Debug("ignoring variant event, search indexes products",
			"event_type", envelope.EventType, "variant_id", body.ID)
		return nil

	case *event.VariantDeletedPayload:
		p.log.Debug("ignoring variant delete, search indexes products",
			"event_type", envelope.EventType, "variant_id", body.VariantID)
		return nil

	default:
		return fmt.Errorf("projection: no handler for decoded payload %T", payload)
	}
}

// A failure to decode is permanent, and that is what routes the record to the
// dead-letter topic instead of a backoff. Reclassification lives in the event
// package because it is a property of the wire format, not of the projection.
//
// A database failure, by contrast, is transient and is retried: the index comes
// back and the same record then applies cleanly.
func IsPermanent(err error) bool { return event.IsPermanent(err) }

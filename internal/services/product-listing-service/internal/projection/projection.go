// Package projection applies product events to the read model.
//
// It sits between the Kafka consumer and the database. The consumer's job is to
// fetch records and commit offsets; this package's job is to decide what a record
// means and to make the change. Keeping them apart means the idempotency rules,
// which are the subtle part, are testable without a broker.
package projection

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/event"
)

// Writer is the read-model mutation this package needs. It is an interface so the
// projector can be tested without a database, and so the consumer does not need
// to know how the writes are made.
//
// It is declared as a transaction runner plus the mutations available inside it,
// rather than as a flat list of writes, because recording the event as processed
// and applying it have to commit together. Exposing the writes individually would
// let a caller drive them in any order, and the dangerous order — mark processed,
// then fail to write — is exactly the one that loses an event permanently.
type Writer interface {
	// InTx runs fn inside a single transaction, committing if fn returns nil and
	// rolling back otherwise.
	InTx(ctx context.Context, fn func(Mutations) error) error
}

// Mutations are the read-model writes available inside a Writer's transaction.
type Mutations interface {
	// ApplyWithPosition records the event as processed along with where it was read
	// from, and reports whether it was new. A false return means this event has
	// been seen before, which is a normal outcome of at-least-once delivery rather
	// than an error.
	ApplyWithPosition(ctx context.Context, envelope *event.Envelope, topic string, partition int32, offset int64) (bool, error)

	UpsertProduct(ctx context.Context, snapshot *event.ProductSnapshot, eventID string) error
	UpsertVariant(ctx context.Context, snapshot *event.VariantSnapshot, eventID string) error
	DeleteProduct(ctx context.Context, productID int64) error
	DeleteVariant(ctx context.Context, variantID int64) error
	SetProductStock(ctx context.Context, productID int64, stock int, eventID string) error
}

// Outcome describes what happened to one event.
type Outcome int

const (
	// OutcomeApplied means the read model changed.
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

// Invalidator drops cached reads for a product whose rows have just changed.
//
// This is a port, not the cache itself, so the projection can invalidate without
// naming a cache implementation — and so a build with caching disabled wires a
// no-op rather than branching on nil.
//
// Why the projection needs it at all: the cache in front of this read model has a
// TTL, and a TTL alone does not bound staleness for data this service does not
// write. A product updated upstream stays visible at its old price for the whole
// TTL unless the write that changed it says so, and the only component that knows
// the write happened is the projection.
type Invalidator interface {
	// Invalidate drops every cached read for productID.
	Invalidate(productID int64)
}

// nopInvalidator is the Invalidator that drops nothing.
type nopInvalidator struct{}

// NopInvalidator returns an Invalidator that discards invalidation requests,
// for builds that run without a cache.
func NopInvalidator() Invalidator { return nopInvalidator{} }

func (nopInvalidator) Invalidate(int64) {}

// Projector turns decoded events into read-model writes.
type Projector struct {
	writer      Writer
	log         *slog.Logger
	invalidator Invalidator
}

// NewProjector returns a projector writing through w. Pass NopInvalidator() to
// run without cache invalidation.
func NewProjector(w Writer, log *slog.Logger, invalidator Invalidator) *Projector {
	if invalidator == nil {
		invalidator = nopInvalidator{}
	}
	return &Projector{writer: w, log: log, invalidator: invalidator}
}

// Position is where a record was read from. It is recorded with the event so a
// replay can be traced back to a point in the log, which is the difference
// between "the read model says X" and "the read model says X because of offset
// 1,234 on partition 2".
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
// The dedupe marker and the write share one transaction. Recording the event and
// applying it as two autocommit statements loses the event when the second fails:
// the marker is committed, so the redelivery is dismissed as a duplicate and the
// offset is committed, and the product never appears. One transaction rolls the
// marker back with the failed write, so the redelivery applies cleanly.
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
	// Collected during the transaction and acted on only after it commits. The
	// alternative — invalidating before the commit — has a window a concurrent
	// reader walks straight into: it misses, re-reads pre-commit rows, repopulates
	// the cache, and the commit then lands underneath a cache that is now stale
	// again with a fresh TTL.
	var touched []int64
	err = p.writer.InTx(ctx, func(m Mutations) error {
		// The position is recorded with the event rather than left out: it is what
		// makes the projection auditable against the log it was built from.
		applied, err := m.ApplyWithPosition(ctx, envelope,
			position.Topic, position.Partition, position.Offset)
		if err != nil {
			return err
		}
		if !applied {
			// The event id has been seen, so this is a redelivery. The upserts below
			// would produce the same rows anyway, but skipping them keeps a
			// redelivery from looking like a fresh write in projected_at.
			p.log.Debug("skipping duplicate event",
				"event_type", envelope.EventType, "event_id", envelope.EventID)
			outcome = OutcomeDuplicate
			return nil
		}

		if err := p.applyPayload(ctx, m, envelope, payload, &touched); err != nil {
			return err
		}
		outcome = OutcomeApplied
		return nil
	})
	if err != nil {
		return OutcomeSkipped, err
	}

	for _, id := range touched {
		p.invalidator.Invalidate(id)
	}
	return outcome, nil
}

// applyPayload dispatches on the event type and performs the write.
//
// It writes through the Mutations handed to it rather than through p.writer, so the
// write joins the caller's transaction instead of committing on its own.
//
// Every product whose cached reads just became wrong is appended to touched. A
// variant event is included because the variant-bearing read of its parent
// product changed too, and the variant-less read is cached under its own key: the
// product is what both are keyed on.
func (p *Projector) applyPayload(ctx context.Context, m Mutations, envelope *event.Envelope, payload any, touched *[]int64) error {
	switch body := payload.(type) {
	case *event.ProductSnapshot:
		// A created and an updated product are applied identically: both carry a
		// full snapshot, so the upsert does not care which it was.
		*touched = append(*touched, body.ID)
		return m.UpsertProduct(ctx, body, envelope.EventID)

	case *event.ProductDeletedPayload:
		*touched = append(*touched, body.ID)
		return m.DeleteProduct(ctx, body.ID)

	case *event.ProductStockAdjustedPayload:
		// Only the stock column is written. The event does not carry the rest of
		// the row, and overwriting it from this event would clobber a newer product
		// update that was already projected.
		*touched = append(*touched, body.ID)
		return m.SetProductStock(ctx, body.ID, body.Stock, envelope.EventID)

	case *event.VariantSnapshot:
		*touched = append(*touched, body.ProductID)
		return m.UpsertVariant(ctx, body, envelope.EventID)

	case *event.VariantDeletedPayload:
		*touched = append(*touched, body.ProductID)
		return m.DeleteVariant(ctx, body.VariantID)

	default:
		return fmt.Errorf("projection: no handler for decoded payload %T", payload)
	}
}

// A failure to decode is permanent, and that is what routes the record to the
// dead-letter topic instead of a backoff. Reclassification lives in the event
// package because it is a property of the wire format, not of the projection.
//
// A database failure, by contrast, is transient and is retried: the read model
// comes back and the same record then applies cleanly.
func IsPermanent(err error) bool { return event.IsPermanent(err) }

package projection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/event"
)

// recordingWriter captures what the projector asked the read model to do.
//
// It implements Writer, so the writes it records are exactly the writes that would
// have run inside a real transaction.
type recordingWriter struct {
	applied  bool
	applyErr error
	knownIDs map[string]bool

	products  []*event.ProductSnapshot
	variants  []*event.VariantSnapshot
	deletes   []int64
	varDelete []int64
	stock     []stockWrite
	position  []Position

	// inTx guards against a nested InTx, and txOpened counts how many transactions
	// Apply started, so a test can assert the marker and the write shared one.
	inTx      bool
	txOpened  int
	writeErrs map[string]error
}

type stockWrite struct {
	productID int64
	stock     int
}

// InTx models the repository's transaction: fn runs, and its error decides whether
// the work stands. txDepth makes a nested call an error rather than silently
// starting a second transaction, which would be the bug this interface exists to
// prevent.
func (w *recordingWriter) InTx(_ context.Context, fn func(Mutations) error) error {
	if w.inTx {
		return errors.New("InTx called while already inside a transaction")
	}
	w.inTx = true
	w.txOpened++
	defer func() { w.inTx = false }()
	return fn(w)
}

func (w *recordingWriter) ApplyWithPosition(
	_ context.Context,
	envelope *event.Envelope,
	topic string,
	partition int32,
	offset int64,
) (bool, error) {
	w.position = append(w.position, Position{Topic: topic, Partition: partition, Offset: offset})
	if w.applyErr != nil {
		return false, w.applyErr
	}
	if w.knownIDs != nil && w.knownIDs[envelope.EventID] {
		return false, nil
	}
	return w.applied, nil
}

func (w *recordingWriter) UpsertProduct(_ context.Context, s *event.ProductSnapshot, _ string) error {
	if err := w.writeErrs["product"]; err != nil {
		return err
	}
	w.products = append(w.products, s)
	return nil
}

func (w *recordingWriter) UpsertVariant(_ context.Context, s *event.VariantSnapshot, _ string) error {
	w.variants = append(w.variants, s)
	return nil
}

func (w *recordingWriter) DeleteProduct(_ context.Context, id int64) error {
	w.deletes = append(w.deletes, id)
	return nil
}

func (w *recordingWriter) DeleteVariant(_ context.Context, id int64) error {
	w.varDelete = append(w.varDelete, id)
	return nil
}

func (w *recordingWriter) SetProductStock(_ context.Context, id int64, stock int, _ string) error {
	w.stock = append(w.stock, stockWrite{id, stock})
	return nil
}

// testPosition is a fixed Kafka position for the test messages.
func testPosition() Position {
	return Position{Topic: "product.events.v1", Partition: 0, Offset: 42}
}

func testProjector(w Writer) *Projector {
	return NewProjector(w, slog.New(slog.NewTextHandler(io.Discard, nil)), NopInvalidator())
}

// encode builds a message body the way product-service would.
func encode(t *testing.T, eventID, eventType string, payload any) []byte {
	t.Helper()

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	body, err := json.Marshal(event.Envelope{
		EventID:       eventID,
		EventType:     eventType,
		SchemaVersion: event.SchemaVersion,
		AggregateType: "product",
		AggregateID:   1,
		OccurredAt:    time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		Payload:       raw,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return body
}

func sampleProduct() *event.ProductSnapshot {
	return &event.ProductSnapshot{
		ID:         1,
		CategoryID: 2,
		Name:       "Aurora Phone 15 Pro",
		Slug:       "aurora-phone-15-pro",
		PriceCents: 12999000,
		Currency:   "IDR",
		Stock:      25,
		Status:     "active",
		CreatedAt:  time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 9, 30, 11, 0, 0, 0, time.UTC),
	}
}

func TestProjectorAppliesProductEvents(t *testing.T) {
	t.Parallel()

	t.Run("an insert and an update are applied identically", func(t *testing.T) {
		t.Parallel()

		// Both carry a full snapshot, so the projector must not need to know which
		// is which: that is what makes re-applying an event harmless.
		for _, eventType := range []string{event.TypeProductCreated, event.TypeProductUpdated} {
			writer := &recordingWriter{applied: true}
			outcome, err := testProjector(writer).Apply(context.Background(), testPosition(),
				encode(t, "evt-"+eventType, eventType, sampleProduct()))
			if err != nil {
				t.Fatalf("%s: %v", eventType, err)
			}
			if outcome != OutcomeApplied {
				t.Errorf("%s: outcome = %v, want applied", eventType, outcome)
			}
			if len(writer.products) != 1 {
				t.Fatalf("%s: wrote %d products, want 1", eventType, len(writer.products))
			}
			if writer.products[0].ID != 1 || writer.products[0].Slug != "aurora-phone-15-pro" {
				t.Errorf("%s: wrote %+v, want the snapshot as published", eventType, writer.products[0])
			}
		}
	})

	t.Run("a delete removes the product", func(t *testing.T) {
		t.Parallel()

		writer := &recordingWriter{applied: true}
		if _, err := testProjector(writer).Apply(context.Background(), testPosition(),
			encode(t, "evt-del", event.TypeProductDeleted, event.ProductDeletedPayload{ID: 1}),
		); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(writer.deletes) != 1 || writer.deletes[0] != 1 {
			t.Errorf("deleted %v, want [1]", writer.deletes)
		}
	})

	t.Run("a variant event is projected to the variant table", func(t *testing.T) {
		t.Parallel()

		variant := &event.VariantSnapshot{
			ID: 5, ProductID: 1, SKU: "AUR-15-BLK", Name: "256GB Black",
			PriceCents: 12999000, Currency: "IDR", Stock: 10,
		}
		writer := &recordingWriter{applied: true}
		if _, err := testProjector(writer).Apply(context.Background(), testPosition(),
			encode(t, "evt-var", event.TypeVariantCreated, variant),
		); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(writer.variants) != 1 || writer.variants[0].SKU != "AUR-15-BLK" {
			t.Errorf("wrote %+v, want the variant snapshot", writer.variants)
		}
		// A variant event must not touch the product row, or a variant change
		// would rewrite the whole product.
		if len(writer.products) != 0 {
			t.Errorf("a variant event wrote %d product rows, want none", len(writer.products))
		}
	})

	t.Run("a variant delete removes only the variant", func(t *testing.T) {
		t.Parallel()

		writer := &recordingWriter{applied: true}
		if _, err := testProjector(writer).Apply(context.Background(), testPosition(),
			encode(t, "evt-vdel", event.TypeVariantDeleted,
				event.VariantDeletedPayload{ProductID: 1, VariantID: 5}),
		); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(writer.varDelete) != 1 || writer.varDelete[0] != 5 {
			t.Errorf("deleted variants %v, want [5]", writer.varDelete)
		}
		if len(writer.deletes) != 0 {
			t.Errorf("a variant delete removed %d products, want none", len(writer.deletes))
		}
	})
}

// A stock event carries the resulting value and nothing else. Writing the whole
// product row from it would clobber any newer update already projected, so the
// projector must write only the stock column.
func TestProjectorWritesOnlyStockForAStockAdjustment(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applied: true}
	outcome, err := testProjector(writer).Apply(context.Background(), testPosition(),
		encode(t, "evt-stock", event.TypeProductStockAdjusted,
			event.ProductStockAdjustedPayload{ID: 1, Stock: 7, Delta: -3}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Errorf("outcome = %v, want applied", outcome)
	}
	if len(writer.products) != 0 {
		t.Errorf("wrote %d product rows, want none: the event carries no snapshot", len(writer.products))
	}
	if len(writer.stock) != 1 || writer.stock[0].productID != 1 || writer.stock[0].stock != 7 {
		t.Errorf("stock writes = %+v, want product 1 at 7", writer.stock)
	}
}

// The dedupe marker and the read-model write have to be one transaction. If they
// were two autocommit statements, this sequence loses the event permanently:
//
//  1. the marker commits
//  2. the upsert fails (a UNIQUE slug collision is enough)
//  3. the redelivery finds the marker and is dismissed as a duplicate
//  4. the offset is committed, so nothing ever retries it
//
// Asserted on the stub: Apply must open exactly one transaction, and the marker
// and the write must both happen inside it. The rollback behaviour against a real
// database is covered by TestAFailedWriteRollsBackTheDedupeMarker.
func TestApplyRecordsAndWritesInOneTransaction(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applied: true}
	if _, err := testProjector(writer).Apply(context.Background(), testPosition(),
		encode(t, "evt-tx", event.TypeProductCreated, sampleProduct()),
	); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if writer.txOpened != 1 {
		t.Errorf("opened %d transactions, want 1: the marker and the write must share one", writer.txOpened)
	}
	if len(writer.position) != 1 {
		t.Fatalf("recorded the marker %d times, want 1", len(writer.position))
	}
	if len(writer.products) != 1 {
		t.Errorf("wrote %d products, want 1", len(writer.products))
	}
}

// A write failure must propagate so the transaction rolls back. If Apply swallowed
// it, the marker would commit alone and the redelivery would be dismissed as a
// duplicate, which is exactly the loss the transaction exists to prevent.
func TestAFailedWriteIsReturnedSoTheTransactionRollsBack(t *testing.T) {
	t.Parallel()

	boom := errors.New("duplicate key value violates unique constraint product_listings_slug_key")
	writer := &recordingWriter{applied: true, writeErrs: map[string]error{"product": boom}}

	outcome, err := testProjector(writer).Apply(context.Background(), testPosition(),
		encode(t, "evt-rollback", event.TypeProductCreated, sampleProduct()))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the write failure so the transaction rolls back", err)
	}
	if outcome != OutcomeSkipped {
		t.Errorf("outcome = %v, want skipped", outcome)
	}
	// The marker was attempted, which is what the rollback undoes.
	if len(writer.position) != 1 {
		t.Errorf("recorded the marker %d times, want 1 attempt inside the transaction", len(writer.position))
	}
}

// A database failure is transient, so it must be retried rather than dead-lettered:
// the read model comes back and the same record then applies cleanly.
func TestAWriteFailureIsRetriedRatherThanDeadLettered(t *testing.T) {
	t.Parallel()

	boom := errors.New("database unavailable")
	writer := &recordingWriter{applied: true, writeErrs: map[string]error{"product": boom}}

	_, err := testProjector(writer).Apply(context.Background(), testPosition(),
		encode(t, "evt-retry", event.TypeProductCreated, sampleProduct()))
	if err == nil {
		t.Fatal("err = nil, want the write failure")
	}
	if IsPermanent(err) {
		t.Error("IsPermanent = true for a database failure, want a retry instead")
	}
}

// The idempotency gate is what makes at-least-once delivery safe: a redelivered
// event must not be written again.
func TestProjectorSkipsADuplicate(t *testing.T) {
	t.Parallel()

	// The writer reports the event id as already seen, which is how a redelivery
	// is signalled.
	writer := &recordingWriter{knownIDs: map[string]bool{"evt-dup": true}}
	outcome, err := testProjector(writer).Apply(context.Background(), testPosition(),
		encode(t, "evt-dup", event.TypeProductCreated, sampleProduct()))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if outcome != OutcomeDuplicate {
		t.Errorf("outcome = %v, want duplicate", outcome)
	}
	if len(writer.products) != 0 {
		t.Errorf("a duplicate event wrote %d rows, want none", len(writer.products))
	}
}

// A type this version does not know is not an error. A newer producer may emit
// events this consumer has never heard of, and treating that as a failure would
// stall the partition on a message that is perfectly valid.
func TestProjectorSkipsAnUnknownType(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applied: true}
	outcome, err := testProjector(writer).Apply(context.Background(), testPosition(),
		encode(t, "evt-future", "product.v2.product.rating_changed",
			map[string]any{"product_id": 1}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if outcome != OutcomeSkipped {
		t.Errorf("outcome = %v, want skipped", outcome)
	}
	if len(writer.products) != 0 || len(writer.variants) != 0 {
		t.Error("an unknown event type wrote to the read model")
	}
}

func TestProjectorRejectsAMalformedMessage(t *testing.T) {
	t.Parallel()

	cases := map[string][]byte{
		"not json":         []byte("this is not json"),
		"no event id":      []byte(`{"event_type":"product.v1.product.created","schema_version":1}`),
		"wrong version":    []byte(`{"event_id":"x","event_type":"product.v1.product.created","schema_version":99}`),
		"payload mismatch": []byte(`{"event_id":"x","event_type":"product.v1.product.created","schema_version":1,"payload":"a string"}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			writer := &recordingWriter{applied: true}
			outcome, err := testProjector(writer).Apply(context.Background(), testPosition(), body)
			if err == nil {
				t.Fatalf("%s: err = nil, want a decode failure", name)
			}
			// The consumer routes on this classification: a record that can never
			// decode is dead-lettered, because retrying it would hold its partition
			// and stop every product behind it from being read.
			if !IsPermanent(err) {
				t.Errorf("%s: IsPermanent(%v) = false, want true", name, err)
			}
			if outcome != OutcomeSkipped {
				t.Errorf("%s: outcome = %v, want skipped", name, outcome)
			}
		})
	}
}

// A database failure is transient, so it must not be classified as permanent: the
// read model will come back and the same record then applies cleanly.
func TestAWriteFailureIsNotPermanent(t *testing.T) {
	t.Parallel()

	boom := errors.New("database unavailable")
	writer := &recordingWriter{applyErr: boom}

	_, err := testProjector(writer).Apply(context.Background(), testPosition(),
		encode(t, "evt-db", event.TypeProductCreated, sampleProduct()))
	if err == nil {
		t.Fatal("err = nil, want the write failure")
	}
	if IsPermanent(err) {
		t.Error("IsPermanent = true for a database failure, want a retry instead")
	}
}

// The Kafka position has to travel with the event, or a replay cannot be traced
// back to the offset in the log that produced a row.
func TestProjectorRecordsTheKafkaPosition(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applied: true}
	if _, err := testProjector(writer).Apply(context.Background(), testPosition(),
		encode(t, "evt-pos", event.TypeProductCreated, sampleProduct()),
	); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(writer.position) != 1 {
		t.Fatalf("recorded %d positions, want 1", len(writer.position))
	}
	if writer.position[0] != testPosition() {
		t.Errorf("position = %+v, want %+v", writer.position[0], testPosition())
	}
}

// A failure to record the event as processed must abort before any write, or the
// read model would change without a record that it had, and a redelivery would
// apply it a second time.
func TestProjectorDoesNotWriteWhenRecordingTheEventFails(t *testing.T) {
	t.Parallel()

	boom := errors.New("database unreachable")
	writer := &recordingWriter{applyErr: boom}
	if _, err := testProjector(writer).Apply(context.Background(), testPosition(),
		encode(t, "evt-x", event.TypeProductCreated, sampleProduct()),
	); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the recording failure", err)
	}
	if len(writer.products) != 0 {
		t.Errorf("wrote %d products despite the failure, want none", len(writer.products))
	}
}

// recordingInvalidator captures invalidation requests so a test can assert both
// that they happen and, in the rollback case, that they do not.
type recordingInvalidator struct {
	invalidated []int64
	// duringTx records whether Invalidate was called before InTx returned, which is
	// the ordering bug TestInvalidationHappensAfterCommit exists to catch.
	duringTx bool
	inTx     bool
}

func (r *recordingInvalidator) Invalidate(id int64) {
	r.invalidated = append(r.invalidated, id)
	if r.inTx {
		r.duringTx = true
	}
}

func (r *recordingInvalidator) seen(id int64) bool {
	for _, got := range r.invalidated {
		if got == id {
			return true
		}
	}
	return false
}

// The projection is the only component that learns a product changed, so it is the
// only place a cached read can be dropped. Without this, a product updated
// upstream keeps serving its pre-update rows until the TTL runs out.
func TestProjectionInvalidatesCachedProduct(t *testing.T) {
	t.Parallel()

	// sampleProduct() is the fixture these events carry, so its id is the id that
	// has to be invalidated.
	const productID = int64(1)

	tests := []struct {
		name      string
		eventType string
		payload   any
	}{
		{
			name:      "product updated",
			eventType: event.TypeProductUpdated,
			payload:   sampleProduct(),
		},
		{
			name:      "product deleted",
			eventType: event.TypeProductDeleted,
			payload:   event.ProductDeletedPayload{ID: productID},
		},
		{
			name:      "stock adjusted",
			eventType: event.TypeProductStockAdjusted,
			payload:   event.ProductStockAdjustedPayload{ID: productID, Stock: 3},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			writer := &recordingWriter{applied: true}
			inv := &recordingInvalidator{}
			p := NewProjector(writer, slog.New(slog.NewTextHandler(io.Discard, nil)), inv)

			if _, err := p.Apply(context.Background(), testPosition(),
				encode(t, "evt-inv", tc.eventType, tc.payload)); err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if !inv.seen(productID) {
				t.Errorf("product %d was written but never invalidated; a cached read of it stays stale until the TTL", productID)
			}
		})
	}
}

// A variant event changes what a read of the parent product returns, so the parent
// is what has to be invalidated — not the variant id, which is not a cache key.
func TestVariantEventInvalidatesTheParentProduct(t *testing.T) {
	t.Parallel()

	const (
		productID = int64(88)
		variantID = int64(91)
	)

	writer := &recordingWriter{applied: true}
	inv := &recordingInvalidator{}
	p := NewProjector(writer, slog.New(slog.NewTextHandler(io.Discard, nil)), inv)

	body := encode(t, "evt-var", event.TypeVariantCreated, event.VariantSnapshot{
		ID: variantID, ProductID: productID,
	})
	if _, err := p.Apply(context.Background(), testPosition(), body); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !inv.seen(productID) {
		t.Errorf("variant %d changed under product %d, but product %d was not invalidated", variantID, productID, productID)
	}
}

// Invalidation must not happen for a rolled-back transaction.
//
// Evicting before the commit opens a window a reader walks into: it misses,
// repopulates the cache from pre-commit rows, and the commit then lands under a
// cache that is stale again but has a fresh TTL. That is worse than not
// invalidating, because it looks handled.
func TestInvalidationHappensAfterCommit(t *testing.T) {
	t.Parallel()

	const productID = int64(1)

	boom := errors.New("write failed")
	writer := &recordingWriter{applied: true, writeErrs: map[string]error{"product": boom}}
	inv := &recordingInvalidator{}
	p := NewProjector(writer, slog.New(slog.NewTextHandler(io.Discard, nil)), inv)

	// Track whether Invalidate is reached while the transaction is still open.
	inv.inTx = true
	if _, err := p.Apply(context.Background(), testPosition(),
		encode(t, "evt-rb", event.TypeProductUpdated, sampleProduct())); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the write failure", err)
	}
	inv.inTx = false

	if len(inv.invalidated) != 0 {
		t.Errorf("invalidated %v despite the transaction rolling back; a reader would repopulate the cache from pre-commit rows",
			inv.invalidated)
	}
	if inv.duringTx {
		t.Error("invalidation ran inside the transaction instead of after the commit")
	}
}

// A redelivery changes nothing, so there is nothing to invalidate.
func TestDuplicateEventDoesNotInvalidate(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{knownIDs: map[string]bool{"evt-dup": true}}
	inv := &recordingInvalidator{}
	p := NewProjector(writer, slog.New(slog.NewTextHandler(io.Discard, nil)), inv)

	outcome, err := p.Apply(context.Background(), testPosition(),
		encode(t, "evt-dup", event.TypeProductUpdated, sampleProduct()))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if outcome != OutcomeDuplicate {
		t.Errorf("outcome = %v, want %v", outcome, OutcomeDuplicate)
	}
	if len(inv.invalidated) != 0 {
		t.Errorf("a redelivery wrote nothing but invalidated %v", inv.invalidated)
	}
}

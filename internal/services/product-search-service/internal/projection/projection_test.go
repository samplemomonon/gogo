package projection

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"product-search-service/internal/event"
)

// recordingWriter captures what the projector asked the index to do.
//
// It implements Writer, so the writes it records are exactly the writes that would
// have run inside a real transaction.
type recordingWriter struct {
	applied  bool
	applyErr error
	knownIDs map[string]bool

	products []*event.ProductSnapshot
	removes  []int64
	stock    []stockWrite
	position []Position

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
// the work stands.
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

func (w *recordingWriter) IndexProduct(_ context.Context, s *event.ProductSnapshot, _ string) error {
	if err := w.writeErrs["product"]; err != nil {
		return err
	}
	w.products = append(w.products, s)
	return nil
}

func (w *recordingWriter) RemoveProduct(_ context.Context, id int64) error {
	w.removes = append(w.removes, id)
	return nil
}

func (w *recordingWriter) SetProductStock(_ context.Context, productID int64, stock int, _ string) error {
	w.stock = append(w.stock, stockWrite{productID, stock})
	return nil
}

// testPosition is a fixed Kafka position for the test messages.
func testPosition() Position {
	return Position{Topic: "product.events.v1", Partition: 0, Offset: 42}
}

var testTime = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func newTestProjector(w Writer) *Projector {
	return NewProjector(w, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// message builds a real event body, so the tests exercise the same decoding the
// consumer does rather than a hand-assembled struct.
func message(t *testing.T, eventType string, aggregateID int64, payload any) []byte {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"event_id":       "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		"event_type":     eventType,
		"schema_version": event.SchemaVersion,
		"aggregate_type": "product",
		"aggregate_id":   aggregateID,
		"occurred_at":    testTime,
		"payload":        payload,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

func productPayload(id int64) event.ProductSnapshot {
	return event.ProductSnapshot{
		ID: id, CategoryID: 1, Name: "Aurora Phone", Slug: "aurora-phone",
		PriceCents: 1000, Currency: "IDR", Stock: 1, Status: "active",
		CreatedAt: testTime, UpdatedAt: testTime,
	}
}

func TestApplyIndexesTheProductEvents(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		eventType string
		payload   any
		// wantIndexed is false for the event types that must not touch the index.
		wantIndexed bool
	}{
		"created": {event.TypeProductCreated, productPayload(1), true},
		"updated": {event.TypeProductUpdated, productPayload(1), true},
		"deleted": {event.TypeProductDeleted, event.ProductDeletedPayload{ID: 1}, false},
		"stock": {
			event.TypeProductStockAdjusted,
			event.ProductStockAdjustedPayload{ID: 1, Stock: 7, Delta: 2},
			false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			writer := &recordingWriter{applied: true}
			outcome, err := newTestProjector(writer).Apply(context.Background(),
				testPosition(), message(t, tc.eventType, 1, tc.payload))
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if outcome != OutcomeApplied {
				t.Fatalf("outcome = %v, want applied", outcome)
			}
			if got := len(writer.products) == 1; got != tc.wantIndexed {
				t.Errorf("indexed = %v, want %v", got, tc.wantIndexed)
			}
		})
	}
}

// A delete has to reach the index, or a removed product stays findable. This is
// the one event whose only effect is a removal.
func TestApplyRemovesADeletedProduct(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applied: true}
	if _, err := newTestProjector(writer).Apply(context.Background(), testPosition(),
		message(t, event.TypeProductDeleted, 42, event.ProductDeletedPayload{ID: 42}),
	); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(writer.removes) != 1 || writer.removes[0] != 42 {
		t.Errorf("removes = %v, want [42]", writer.removes)
	}
	if len(writer.products) != 0 {
		t.Errorf("a delete also indexed %d products, want 0", len(writer.products))
	}
}

// A stock adjustment writes only the stock column, and it must not be applied as a
// whole-product upsert. The event carries no name, price or category, so writing
// those from it would overwrite newer values with nothing.
func TestApplyWritesOnlyStockForAStockAdjustment(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applied: true}
	if _, err := newTestProjector(writer).Apply(context.Background(), testPosition(),
		message(t, event.TypeProductStockAdjusted, 7,
			event.ProductStockAdjustedPayload{ID: 7, Stock: 42, Delta: -1}),
	); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(writer.stock) != 1 || writer.stock[0].productID != 7 || writer.stock[0].stock != 42 {
		t.Errorf("stock writes = %+v, want a single write of 42 to product 7", writer.stock)
	}
	if len(writer.products) != 0 {
		t.Errorf("a stock event also ran a full upsert, want 0")
	}
}

// A search result is a product, not a product with its variants, so variant events
// must reach the projector, be recognised, and then do nothing. They are still
// decoded rather than skipped blindly, which is what keeps an unparseable variant
// payload a dead-lettered failure instead of a silent no-op.
func TestVariantEventsAreDecodedAndThenIgnored(t *testing.T) {
	t.Parallel()

	variant := event.VariantSnapshot{
		ID: 5, ProductID: 1, SKU: "AUR-15-BLK", Name: "Black",
		PriceCents: 1000, Currency: "IDR", Stock: 1,
		CreatedAt: testTime, UpdatedAt: testTime,
	}

	for _, eventType := range []string{event.TypeVariantCreated, event.TypeVariantUpdated} {
		t.Run(eventType, func(t *testing.T) {
			t.Parallel()

			writer := &recordingWriter{applied: true}
			outcome, err := newTestProjector(writer).Apply(context.Background(),
				testPosition(), message(t, eventType, 5, variant))
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if outcome != OutcomeApplied {
				t.Errorf("outcome = %v, want applied: the event was understood", outcome)
			}
			if len(writer.products) != 0 || len(writer.stock) != 0 || len(writer.removes) != 0 {
				t.Errorf("a variant event wrote to the index: %d products, %d stock, %d removes",
					len(writer.products), len(writer.stock), len(writer.removes))
			}
		})
	}

	t.Run(event.TypeVariantDeleted, func(t *testing.T) {
		t.Parallel()

		writer := &recordingWriter{applied: true}
		if _, err := newTestProjector(writer).Apply(context.Background(), testPosition(),
			message(t, event.TypeVariantDeleted, 5,
				event.VariantDeletedPayload{ProductID: 1, VariantID: 5}),
		); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if len(writer.products) != 0 || len(writer.stock) != 0 || len(writer.removes) != 0 {
			t.Error("a variant delete wrote to the index")
		}
	})
}

// The dedupe marker and the index write have to be one transaction. If they were
// two autocommit statements, this sequence loses the event permanently:
//
//  1. the marker commits
//  2. the index write fails
//  3. the redelivery finds the marker and is dismissed as a duplicate
//  4. the offset is committed, so nothing ever retries it
//
// Asserted on the stub: Apply must open exactly one transaction, and the marker
// and the write must both happen inside it. Rollback against a real database is
// covered by TestAFailedWriteRollsBackTheDedupeMarker in the repository package.
func TestApplyRecordsAndWritesInOneTransaction(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applied: true}
	if _, err := newTestProjector(writer).Apply(context.Background(),
		testPosition(), message(t, event.TypeProductCreated, 1, productPayload(1)),
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
		t.Errorf("wrote %d documents, want 1", len(writer.products))
	}
}

// A write failure must propagate so the transaction rolls back. If Apply swallowed
// it, the marker would commit alone and the redelivery would be dismissed as a
// duplicate, which is exactly the loss the transaction exists to prevent.
func TestAFailedWriteIsReturnedSoTheTransactionRollsBack(t *testing.T) {
	t.Parallel()

	boom := errors.New("index write failed")
	writer := &recordingWriter{applied: true, writeErrs: map[string]error{"product": boom}}

	outcome, err := newTestProjector(writer).Apply(context.Background(),
		testPosition(), message(t, event.TypeProductCreated, 1, productPayload(1)))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the write failure so the transaction rolls back", err)
	}
	if outcome != OutcomeSkipped {
		t.Errorf("outcome = %v, want skipped", outcome)
	}
	if len(writer.position) != 1 {
		t.Errorf("recorded the marker %d times, want 1 attempt inside the transaction", len(writer.position))
	}
}

// Deduplication happens before any write, so a redelivery costs one lookup and
// nothing else.
func TestDuplicateEventsAreSkipped(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{
		knownIDs: map[string]bool{"3f2504e0-4f89-41d3-9a0c-0305e82c3301": true},
	}
	outcome, err := newTestProjector(writer).Apply(context.Background(),
		testPosition(), message(t, event.TypeProductCreated, 1, productPayload(1)))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if outcome != OutcomeDuplicate {
		t.Errorf("outcome = %v, want duplicate", outcome)
	}
	if len(writer.products) != 0 {
		t.Errorf("a duplicate event indexed %d products, want 0", len(writer.products))
	}
}

// An event type from a newer producer is skipped but NOT recorded as processed.
// Marking it would discard it permanently if this service were ever rolled back to
// a version that understands it.
func TestUnknownEventTypesAreSkippedWithoutBeingRecorded(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applied: true}
	outcome, err := newTestProjector(writer).Apply(context.Background(), testPosition(),
		message(t, "product.v1.product.translated", 1, map[string]any{}))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if outcome != OutcomeSkipped {
		t.Errorf("outcome = %v, want skipped", outcome)
	}
	if len(writer.position) != 0 {
		t.Errorf("an unknown event was recorded as processed: %+v", writer.position)
	}
}

// A message this version can never interpret is permanent. Retrying it produces the
// same failure forever and would hold its partition, so the consumer has to be able
// to tell this apart from a database that is merely down.
func TestUndecodableMessagesArePermanent(t *testing.T) {
	t.Parallel()

	cases := map[string][]byte{
		"not json":            []byte("{not json"),
		"no event id":         []byte(`{"event_type":"product.v1.product.created","schema_version":1,"aggregate_id":1}`),
		"future schema":       []byte(`{"event_id":"x","event_type":"product.v1.product.created","schema_version":99,"aggregate_id":1}`),
		"payload wrong shape": []byte(`{"event_id":"x","event_type":"product.v1.product.created","schema_version":1,"aggregate_id":1,"payload":"not an object"}`),
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := newTestProjector(&recordingWriter{applied: true}).
				Apply(context.Background(), testPosition(), body)
			if err == nil {
				t.Fatal("err = nil, want a failure")
			}
			if !IsPermanent(err) {
				t.Errorf("permanent = false, want true: %v", err)
			}
		})
	}
}

// A database that is down is transient, and retrying is the right response: the
// index comes back and the same record applies cleanly.
func TestDatabaseFailuresAreTransient(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applyErr: errors.New("connection refused")}
	_, err := newTestProjector(writer).Apply(context.Background(),
		testPosition(), message(t, event.TypeProductCreated, 1, productPayload(1)))
	if err == nil {
		t.Fatal("err = nil, want a failure")
	}
	if IsPermanent(err) {
		t.Error("a database failure was reported as permanent, so it would be dead-lettered")
	}
}

// The Kafka position has to be recorded, or a replay cannot be traced back to the
// offset that produced a row.
func TestPositionIsRecordedWithTheEvent(t *testing.T) {
	t.Parallel()

	writer := &recordingWriter{applied: true}
	if _, err := newTestProjector(writer).Apply(context.Background(), testPosition(),
		message(t, event.TypeProductCreated, 1, productPayload(1)),
	); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(writer.position) != 1 {
		t.Fatalf("recorded %d positions, want 1", len(writer.position))
	}
	got := writer.position[0]
	if got.Topic != "product.events.v1" || got.Partition != 0 || got.Offset != 42 {
		t.Errorf("position = %+v, want product.events.v1/0/42", got)
	}
}

func TestOutcomeString(t *testing.T) {
	t.Parallel()

	cases := map[Outcome]string{
		OutcomeApplied:   "applied",
		OutcomeDuplicate: "duplicate",
		OutcomeSkipped:   "skipped",
		Outcome(99):      "unknown",
	}
	for outcome, want := range cases {
		if got := outcome.String(); got != want {
			t.Errorf("Outcome(%d).String() = %q, want %q", outcome, got, want)
		}
	}
}

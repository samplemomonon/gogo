// Package messaging (internal test) pins the batch-application policy, which
// is the heart of the at-least-once design: how a batch is stopped at a
// transient failure, how a permanent one is dead-lettered without blocking its
// partition, and what travels with a rejected record.
//
// The tests construct the Consumer directly, because the behaviour under test
// is unexported and deliberately independent of the Kafka client: nothing in
// this file touches a broker.
package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"product-search-service/internal/event"
	"product-search-service/internal/projection"
)

// stubHandler applies records according to a scripted result per offset.
type stubHandler struct {
	results map[int64]struct {
		outcome projection.Outcome
		err     error
	}
	applied []projection.Position
}

func (h *stubHandler) Apply(_ context.Context, position projection.Position, _ []byte) (projection.Outcome, error) {
	h.applied = append(h.applied, position)
	result, ok := h.results[position.Offset]
	if !ok {
		return projection.OutcomeApplied, nil
	}
	return result.outcome, result.err
}

// stubPublisher records what was dead-lettered.
type stubPublisher struct {
	published []struct {
		topic   string
		key     []byte
		value   []byte
		headers map[string]string
	}
	err error
}

func (p *stubPublisher) Publish(_ context.Context, topic string, key, value []byte, headers map[string]string) error {
	p.published = append(p.published, struct {
		topic   string
		key     []byte
		value   []byte
		headers map[string]string
	}{topic, key, value, headers})
	return p.err
}

func (p *stubPublisher) Close(context.Context) error { return nil }

func record(topic string, partition int32, offset int64, body string) *kgo.Record {
	return &kgo.Record{
		Topic:     topic,
		Partition: partition,
		Offset:    offset,
		Key:       []byte(fmt.Sprintf("key-%d", offset)),
		Value:     []byte(body),
	}
}

func testConsumer(handler Handler, publisher Publisher, deadLetterTopic string) *Consumer {
	return &Consumer{
		handler:   handler,
		publisher: publisher,
		cfg:       ConsumerConfig{DeadLetterTopic: deadLetterTopic},
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// TestApplyRecordsContinuesPastSuccessAndDuplicates pins the happy batch: an
// applied record and a duplicate are both success, the whole batch may be
// committed, and each record reaches the handler with its own Kafka position.
func TestApplyRecordsContinuesPastSuccessAndDuplicates(t *testing.T) {
	t.Parallel()

	handler := &stubHandler{results: map[int64]struct {
		outcome projection.Outcome
		err     error
	}{
		1: {outcome: projection.OutcomeDuplicate},
	}}
	consumer := testConsumer(handler, nil, "")

	records := []*kgo.Record{
		record("product.events.v1", 2, 0, "first"),
		record("product.events.v1", 2, 1, "duplicate"),
		record("product.events.v1", 2, 2, "third"),
	}

	handled, err := consumer.applyRecords(context.Background(), records)
	if err != nil {
		t.Fatalf("applyRecords: %v", err)
	}
	if handled != len(records) {
		t.Errorf("handled = %d, want all %d", handled, len(records))
	}
	if len(handler.applied) != 3 {
		t.Fatalf("handler saw %d records, want 3", len(handler.applied))
	}
	if got := handler.applied[1]; got.Topic != "product.events.v1" || got.Partition != 2 || got.Offset != 1 {
		t.Errorf("position = %+v, want the record's own coordinates", got)
	}
}

// TestApplyRecordsStopsAtATransientFailure pins the boundary of the
// committable prefix: the batch stops at the first transient failure and the
// records behind it are not applied, because a later event may depend on the
// one that failed and the batch is re-read regardless.
func TestApplyRecordsStopsAtATransientFailure(t *testing.T) {
	t.Parallel()

	transient := errors.New("index: connection refused")
	handler := &stubHandler{results: map[int64]struct {
		outcome projection.Outcome
		err     error
	}{
		1: {err: transient},
	}}
	consumer := testConsumer(handler, nil, "")

	records := []*kgo.Record{
		record("product.events.v1", 0, 0, "a"),
		record("product.events.v1", 0, 1, "b"),
		record("product.events.v1", 0, 2, "c"),
	}

	handled, err := consumer.applyRecords(context.Background(), records)
	if !errors.Is(err, transient) {
		t.Fatalf("error = %v, want the transient failure", err)
	}
	if handled != 1 {
		t.Errorf("handled = %d, want only the prefix before the failure (1)", handled)
	}
	if len(handler.applied) != 2 {
		t.Errorf("handler saw %d records; it must not apply anything past the failure", len(handler.applied))
	}
}

// TestApplyRecordsDeadLettersPermanentFailures pins the unblocking rule: a
// record that can never be applied is dead-lettered and skipped, the batch
// continues, and the whole batch may be committed — otherwise one poison
// message would freeze every product behind it.
func TestApplyRecordsDeadLettersPermanentFailures(t *testing.T) {
	t.Parallel()

	poison := event.Permanent(errors.New("decode product.v1.product.created payload: bad"))
	handler := &stubHandler{results: map[int64]struct {
		outcome projection.Outcome
		err     error
	}{
		1: {err: poison},
	}}
	publisher := &stubPublisher{}
	consumer := testConsumer(handler, publisher, "product.events.dlq.v1")

	records := []*kgo.Record{
		record("product.events.v1", 3, 0, "a"),
		record("product.events.v1", 3, 1, "poison"),
		record("product.events.v1", 3, 2, "c"),
	}

	handled, err := consumer.applyRecords(context.Background(), records)
	if err != nil {
		t.Fatalf("applyRecords must not fail on a permanent error: %v", err)
	}
	if handled != len(records) {
		t.Errorf("handled = %d, want the whole batch %d", handled, len(records))
	}
	if len(handler.applied) != 3 {
		t.Errorf("handler saw %d records, want the batch to continue past the poison message", len(handler.applied))
	}
	if len(publisher.published) != 1 {
		t.Fatalf("published %d dead letters, want 1", len(publisher.published))
	}

	dead := publisher.published[0]
	if dead.topic != "product.events.dlq.v1" {
		t.Errorf("dead-letter topic = %q", dead.topic)
	}
	if string(dead.key) != "key-1" || string(dead.value) != "poison" {
		t.Errorf("dead letter key/value = %q/%q, want the original record", dead.key, dead.value)
	}
	for name, want := range map[string]string{
		"original_topic":     "product.events.v1",
		"original_partition": "3",
		"original_offset":    "1",
	} {
		if got := dead.headers[name]; got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if dead.headers["error"] == "" {
		t.Error("dead letter carries no error header")
	}
}

// TestDeadLetterCarriesProducerHeaders pins that the headers the producer set
// on the original record travel with the dead letter, so a trace id is not
// lost at the point where it is most needed.
func TestDeadLetterCarriesProducerHeaders(t *testing.T) {
	t.Parallel()

	publisher := &stubPublisher{}
	consumer := testConsumer(&stubHandler{}, publisher, "dlq")

	poisoned := record("product.events.v1", 0, 0, "body")
	poisoned.Headers = []kgo.RecordHeader{
		{Key: "traceparent", Value: []byte("00-abc-def-01")},
	}
	consumer.deadLetter(context.Background(), poisoned, errors.New("boom"))

	if len(publisher.published) != 1 {
		t.Fatalf("published %d dead letters, want 1", len(publisher.published))
	}
	if got := publisher.published[0].headers["traceparent"]; got != "00-abc-def-01" {
		t.Errorf("traceparent header = %q, want the original value", got)
	}
}

// TestDeadLetterIsSkippedWhenDisabledOrUnpublishable pins the two quiet
// paths: without a dead-letter topic or publisher the record is logged and
// dropped, and a publish failure is not allowed to abort the batch — the
// offset still advances, because holding the partition is worse than losing
// one record that is already logged.
func TestDeadLetterIsSkippedWhenDisabledOrUnpublishable(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		topic    string
		pub      Publisher
		expected int
	}{
		{"no dead-letter topic", "", &stubPublisher{}, 0},
		{"no publisher", "dlq", nil, 0},
		{"publish fails", "dlq", &stubPublisher{err: errors.New("kafka down")}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			consumer := testConsumer(&stubHandler{}, tt.pub, tt.topic)
			// Must not panic and must return; the caller commits regardless.
			consumer.deadLetter(context.Background(), record("t", 0, 0, "v"), errors.New("boom"))

			if pub, ok := tt.pub.(*stubPublisher); ok && len(pub.published) != tt.expected {
				t.Errorf("published %d records, want %d", len(pub.published), tt.expected)
			}
		})
	}
}

// TestApplyRecordsOnAnEmptyBatch pins the trivial case: nothing to apply is
// success, so an idle poll never looks like a failure.
func TestApplyRecordsOnAnEmptyBatch(t *testing.T) {
	t.Parallel()

	consumer := testConsumer(&stubHandler{}, nil, "")

	handled, err := consumer.applyRecords(context.Background(), nil)
	if err != nil || handled != 0 {
		t.Errorf("applyRecords(nil) = (%d, %v), want (0, nil)", handled, err)
	}
}

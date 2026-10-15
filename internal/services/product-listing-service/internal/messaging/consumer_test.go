package messaging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/event"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/projection"
)

// stubHandler applies or fails per offset, so the batch handling can be observed
// without a broker.
type stubHandler struct {
	// failAt maps an offset to the error the handler should return for it.
	failAt map[int64]error
	// permanentAt maps an offset to an error classified as unretriable.
	permanentAt map[int64]error

	// seen records the offsets the handler was asked to apply, in order.
	seen []int64
	// positions records the Kafka coordinates each record carried.
	positions []projection.Position
}

func (h *stubHandler) Apply(
	_ context.Context,
	position projection.Position,
	_ []byte,
) (projection.Outcome, error) {
	h.seen = append(h.seen, position.Offset)
	h.positions = append(h.positions, position)

	if err, ok := h.permanentAt[position.Offset]; ok {
		return projection.OutcomeSkipped, err
	}
	if err, ok := h.failAt[position.Offset]; ok {
		return projection.OutcomeSkipped, err
	}
	return projection.OutcomeApplied, nil
}

// recordingPublisher captures what was sent to the dead-letter topic.
type recordingPublisher struct {
	published []publishedRecord
	err       error
	closed    bool
}

type publishedRecord struct {
	topic   string
	key     []byte
	headers map[string]string
}

func (p *recordingPublisher) Publish(
	_ context.Context,
	topic string,
	key, _ []byte,
	headers map[string]string,
) error {
	if p.err != nil {
		return p.err
	}
	p.published = append(p.published, publishedRecord{topic: topic, key: key, headers: headers})
	return nil
}

func (p *recordingPublisher) Close(context.Context) error {
	p.closed = true
	return nil
}

func testConsumer(handler Handler, publisher Publisher, cfg ConsumerConfig) *Consumer {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The client is never dialled: applyRecords and deadLetter are exercised
	// directly, which is the behaviour under test and needs no broker.
	return &Consumer{
		publisher: publisher,
		handler:   handler,
		cfg:       cfg,
		log:       log,
	}
}

func record(offset int64) *kgo.Record {
	return &kgo.Record{
		Topic:     "product.events.v1",
		Partition: int32(offset % 3),
		Offset:    offset,
		Key:       []byte("42"),
		Value:     []byte(`{"event_id":"x"}`),
	}
}

// A transient failure must not stop the whole batch from being attempted, and the
// handled count must stop at the failure so the offset is not advanced past it.
func TestApplyRecordsStopsAtATransientFailure(t *testing.T) {
	t.Parallel()

	boom := errors.New("read model unavailable")
	handler := &stubHandler{failAt: map[int64]error{5: boom}}
	consumer := testConsumer(handler, &recordingPublisher{}, ConsumerConfig{
		DeadLetterTopic: "dlq",
	})

	records := []*kgo.Record{record(4), record(5), record(6)}
	handled, err := consumer.applyRecords(context.Background(), records)

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the transient failure", err)
	}
	if handled != 1 {
		t.Errorf("handled = %d, want 1: the offset must not advance past offset 5", handled)
	}
	if len(handler.seen) != 2 {
		t.Errorf("attempted %v, want it to stop at the failure", handler.seen)
	}
	if handler.seen[1] != 5 {
		t.Errorf("stopped at offset %d, want 5", handler.seen[1])
	}
}

// Every record handled means the whole batch may be committed.
func TestApplyRecordsHandlesEveryRecordOnSuccess(t *testing.T) {
	t.Parallel()

	handler := &stubHandler{}
	consumer := testConsumer(handler, &recordingPublisher{}, ConsumerConfig{DeadLetterTopic: "dlq"})

	handled, err := consumer.applyRecords(context.Background(), []*kgo.Record{record(1), record(2), record(3)})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if handled != 3 {
		t.Errorf("handled = %d, want 3", handled)
	}
}

// A record that can never be applied goes to the dead-letter topic and the batch
// still completes. Retrying it forever would stop every product behind it in that
// partition from ever being read.
func TestApplyRecordsDeadLettersAnUnrecoverableRecord(t *testing.T) {
	t.Parallel()

	// A malformed message, which the decoder classifies as unretriable.
	poison := event.Permanent(errors.New("malformed envelope"))
	handler := &stubHandler{permanentAt: map[int64]error{2: poison}}
	publisher := &recordingPublisher{}
	consumer := testConsumer(handler, publisher, ConsumerConfig{DeadLetterTopic: "product.events.v1.dlq"})

	handled, err := consumer.applyRecords(context.Background(),
		[]*kgo.Record{record(1), record(2), record(3)})
	if err != nil {
		t.Fatalf("err = %v, want the batch to complete despite the poison record", err)
	}
	if handled != 3 {
		t.Errorf("handled = %d, want 3", handled)
	}

	if len(publisher.published) != 1 {
		t.Fatalf("dead-lettered %d records, want 1", len(publisher.published))
	}
	published := publisher.published[0]
	if published.topic != "product.events.v1.dlq" {
		t.Errorf("topic = %q, want the dead-letter topic", published.topic)
	}
	// The original position has to travel with the record, or it cannot be found
	// again without replaying the whole topic.
	for header, want := range map[string]string{
		"original_topic":     "product.events.v1",
		"original_partition": "2",
		"original_offset":    "2",
	} {
		if published.headers[header] != want {
			t.Errorf("header %s = %q, want %q", header, published.headers[header], want)
		}
	}
	if published.headers["error"] == "" {
		t.Error("the error is not recorded, so the rejection cannot be diagnosed")
	}
}

// With no dead-letter topic configured the record is dropped after being logged,
// and the batch still completes so the partition keeps moving.
func TestDeadLetterIsOptional(t *testing.T) {
	t.Parallel()

	// A malformed message, which the decoder classifies as unretriable.
	poison := event.Permanent(errors.New("malformed envelope"))
	handler := &stubHandler{permanentAt: map[int64]error{1: poison}}
	publisher := &recordingPublisher{}
	consumer := testConsumer(handler, publisher, ConsumerConfig{DeadLetterTopic: ""})

	handled, err := consumer.applyRecords(context.Background(), []*kgo.Record{record(1), record(2)})
	if err != nil {
		t.Fatalf("err = %v, want the batch to complete", err)
	}
	if handled != 2 {
		t.Errorf("handled = %d, want 2", handled)
	}
	if len(publisher.published) != 0 {
		t.Errorf("published %d records, want none without a dead-letter topic", len(publisher.published))
	}
}

// A dead-letter publish that fails must not stall the batch either: the record is
// dropped, the offset advances, and the gap is visible in the logs.
func TestDeadLetterFailureDoesNotStallTheBatch(t *testing.T) {
	t.Parallel()

	// A malformed message, which the decoder classifies as unretriable.
	poison := event.Permanent(errors.New("malformed envelope"))
	handler := &stubHandler{permanentAt: map[int64]error{1: poison}}
	publisher := &recordingPublisher{err: errors.New("broker unreachable")}
	consumer := testConsumer(handler, publisher, ConsumerConfig{DeadLetterTopic: "dlq"})

	handled, err := consumer.applyRecords(context.Background(), []*kgo.Record{record(1), record(2)})
	if err != nil {
		t.Fatalf("err = %v, want the batch to complete", err)
	}
	if handled != 2 {
		t.Errorf("handled = %d, want 2", handled)
	}
}

// The Kafka coordinates must reach the projector, because it records them with
// the event to make a replay auditable.
func TestApplyRecordsPassesTheKafkaPosition(t *testing.T) {
	t.Parallel()

	handler := &stubHandler{}
	consumer := testConsumer(handler, &recordingPublisher{}, ConsumerConfig{DeadLetterTopic: "dlq"})

	if _, err := consumer.applyRecords(context.Background(), []*kgo.Record{record(7)}); err != nil {
		t.Fatalf("applyRecords: %v", err)
	}
	if len(handler.positions) != 1 {
		t.Fatalf("recorded %d positions, want 1", len(handler.positions))
	}
	position := handler.positions[0]
	if position.Topic != "product.events.v1" || position.Offset != 7 {
		t.Errorf("position = %+v, want topic product.events.v1 offset 7", position)
	}
}

func TestNewConsumerValidatesItsConfig(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := &stubHandler{}

	cases := map[string]ConsumerConfig{
		"no brokers":  {GroupID: "g", Topics: []string{"t"}},
		"no group":    {Brokers: []string{"b:9092"}, Topics: []string{"t"}},
		"no topics":   {Brokers: []string{"b:9092"}, GroupID: "g"},
		"all missing": {},
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewConsumer(cfg, handler, &recordingPublisher{}, log); err == nil {
				t.Error("err = nil, want the incomplete configuration rejected")
			}
		})
	}
}

package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"product-service/internal/event"
)

// stubRepo serves a fixed set of claimed events and records what the relay did
// with them. The claim returns the batch once and then nothing, which is what a
// real outbox does when the queue drains.
type stubRepo struct {
	batches [][]event.Record
	claimed []event.Record

	published       []int64
	publishedLeases []time.Time
	rescheduled     []reschedule
	failed          []failure
	err             error
}

type reschedule struct {
	id      int64
	retryAt time.Time
	cause   error
}

type failure struct {
	id    int64
	cause error
}

func (s *stubRepo) Claim(_ context.Context, _ int, _ time.Duration) ([]event.Record, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(s.batches) == 0 {
		return nil, nil
	}
	batch := s.batches[0]
	s.batches = s.batches[1:]
	s.claimed = append(s.claimed, batch...)
	return batch, nil
}

// MarkPublished, Reschedule and Fail record the lease they were handed, because
// the store is guarded on it and a test that dropped it would not notice.
func (s *stubRepo) MarkPublished(_ context.Context, claimed []event.Record) error {
	for _, record := range claimed {
		s.published = append(s.published, record.ID)
		s.publishedLeases = append(s.publishedLeases, record.LeaseUntil)
	}
	return nil
}

func (s *stubRepo) Reschedule(_ context.Context, record event.Record, cause error, retryAt time.Time) error {
	s.rescheduled = append(s.rescheduled, reschedule{id: record.ID, cause: cause, retryAt: retryAt})
	return nil
}

func (s *stubRepo) Fail(_ context.Context, record event.Record, cause error) error {
	s.failed = append(s.failed, failure{id: record.ID, cause: cause})
	return nil
}

// stubPublisher succeeds or fails per event id, so one bad event in a batch can
// be observed alongside the good ones.
type stubPublisher struct {
	failFor map[string]error
	seen    []string
}

func (p *stubPublisher) Publish(_ context.Context, record event.Record) error {
	p.seen = append(p.seen, record.EventID)
	if err, ok := p.failFor[record.EventID]; ok {
		return err
	}
	return nil
}

func testRecord(eventID, eventType string, attempts int) event.Record {
	return event.Record{
		ID:            int64(len(eventID)),
		EventID:       eventID,
		Topic:         event.Topic,
		EventType:     eventType,
		AggregateType: event.AggregateType,
		AggregateID:   1,
		Key:           "1",
		Payload:       []byte(`{}`),
		OccurredAt:    time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		Attempts:      attempts,
		LeaseUntil:    time.Date(2026, 9, 30, 12, 0, 30, 0, time.UTC),
	}
}

func testRelay(repo Repository, publisher Publisher, cfg RelayConfig) *Relay {
	return NewRelay(repo, publisher, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestRelayPublishesClaimedEvents(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{batches: [][]event.Record{{
		testRecord("a", event.TypeProductCreated, 1),
		testRecord("b", event.TypeProductUpdated, 1),
	}}}
	publisher := &stubPublisher{}
	relay := testRelay(repo, publisher, RelayConfig{BatchSize: 10})

	// drain is what Run calls on each tick, so testing it directly keeps the
	// test free of timing.
	if err := relay.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if len(publisher.seen) != 2 {
		t.Errorf("published %v, want both events", publisher.seen)
	}
	if len(repo.published) != 2 {
		t.Errorf("marked %d published, want 2", len(repo.published))
	}
	if len(repo.failed) != 0 || len(repo.rescheduled) != 0 {
		t.Errorf("unexpected failures: %d rescheduled, %d failed", len(repo.rescheduled), len(repo.failed))
	}
}

// The loop keeps claiming until a claim comes back empty, so a backlog spanning
// several batches is drained in one sweep rather than one batch per tick.
func TestRelayDrainsEveryFullBatch(t *testing.T) {
	t.Parallel()

	full := func() []event.Record {
		batch := make([]event.Record, 3)
		for i := range batch {
			batch[i] = testRecord(string(rune('a'+i)), event.TypeProductUpdated, 1)
		}
		return batch
	}
	repo := &stubRepo{batches: [][]event.Record{full(), full()}}
	publisher := &stubPublisher{}
	relay := testRelay(repo, publisher, RelayConfig{BatchSize: 3})

	if err := relay.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(publisher.seen) != 6 {
		t.Errorf("published %d events, want 6 from two full batches", len(publisher.seen))
	}
}

// One undeliverable event must not abandon the rest of its batch: they are
// different products, and blocking them behind a poison event would let one bad
// payload stall the whole stream.
func TestRelayIsolatesAFailedEvent(t *testing.T) {
	t.Parallel()

	boom := errors.New("broker unreachable")
	repo := &stubRepo{batches: [][]event.Record{{
		testRecord("good1", event.TypeProductCreated, 1),
		testRecord("bad", event.TypeProductUpdated, 1),
		testRecord("good2", event.TypeProductCreated, 1),
	}}}
	publisher := &stubPublisher{failFor: map[string]error{"bad": boom}}
	relay := testRelay(repo, publisher, RelayConfig{BatchSize: 10, MaxAttempts: 5})

	if err := relay.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if len(repo.published) != 2 {
		t.Errorf("marked %v published, want the two good events", repo.published)
	}
	if len(repo.rescheduled) != 1 || repo.rescheduled[0].id != repo.claimed[1].ID {
		t.Errorf("rescheduled %+v, want just the failed event", repo.rescheduled)
	}
	if len(repo.failed) != 0 {
		t.Errorf("a retryable failure was marked permanent: %+v", repo.failed)
	}
}

// A retried event must wait, not spin: a broker that is down should be polled
// occasionally rather than on every tick.
func TestRelayBacksOffBeforeRetrying(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{batches: [][]event.Record{{
		testRecord("a", event.TypeProductCreated, 3),
	}}}
	publisher := &stubPublisher{failFor: map[string]error{
		"a": errors.New("still down"),
	}}
	relay := testRelay(repo, publisher, RelayConfig{BatchSize: 10, MaxAttempts: 10})

	before := time.Now()
	if err := relay.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if len(repo.rescheduled) != 1 {
		t.Fatalf("rescheduled %d events, want 1", len(repo.rescheduled))
	}
	if !repo.rescheduled[0].retryAt.After(before) {
		t.Errorf("retry at %s, want it in the future", repo.rescheduled[0].retryAt)
	}
	if wait := repo.rescheduled[0].retryAt.Sub(before); wait < time.Second {
		t.Errorf("retry in %s, want at least a second of backoff", wait)
	}
}

// Past the attempt cap the event is abandoned, because retrying forever would
// leave one poison event at the head of the queue holding back everything behind
// it.
func TestRelayAbandonsAnEventThatExhaustsItsAttempts(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{batches: [][]event.Record{{
		testRecord("a", event.TypeProductCreated, 5),
	}}}
	publisher := &stubPublisher{failFor: map[string]error{
		"a": errors.New("permanently rejected"),
	}}
	relay := testRelay(repo, publisher, RelayConfig{BatchSize: 10, MaxAttempts: 5})

	if err := relay.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if len(repo.failed) != 1 {
		t.Fatalf("failed %d events, want 1", len(repo.failed))
	}
	if len(repo.rescheduled) != 0 {
		t.Errorf("a capped event was also rescheduled: %+v", repo.rescheduled)
	}
}

// A payload that can never be encoded is abandoned immediately, without burning
// the whole retry budget on an identical failure.
func TestRelayAbandonsAnUnrecoverableError(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{batches: [][]event.Record{{
		testRecord("a", event.TypeProductCreated, 1),
	}}}
	publisher := &stubPublisher{failFor: map[string]error{
		"a": errors.New("payload will not encode"),
	}}
	relay := testRelay(repo, publisher, RelayConfig{
		BatchSize:   10,
		MaxAttempts: 10,
		IsPermanent: func(error) bool { return true },
	})

	if err := relay.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if len(repo.failed) != 1 {
		t.Fatalf("failed %d events, want the unrecoverable one abandoned at once", len(repo.failed))
	}
	if len(repo.rescheduled) != 0 {
		t.Errorf("an unrecoverable error was rescheduled: %+v", repo.rescheduled)
	}
}

// An empty claim is the stopping condition, so a partial batch still gets
// published before the loop ends.
func TestRelayPublishesAPartialBatchThenStops(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{batches: [][]event.Record{{
		testRecord("a", event.TypeProductCreated, 1),
	}}}
	publisher := &stubPublisher{}
	relay := testRelay(repo, publisher, RelayConfig{BatchSize: 10})

	if err := relay.drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(publisher.seen) != 1 {
		t.Errorf("published %d events, want 1 before the loop stopped", len(publisher.seen))
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	t.Parallel()

	first := Backoff(1)
	second := Backoff(2)
	if second <= first {
		t.Errorf("Backoff(2) = %s, want more than Backoff(1) = %s", second, first)
	}

	// A large attempt count must not overflow the duration and produce a
	// negative or absurdly small wait.
	for _, attempts := range []int{0, 1, 15, 21, 1000} {
		if wait := Backoff(attempts); wait <= 0 {
			t.Errorf("Backoff(%d) = %s, want a positive wait", attempts, wait)
		}
	}
	if got := Backoff(1000); got != maxBackoff {
		t.Errorf("Backoff(1000) = %s, want the %s ceiling", got, maxBackoff)
	}
}

// The envelope is the contract consumers decode, so its shape is worth pinning
// down: a consumer that cannot read these fields cannot route the message.
func TestRecordEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	record := testRecord("evt-1", event.TypeProductCreated, 1)
	record.EventID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	record.OccurredAt = time.Date(2026, 9, 30, 12, 34, 56, 0, time.UTC)

	body, err := record.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var envelope event.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if envelope.EventID != record.EventID {
		t.Errorf("event id = %q, want %q", envelope.EventID, record.EventID)
	}
	if envelope.EventType != event.TypeProductCreated {
		t.Errorf("event type = %q, want %q", envelope.EventType, event.TypeProductCreated)
	}
	if envelope.SchemaVersion != event.SchemaVersion {
		t.Errorf("schema version = %d, want %d", envelope.SchemaVersion, event.SchemaVersion)
	}
	if envelope.AggregateID != record.AggregateID {
		t.Errorf("aggregate id = %d, want %d", envelope.AggregateID, record.AggregateID)
	}
	if !envelope.OccurredAt.Equal(record.OccurredAt) {
		t.Errorf("occurred at = %s, want %s", envelope.OccurredAt, record.OccurredAt)
	}
	if string(envelope.Payload) != "{}" {
		t.Errorf("payload = %s, want the record payload", envelope.Payload)
	}
}

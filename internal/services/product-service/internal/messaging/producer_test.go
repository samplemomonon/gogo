package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	"product-service/internal/event"
)

// unixMillis is the conversion the occurred_at header depends on, and it has one
// case that is easy to get wrong: the zero time.
func TestUnixMillis(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   time.Time
		want int64
	}{
		{
			// 2026-09-30T12:00:00Z. The header exists so a consumer can filter and
			// sort on the instant without parsing the body's RFC3339 string.
			name: "a normal instant",
			in:   time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
			want: 1790769600000,
		},
		{
			// A zero OccurredAt is a real case: an outbox row written before the
			// field existed, or a producer that did not set it. time.Time{}.UnixMilli()
			// is a large negative number, and a header carrying that would sort
			// every such event to 1969 or earlier.
			name: "the zero time maps to the epoch",
			in:   time.Time{},
			want: 0,
		},
		{
			// Sub-millisecond precision is truncated, not rounded. Rounding could
			// push an event onto the next millisecond and reorder it against a
			// neighbouring event.
			name: "sub-millisecond precision truncates",
			in:   time.Date(2026, 9, 30, 12, 0, 0, 999999, time.UTC),
			want: 1790769600000,
		},
		{
			name: "a non-UTC instant converts to the same epoch value",
			in:   time.Date(2026, 9, 30, 19, 0, 0, 0, time.FixedZone("WIB", 7*60*60)),
			want: 1790769600000,
		},
		{
			// Pre-epoch stays negative. That is the truth and ordering on it is
			// correct; clamping it to zero would claim such an event happened at
			// the epoch instead.
			name: "a pre-epoch instant stays negative",
			in:   time.Date(1969, 6, 1, 0, 0, 0, 0, time.UTC),
			want: -18489600000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := unixMillis(tc.in); got != tc.want {
				t.Errorf("unixMillis(%s) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// The header has to agree with the body. A consumer that sorts on the header while
// a reader reads the body must not see two different instants for one event.
func TestUnixMillisAgreesWithTheEncodedEnvelope(t *testing.T) {
	t.Parallel()

	occurredAt := time.Date(2026, 9, 30, 12, 0, 0, 500000, time.UTC)
	record := event.Record{
		EventID:    "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		EventType:  event.TypeProductCreated,
		Topic:      event.Topic,
		Key:        "1",
		OccurredAt: occurredAt,
		Payload:    []byte(`{}`),
	}

	body, err := record.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var envelope struct {
		OccurredAt time.Time `json:"occurred_at"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !envelope.OccurredAt.Equal(occurredAt) {
		t.Errorf("body carried %s, want %s", envelope.OccurredAt, occurredAt)
	}

	if got, want := unixMillis(envelope.OccurredAt), unixMillis(occurredAt); got != want {
		t.Errorf("header value %d disagrees with the body value %d", got, want)
	}
}

// The header value is a bare decimal integer, because a consumer parsing it should
// not need to know about formats at all.
func TestUnixMillisIsAPlainDecimalInteger(t *testing.T) {
	t.Parallel()

	encoded := strconv.FormatInt(unixMillis(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)), 10)

	if _, err := strconv.ParseInt(encoded, 10, 64); err != nil {
		t.Fatalf("header value %q is not a decimal integer: %v", encoded, err)
	}
	for _, r := range encoded {
		if r == '-' || (r < '0' || r > '9') {
			t.Errorf("header value %q contains %q, want digits only", encoded, r)
		}
	}
}

// This needs a broker, so it skips unless one is configured. It is the only test
// that proves the header and the record timestamp are actually on the wire.
func TestPublishCarriesTheTimestampHeader(t *testing.T) {
	brokers := os.Getenv("KAFKA_TEST_BROKERS")
	if brokers == "" {
		t.Skip("set KAFKA_TEST_BROKERS to run the producer wire tests against Kafka")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	producer, err := NewProducer(ProducerConfig{
		Brokers:        []string{brokers},
		ClientID:       "producer-header-test",
		PublishTimeout: 10 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	t.Cleanup(func() { _ = producer.Close(context.Background()) })

	// A topic per run. The broker persists topics between runs, so a fixed name
	// accumulates records and a consumer reading from the start would find a
	// leftover from an earlier run — one whose timestamp was set correctly, which
	// would let the assertion pass even with the code broken. A fresh name makes the
	// record this run published the only record there is.
	topic := fmt.Sprintf("product.events.v1.header-test.%d", time.Now().UnixNano())

	// The topic is created explicitly rather than left to auto-creation.
	// franz-go asks the broker for metadata before producing, so a topic that does
	// not exist yet comes back as UNKNOWN_TOPIC_OR_PARTITION rather than being
	// created on demand, and retrying does not help because nothing ever triggers
	// the creation.
	createTestTopic(t, ctx, brokers, topic)

	occurredAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	record := event.Record{
		ID:          1,
		EventID:     "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
		EventType:   event.TypeProductCreated,
		Topic:       topic,
		Key:         "1",
		Payload:     []byte(`{}`),
		OccurredAt:  occurredAt,
		AggregateID: 1,
	}
	if err := producer.Publish(ctx, record); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Read it back off the topic and inspect the metadata a consumer would see.
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(brokers),
		kgo.ConsumeTopics(record.Topic),
		// From the start: the topic is unique to this run, so it holds exactly the
		// one record published above.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	defer consumer.Close()

	fetches := consumer.PollRecords(ctx, 10)
	if err := fetches.Err0(); err != nil {
		t.Fatalf("poll: %v", err)
	}
	records := fetches.Records()
	if len(records) == 0 {
		t.Fatal("no record came back, want the one that was published")
	}
	got := records[0]

	headers := make(map[string]string, len(got.Headers))
	for _, header := range got.Headers {
		headers[header.Key] = string(header.Value)
	}

	if want := strconv.FormatInt(unixMillis(occurredAt), 10); headers[headerOccurredAtUnixMS] != want {
		t.Errorf("%s header = %q, want %q", headerOccurredAtUnixMS, headers[headerOccurredAtUnixMS], want)
	}
	for name, want := range map[string]string{
		headerEventType:     record.EventType,
		headerEventID:       record.EventID,
		headerAggregateType: record.AggregateType,
	} {
		if headers[name] != want {
			t.Errorf("%s header = %q, want %q", name, headers[name], want)
		}
	}

	// The record's own timestamp must be when the change committed, not when the
	// relay got round to publishing it.
	if !got.Timestamp.UTC().Equal(occurredAt) {
		t.Errorf("record timestamp = %s, want %s: left unset it would be the "+
			"produce time, so a relay backlog would age every event by the backlog",
			got.Timestamp.UTC(), occurredAt)
	}
}

// Close must report a failed flush.
//
// The bug this pins down: the flush error was logged and Close returned nil, so a
// shutdown finished reporting success while records the relay had already marked
// published were still sitting in the producer's buffer. Nothing is lost — the
// outbox republishes them once the lease expires — but the shutdown was wrong, and
// an orchestrator reading the exit code had no way to know.
//
// The producer is built against an unroutable address and flushed with a cancelled
// context, so Flush fails without needing a broker.
func TestCloseReportsAFailedFlush(t *testing.T) {
	t.Parallel()

	producer, err := NewProducer(ProducerConfig{
		Brokers:        []string{"127.0.0.1:59999"},
		ClientID:       "flush-failure-test",
		PublishTimeout: 100 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Flush on a cancelled context fails immediately.

	err = producer.Close(ctx)
	if err == nil {
		t.Fatal("Close = nil, want the flush failure reported")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled so a caller can tell a "+
			"flush failure from a connection problem", err)
	}
}

// createTestTopic creates topic with one partition, treating an existing topic as
// success. The replication factor is 1 because this only ever runs against the
// single-broker compose setup.
func createTestTopic(t *testing.T, ctx context.Context, broker string, topic string) {
	t.Helper()

	req := kmsg.NewPtrCreateTopicsRequest()
	reqTopic := kmsg.NewCreateTopicsRequestTopic()
	reqTopic.Topic = topic
	reqTopic.NumPartitions = 1
	reqTopic.ReplicationFactor = 1
	req.Topics = append(req.Topics, reqTopic)

	client, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	defer client.Close()

	resp, err := req.RequestWith(ctx, client)
	if err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
	if code := resp.Topics[0].ErrorCode; code != 0 {
		// Already existing is fine: a re-run against a fixed name would otherwise
		// fail on a topic that is exactly what it asked for.
		if kerr.ErrorForCode(code) != kerr.TopicAlreadyExists {
			t.Fatalf("create topic %s: %v", topic, kerr.ErrorForCode(code))
		}
	}
}

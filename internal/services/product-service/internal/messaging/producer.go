// Package messaging holds the Kafka producer. It is a thin adapter over
// franz-go: it knows how to turn an outbox record into a Kafka message and how
// to report failure, and nothing else. The outbox relay depends on the
// outbox.Publisher interface rather than on this type, so a test can substitute a
// recorder and franz-go never has to be reachable.
package messaging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"product-service/internal/event"
)

// ProducerConfig describes how to reach the broker.
type ProducerConfig struct {
	// Brokers is the seed broker list, host:port. The client discovers the rest
	// of the cluster from the seed, so one reachable broker is enough.
	Brokers []string
	// ClientID identifies this producer in broker logs and metrics.
	ClientID string
	// PublishTimeout bounds one produce call, so a broker that accepts the
	// connection but never acknowledges cannot pin a relay goroutine forever.
	PublishTimeout time.Duration
	// Acks is how many replicas must acknowledge. Defaults to all in-sync
	// replicas: an event that a majority of replicas have lost is worse than one
	// that took slightly longer to publish.
	Acks kgo.Acks
}

// Producer publishes outbox records to Kafka.
type Producer struct {
	client  *kgo.Client
	timeout time.Duration
	log     *slog.Logger
}

// NewProducer returns a producer connected to the configured brokers.
//
// The client dials lazily, so a broker that is not up yet does not stop this
// service from starting. Writes keep working while the broker is down because
// they land in the outbox first, and the relay simply finds nothing to publish
// until the connection is available.
func NewProducer(cfg ProducerConfig, log *slog.Logger) (*Producer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("messaging: at least one Kafka broker is required")
	}
	timeout := cfg.PublishTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	acks := cfg.Acks
	if acks == (kgo.Acks{}) {
		acks = kgo.AllISRAcks()
	}
	clientID := cfg.ClientID
	if clientID == "" {
		clientID = "product-service"
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ClientID(clientID),
		kgo.RequiredAcks(acks),
		// Idempotent produce is on by default and is kept on. It costs a
		// producer id and a sequence per partition, and in exchange a retry
		// after a timeout cannot duplicate the record in the log, which matters
		// because the relay will retry an event it could not confirm.
		kgo.RecordRetries(5),
		kgo.ProducerLinger(5*time.Millisecond),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.ProducerOnDataLossDetected(func(topic string, partition int32) {
			// Data loss means the broker lost a record this producer believed it
			// had written, which breaks the at-least-once assumption the outbox
			// relay depends on. The client stops producing on its own after this
			// callback, and the relay keeps retrying from the outbox, so the event
			// is retried rather than silently lost.
			log.Error("kafka reported data loss, producing has stopped",
				"topic", topic, "partition", partition)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("messaging: create kafka producer: %w", err)
	}

	return &Producer{client: client, timeout: timeout, log: log}, nil
}

// Close flushes anything buffered and releases the connections.
//
// The context passed here should be the shutdown context, not the signal
// context: a cancelled context would make the final flush fail immediately and
// the buffered events would be republished after the lease expires.
//
// A flush error is returned, not just logged. The relay marked rows published as
// soon as ProduceSync returned, so anything still buffered here is a record the
// relay believes is safely in Kafka and is not. The outbox will republish it after
// the lease expires, so nothing is lost — but the shutdown that reported success
// was wrong, and a caller deciding whether the process came down cleanly needs to
// know that. Swallowing it is how a deployment pipeline reports a green shutdown
// while events are waiting on a lease expiry.
func (p *Producer) Close(ctx context.Context) error {
	flushErr := p.client.Flush(ctx)
	// Close unconditionally so a failed flush still releases the sockets. A close
	// error is deliberately ignored: the flush outcome is the one that says
	// anything about whether events reached the broker, and returning it instead
	// would hide that behind a connection teardown problem.
	p.client.Close()

	if flushErr != nil {
		p.log.Error("flushing kafka producer failed, buffered events will be republished",
			"error", flushErr)
		return fmt.Errorf("flush kafka producer: %w", flushErr)
	}
	return nil
}

// header names. These travel with every record so a consumer can route, filter
// and order without decoding the body, which for JSON means parsing it first.
const (
	headerEventType     = "event_type"
	headerEventID       = "event_id"
	headerAggregateType = "aggregate_type"
	headerSchemaVersion = "schema_version"

	// headerOccurredAtUnixMS is OccurredAt as milliseconds since the Unix epoch.
	//
	// The body carries the same instant as an RFC3339 string, which is the right
	// form for a human reading the log and the wrong one for a filter: comparing
	// timestamps as strings is lexical, so "2026-01-02" sorts before
	// "2026-01-02T00:00:00Z" despite being later, and an offset would break it
	// outright. A number sorts correctly with no parsing and no format agreement.
	//
	// Milliseconds rather than seconds: the relay can drain a large backlog in
	// under a second, and second resolution would collapse a burst of events onto
	// one timestamp. Nanoseconds are unnecessary — the source is a TIMESTAMPTZ
	// column at microsecond precision, and no consumer here needs finer.
	headerOccurredAtUnixMS = "occurred_at_unix_ms"
)

// Publish sends one record and waits for the broker to acknowledge it.
//
// The message key is the record's partition key, which is what makes every event
// about one product land on one partition and therefore stay in order. The event
// type, id and timestamps also travel as headers so a consumer can route on them
// without decoding the body.
func (p *Producer) Publish(ctx context.Context, record event.Record) error {
	body, err := record.Marshal()
	if err != nil {
		// An envelope that will not encode will never encode, so this is a
		// permanent failure and must not be retried.
		return permanent(err)
	}

	publishCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	// ProduceSync returns after the broker's acknowledgement, which is what the
	// relay needs: MarkPublished has to mean the broker has the event, not that
	// the client has queued it.
	results := p.client.ProduceSync(publishCtx, &kgo.Record{
		Topic: record.Topic,
		Key:   []byte(record.Key),
		Value: body,
		// The record's own timestamp is set to when the change committed, not left
		// to the client. Left unset, franz-go stamps it with the time of the
		// produce call, which for an outbox relay is whenever the backlog drained
		// rather than when the product changed. Kafka's event time is what
		// retention policies and time-ordered consumers read, so a relay backlog
		// would age records by the length of the backlog.
		Timestamp: record.OccurredAt,
		Headers: []kgo.RecordHeader{
			{Key: headerEventType, Value: []byte(record.EventType)},
			{Key: headerEventID, Value: []byte(record.EventID)},
			{Key: headerAggregateType, Value: []byte(record.AggregateType)},
			{Key: headerSchemaVersion, Value: []byte(strconv.Itoa(event.SchemaVersion))},
			{Key: headerOccurredAtUnixMS, Value: []byte(strconv.FormatInt(unixMillis(record.OccurredAt), 10))},
		},
	})

	if err := results.FirstErr(); err != nil {
		return fmt.Errorf("publish %s to %s: %w", record.EventType, record.Topic, err)
	}
	return nil
}

// unixMillis converts t to milliseconds since the Unix epoch.
//
// The zero time is a real case here and not a hypothetical one: an outbox row
// written before this field existed has no occurrence time, and a header reading
// "-6795364578871" would be a nonsense value a consumer might order events by. It
// maps to 0, which is the epoch itself and reads as "unknown" rather than as a
// timestamp in the past.
func unixMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	// UnixMilli on a pre-epoch time is negative, which is correct and is left
	// alone: it orders before the epoch, which is the truth.
	return t.UTC().UnixMilli()
}

// permanentError marks a failure that retrying cannot fix. The relay uses it to
// give up immediately instead of burning the event's whole attempt budget on a
// payload that will never publish.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return &permanentError{err: err} }

// IsPermanent reports whether err is a failure no retry can fix.
func IsPermanent(err error) bool {
	var target *permanentError
	return errors.As(err, &target)
}

// Ping checks that the broker is reachable, used by the readiness probe. The
// error is advisory: a broker outage does not stop product-service from serving
// writes, it only delays the read model catching up.
func (p *Producer) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	if err := p.client.Ping(pingCtx); err != nil {
		return fmt.Errorf("ping kafka: %w", err)
	}
	return nil
}

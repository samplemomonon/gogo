// Package messaging holds the Kafka consumer that feeds the read model.
//
// The consumer is a plain consumer group member with manual offset commits. That
// choice is what makes the projection safe: offsets are committed only after an
// event has been applied, so a crash re-delivers rather than skips. Combined with
// the projector's event-id deduplication, the pair gives at-least-once delivery
// with an exactly-once effect on the read model.
package messaging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/projection"
)

// Handler applies one message and reports whether it is worth retrying.
//
// The position is the Kafka coordinates the record was read from, and the projector
// records it alongside the event id. That is what makes a replay auditable: a row
// can be traced back to the exact offset in the log it came from.
//
// A permanent error means the message can never be applied, for example an
// envelope this version cannot parse. The consumer sends those to the dead-letter
// topic rather than retrying forever, because a record that blocks its partition
// stops every product behind it from being readable.
type Handler interface {
	Apply(ctx context.Context, position projection.Position, body []byte) (projection.Outcome, error)
}

// Publisher sends a rejected message to the dead-letter topic.
type Publisher interface {
	Publish(ctx context.Context, topic string, key, value []byte, headers map[string]string) error
	Close(ctx context.Context) error
}

// ConsumerConfig describes the consumer's connection and behaviour.
type ConsumerConfig struct {
	// Brokers is the seed broker list, host:port.
	Brokers []string
	// GroupID is the consumer group. Every replica of this service joins the same
	// group so partitions are divided between them rather than duplicated.
	GroupID string
	// ClientID identifies this consumer in broker logs and metrics.
	ClientID string
	// Topics are consumed, normally just the product event topic.
	Topics []string
	// MaxPollRecords bounds one fetch, which bounds how long the rebalance is
	// blocked while a batch is applied.
	MaxPollRecords int
	// PollTimeout bounds how long a poll waits for records before returning
	// empty, so a cancellation is noticed promptly.
	PollTimeout time.Duration
	// DeadLetterTopic receives messages that cannot be applied. Empty disables it.
	DeadLetterTopic string
	// RetryBackoff is how long to pause after a transient failure. Without it a
	// broker that is briefly unreachable would be retried in a tight loop.
	RetryBackoff time.Duration
}

// ConsumerConfig defaults.
const (
	defaultMaxPollRecords = 200
	defaultPollTimeout    = time.Second
	defaultRetryBackoff   = time.Second
)

// Consumer reads product events and applies them to the read model.
type Consumer struct {
	client    *kgo.Client
	publisher Publisher
	handler   Handler
	cfg       ConsumerConfig
	log       *slog.Logger
}

// NewConsumer returns a consumer for the configured topics.
func NewConsumer(cfg ConsumerConfig, handler Handler, publisher Publisher, log *slog.Logger) (*Consumer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, fmt.Errorf("messaging: at least one Kafka broker is required")
	}
	if cfg.GroupID == "" {
		return nil, fmt.Errorf("messaging: a consumer group id is required")
	}
	if len(cfg.Topics) == 0 {
		return nil, fmt.Errorf("messaging: at least one topic is required")
	}
	if cfg.MaxPollRecords <= 0 {
		cfg.MaxPollRecords = defaultMaxPollRecords
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = defaultPollTimeout
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = defaultRetryBackoff
	}
	clientID := cfg.ClientID
	if clientID == "" {
		clientID = cfg.GroupID
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ClientID(clientID),
		kgo.ConsumerGroup(cfg.GroupID),
		kgo.ConsumeTopics(cfg.Topics...),
		// Offsets are committed by hand, after the event has been applied. The
		// default autocommit would commit on a timer regardless of whether the
		// work succeeded, which is precisely the window that loses events.
		kgo.DisableAutoCommit(),
		// Held across processing so a rebalance cannot move a partition away while
		// this replica is still writing from it. The alternative is a window where
		// another replica consumes the same records and both apply them.
		kgo.BlockRebalanceOnPoll(),
		// Start from the beginning on a group that has never committed, so a fresh
		// read model is built from the whole retained log rather than only from
		// events published after it started.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.FetchMaxWait(500*time.Millisecond),
		kgo.WithLogger(adaptLogger(log)),
	)
	if err != nil {
		return nil, fmt.Errorf("messaging: create kafka consumer: %w", err)
	}

	return &Consumer{
		client:    client,
		publisher: publisher,
		handler:   handler,
		cfg:       cfg,
		log:       log,
	}, nil
}

// Run consumes until ctx is cancelled.
//
// It returns nil on a clean shutdown. A failure to commit offsets is logged
// rather than returned, because the events are already applied: returning would
// stop the consumer and leave the read model frozen, when the correct response is
// to let the offsets be re-committed on the next batch.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.Info("consumer started",
		"group", c.cfg.GroupID,
		"topics", c.cfg.Topics,
		"max_poll_records", c.cfg.MaxPollRecords,
		"dead_letter_topic", c.cfg.DeadLetterTopic,
	)

	// Deferred rather than inline so a cancellation still releases the group
	// membership and connections on every exit path.
	defer c.client.Close()

	for {
		if ctx.Err() != nil {
			c.log.Info("consumer stopped")
			return nil
		}

		if err := c.pollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				c.log.Info("consumer stopped")
				return nil
			}
			// Back off rather than spinning: the failure is usually a broker or
			// database problem, and retrying it in a tight loop only adds load to
			// something that is already struggling.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(c.cfg.RetryBackoff):
			}
			c.log.Warn("consume poll failed, retrying", "error", err)
		}
	}
}

// pollOnce fetches one batch and applies it.
func (c *Consumer) pollOnce(ctx context.Context) error {
	// The poll is bounded so a cancellation is noticed promptly rather than only
	// when the broker produces something. A deadline that elapses with no records
	// is the normal idle case, not a failure, so Err0 returning context.Deadline
	// is translated into an empty result.
	pollCtx, cancel := context.WithTimeout(ctx, c.cfg.PollTimeout)
	defer cancel()

	fetches := c.client.PollRecords(pollCtx, c.cfg.MaxPollRecords)
	// Rebalances stay blocked until this returns, so it must be called on every
	// path. The commit below needs the assignments to still be ours.
	defer c.client.AllowRebalance()

	// Err0 reports the first fetch error. An idle poll is expected to hit its own
	// deadline, and treating that as an error would log a warning every second for
	// a consumer that is simply waiting.
	if err := fetches.Err0(); err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
			// Our own poll timeout with the consumer still live: nothing to do.
			return nil
		case ctx.Err() != nil:
			// Shutting down. Not worth reporting.
			return nil
		default:
			return fmt.Errorf("poll records: %w", err)
		}
	}

	records := fetches.Records()
	if len(records) == 0 {
		return nil
	}

	handled, applyErr := c.applyRecords(ctx, records)
	if applyErr != nil {
		// A record failed transiently. Only the prefix that was handled may be
		// committed: committing past the failure would advance the offset over an
		// event the read model never saw, and that event would then be gone for
		// good, which is the one failure this design exists to prevent.
		return c.commit(ctx, records[:handled])
	}
	return c.commit(ctx, records)
}

// commit records how far the consumer has got, and treats a failed commit as a
// warning rather than a fatal error.
//
// Losing a commit only costs a redelivery, which the projector's event-id check
// turns into a no-op. Treating it as fatal would stop the consumer instead, leaving
// the read model frozen for a problem that resolves itself on the next batch.
//
// The commit context is deliberately detached from the poll context, which has
// already expired by this point. Cancelling a commit mid-flight is what leaves a
// consumer group unable to make progress, so this one gets a fresh budget.
func (c *Consumer) commit(ctx context.Context, records []*kgo.Record) error {
	if len(records) == 0 {
		return nil
	}

	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	if err := c.client.CommitRecords(commitCtx, records...); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		c.log.Warn("committing offsets failed, the batch will be redelivered",
			"count", len(records), "error", err)
	}
	return nil
}

// applyRecords applies each record and returns the count that were handled, which
// is the prefix that may be committed.
//
// It stops at the first transient failure. Applying the rest would be wrong twice
// over: a later event may depend on the one that failed, and the whole batch is
// re-read regardless, so the work would simply be done twice.
func (c *Consumer) applyRecords(ctx context.Context, records []*kgo.Record) (int, error) {
	var (
		applied int
		skipped int
	)

	for i, record := range records {
		outcome, err := c.handler.Apply(ctx, projection.Position{
			Topic:     record.Topic,
			Partition: record.Partition,
			Offset:    record.Offset,
		}, record.Value)
		switch {
		case err == nil:
			switch outcome {
			case projection.OutcomeApplied:
				applied++
			default:
				// A duplicate or an unknown type is a success: the batch is done and
				// its offset may be committed.
				skipped++
			}

		case projection.IsPermanent(err):
			// The record can never be applied, so it must not hold up the partition.
			// It goes to the dead-letter topic and the offset is committed, which is
			// the only way the records behind it ever get read.
			skipped++
			c.deadLetter(ctx, record, err)

		default:
			// A transient failure, most often the read model being unreachable. The
			// offset is not advanced past this record, so it is re-read once the
			// cause clears.
			c.log.Error("applying event failed, its offset is not advanced",
				"topic", record.Topic,
				"partition", record.Partition,
				"offset", record.Offset,
				"handled", i,
				"error", err,
			)
			return i, err
		}
	}

	c.log.Debug("batch applied",
		"applied", applied, "skipped", skipped, "records", len(records))
	return len(records), nil
}

// deadLetter sends an unapplicable message to the dead-letter topic, wrapped with
// the position it was read from so it can be found again.
func (c *Consumer) deadLetter(ctx context.Context, record *kgo.Record, cause error) {
	c.log.Error("event cannot be applied, sending to the dead-letter topic",
		"topic", record.Topic,
		"partition", record.Partition,
		"offset", record.Offset,
		"error", cause,
	)

	if c.cfg.DeadLetterTopic == "" || c.publisher == nil {
		// Without a dead-letter topic the record is dropped after being logged.
		// Its offset is still committed, so the partition keeps moving.
		return
	}

	// The original position and event identity travel as headers, so a rejected
	// record can be traced back to exactly where it came from without reading the
	// body. The Kafka headers the producer set are carried over for the same
	// reason.
	headers := map[string]string{
		"error":              cause.Error(),
		"original_topic":     record.Topic,
		"original_partition": fmt.Sprintf("%d", record.Partition),
		"original_offset":    fmt.Sprintf("%d", record.Offset),
	}
	for _, header := range record.Headers {
		headers[header.Key] = string(header.Value)
	}

	// Detached from the batch context: this must not be abandoned because the
	// batch is about to be re-read.
	dlCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	if err := c.publisher.Publish(dlCtx, c.cfg.DeadLetterTopic, record.Key, record.Value, headers); err != nil {
		c.log.Error("publishing to the dead-letter topic failed, the record is dropped",
			"partition", record.Partition, "offset", record.Offset, "error", err)
	}
}

// Close leaves the group and releases the connections. The run loop calls it on
// exit; it is exported so a supervisor can shut the consumer down early.
func (c *Consumer) Close() {
	c.client.CloseAllowingRebalance()
}

// adaptLogger bridges franz-go's logging to slog, so the consumer's diagnostics
// land in the same structured stream as everything else.
type slogAdapter struct {
	log *slog.Logger
}

func adaptLogger(log *slog.Logger) kgo.Logger { return slogAdapter{log: log} }

func (a slogAdapter) Level() kgo.LogLevel {
	switch {
	case a.log.Enabled(nil, slog.LevelDebug): //nolint:staticcheck // a nil context is fine for a level check
		return kgo.LogLevelDebug
	case a.log.Enabled(nil, slog.LevelInfo): //nolint:staticcheck
		return kgo.LogLevelInfo
	case a.log.Enabled(nil, slog.LevelWarn): //nolint:staticcheck
		return kgo.LogLevelWarn
	default:
		return kgo.LogLevelError
	}
}

func (a slogAdapter) Log(level kgo.LogLevel, msg string, keyvals ...any) {
	// kgo supplies alternating key/value pairs, which map onto slog directly.
	attrs := make([]any, 0, len(keyvals))
	for i := 0; i+1 < len(keyvals); i += 2 {
		key, ok := keyvals[i].(string)
		if !ok {
			key = fmt.Sprintf("%v", keyvals[i])
		}
		attrs = append(attrs, key, keyvals[i+1])
	}

	switch level {
	case kgo.LogLevelError:
		a.log.Error("kafka client", append([]any{"msg", msg}, attrs...)...)
	default:
		a.log.Debug("kafka client", append([]any{"msg", msg}, attrs...)...)
	}
}

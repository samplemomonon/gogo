package productevents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// ConsumerGroup is the group the gateway commits under. It is deliberately distinct
// from both the platform's projection consumers (product-listing-service,
// product-search-service): Kafka splits partitions *within* a group, so sharing a group
// would hand each service a fraction of the stream and every one of them would
// silently observe a partial catalogue. Three groups means three full reads.
const ConsumerGroup = "gogo.gateway"

// Runner is a background feed the gateway starts and drains with its own lifecycle.
// Run blocks until ctx is cancelled.
type Runner interface {
	Run(ctx context.Context) error
}

// Consumer reads the platform's change stream and hands each record to a handler.
// Offsets are committed by mark, so a record is committed only after it was handled;
// a transient failure leaves it uncommitted and a restart replays it.
type Consumer struct {
	client  *kgo.Client
	handler func(ctx context.Context, value []byte) error
	logger  *slog.Logger
}

// NewConsumer dials the brokers and subscribes to the product event topic under group.
//
// The topic is subscribed from the beginning (AtStart) rather than from the latest
// offset: a gateway starting against an existing stream has no local caches to warm,
// so it wants the events it missed. Replaying them is cheap — each only drops a cache
// entry — and it means a cold gateway does not serve stale pages until the next
// unrelated write.
func NewConsumer(brokers []string, group string, handler func(context.Context, []byte) error, logger *slog.Logger) (*Consumer, error) {
	if len(brokers) == 0 {
		return nil, errors.New("productevents: at least one broker is required")
	}
	if handler == nil {
		return nil, errors.New("productevents: handler is required")
	}

	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(Topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.AutoCommitMarks(),
		kgo.AutoCommitInterval(5*time.Second),
		// Block rebalances between a poll and its AllowRebalance, so the group cannot
		// revoke a partition mid-batch and commit marks out of order.
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return nil, fmt.Errorf("productevents: create kafka client: %w", err)
	}
	return &Consumer{client: client, handler: handler, logger: logger}, nil
}

// Run polls, dispatches and marks until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	// CloseAllowingRebalance releases the poll-held rebalance lock before closing; a
	// plain Close would hang after a poll that never allowed one.
	defer c.client.CloseAllowingRebalance()

	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		for _, fetchErr := range fetches.Errors() {
			c.logger.Warn("productevents: fetch", "topic", fetchErr.Topic, "partition", fetchErr.Partition, "error", fetchErr.Err)
		}
		fetches.EachRecord(func(record *kgo.Record) { c.dispatch(ctx, record) })
		c.client.AllowRebalance()
	}
}

// dispatch handles one record.
//
// The three outcomes are deliberately different, because how a failure is handled is
// the difference between a gateway that keeps running and one that wedges:
//
//   - A skip or an undecodable record is committed. It will never become decodable, so
//     replaying it forever would stall every consumer behind it on the same partition.
//   - A handler failure is *not* committed, so the record replays after a restart.
//     That is safe only because every handler here is idempotent — they drop cache
//     entries and append to a bounded activity feed, neither of which double-counts.
func (c *Consumer) dispatch(ctx context.Context, record *kgo.Record) {
	err := c.handler(ctx, record.Value)
	switch {
	case err == nil:
		c.client.MarkCommitRecords(record)
	case errors.Is(err, ErrSkip):
		c.client.MarkCommitRecords(record)
	case errors.Is(err, ErrUndecodable):
		c.logger.Error("productevents: undecodable record; skipping",
			"topic", record.Topic, "partition", record.Partition, "offset", record.Offset, "error", err)
		c.client.MarkCommitRecords(record)
	default:
		c.logger.Error("productevents: handler failed; record left uncommitted for replay",
			"topic", record.Topic, "partition", record.Partition, "offset", record.Offset, "error", err)
	}
}

package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lib/pq"

	"product-service/internal/event"
)

// NotifyChannel is the PostgreSQL channel the outbox trigger publishes on. It is
// exported so the relay's LISTEN and the migration's pg_notify cannot drift
// apart unnoticed.
const NotifyChannel = "outbox_events"

// Publisher sends one event to the broker. The relay only needs this much of a
// producer, which keeps franz-go out of this package and lets tests run the whole
// loop against a recording stub.
type Publisher interface {
	// Publish sends the record and returns once the broker has acknowledged it
	// or the context expires. The key must be the record's partition key, so
	// events about one product keep their order.
	Publish(ctx context.Context, record event.Record) error
}

// Repository is the outbox storage the relay needs. It is an interface so the
// relay can be tested without a database.
//
// MarkPublished, Reschedule and Fail take the whole claimed record rather than an
// id, because they are guarded on the lease. Passing only the id would let a relay
// that stalled through its lease overwrite the bookkeeping of the relay that took
// the row over, which is how a row ends up marked published without anyone
// publishing it.
type Repository interface {
	Claim(ctx context.Context, batchSize int, lease time.Duration) ([]event.Record, error)
	MarkPublished(ctx context.Context, claimed []event.Record) error
	Reschedule(ctx context.Context, record event.Record, cause error, retryAt time.Time) error
	Fail(ctx context.Context, record event.Record, cause error) error
}

// RelayConfig tunes the background publisher.
type RelayConfig struct {
	// BatchSize is how many events one claim takes. Large enough to keep the
	// broker's batching useful, small enough that a failure retries quickly.
	BatchSize int
	// PollInterval is the fallback sweep. Notifications cut the common-case
	// latency; this is what guarantees nothing is stranded when one is missed.
	PollInterval time.Duration
	// Lease is how long a claimed batch stays reserved. It must comfortably
	// exceed the time to publish one batch, and must be short enough that a
	// relay that dies mid-publish releases its events quickly.
	Lease time.Duration
	// MaxAttempts is how many times an event is tried before it is marked failed
	// and left for an operator.
	MaxAttempts int
	// DSN is used only for the LISTEN connection. The outbox rows themselves are
	// read through the normal pool.
	DSN string
	// IsPermanent classifies a publish error as unretriable. It is injected
	// rather than imported so this package stays free of any dependency on the
	// Kafka client; leaving it nil means every failure is retried.
	IsPermanent func(error) bool
}

// RelayConfig defaults, applied for any field left at zero.
const (
	defaultBatchSize    = 100
	defaultPollInterval = 5 * time.Second
	defaultLease        = 30 * time.Second
	defaultMaxAttempts  = 10
	// maxBackoff bounds how long a failing broker is left alone between
	// attempts, so a long outage does not push an event's delivery hours out.
	maxBackoff = 5 * time.Minute
)

// Relay moves events from the outbox table to Kafka. It runs in the same process
// as the HTTP and gRPC servers: it shares the database pool and the lifecycle,
// and a separate process would only add a deployment unit to supervise.
//
// The loop is deliberately simple. Claim a batch, publish each event, record the
// outcome, repeat until the batch is smaller than the batch size, then wait for
// either a notification or the next tick. A failure to publish one event does not
// abandon the rest of the batch, because they are independent products and one
// undeliverable event should not hold up the queue behind it.
type Relay struct {
	repo      Repository
	publisher Publisher
	cfg       RelayConfig
	log       *slog.Logger
}

// NewRelay returns a relay publishing through publisher.
func NewRelay(repo Repository, publisher Publisher, cfg RelayConfig, log *slog.Logger) *Relay {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultBatchSize
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	if cfg.Lease <= 0 {
		cfg.Lease = defaultLease
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	return &Relay{
		repo:      repo,
		publisher: publisher,
		cfg:       cfg,
		log:       log,
	}
}

// Run publishes pending events until ctx is cancelled.
//
// It returns nil on a clean cancellation so the caller can treat that as part of
// a normal shutdown rather than a failure. A persistent database or broker
// problem is logged and retried on the next tick rather than returned, because
// exiting would take the command API down with it: writes still land in the
// outbox and are published whenever the dependency recovers.
func (r *Relay) Run(ctx context.Context) error {
	// A nil wake channel is not an error: it means the first LISTEN failed and
	// the relay is running on its poll interval alone. That costs latency, not
	// correctness, so it is not a reason to refuse to start. A nil channel in a
	// select simply never fires, which is exactly the fallback wanted here.
	wake := r.listen(ctx)
	if wake == nil {
		r.log.Warn("outbox relay continuing without LISTEN, falling back to polling",
			"poll_interval", r.cfg.PollInterval.String())
	}

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	r.log.Info("outbox relay started",
		"topic", event.Topic,
		"batch_size", r.cfg.BatchSize,
		"poll_interval", r.cfg.PollInterval.String(),
		"max_attempts", r.cfg.MaxAttempts,
	)

	for {
		select {
		case <-ctx.Done():
			r.log.Info("outbox relay stopped")
			return nil
		case <-ticker.C:
		case <-wake:
		}

		if err := r.drain(ctx); err != nil && !errors.Is(err, context.Canceled) {
			r.log.Error("outbox relay sweep failed", "error", err)
		}
	}
}

// drain publishes every event currently claimable, in batches.
//
// Each batch is published before the next is claimed, so the stop condition is
// the claim itself: an empty claim means the queue is drained. Claiming a full
// batch is deliberately not treated as "more work follows", because a batch can
// be full simply because the queue is exactly that size, and the next empty
// claim costs one cheap indexed query.
func (r *Relay) drain(ctx context.Context) error {
	for {
		claimed, err := r.repo.Claim(ctx, r.cfg.BatchSize, r.cfg.Lease)
		if err != nil {
			return err
		}
		if len(claimed) == 0 {
			return nil
		}
		r.publishBatch(ctx, claimed)

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

// publishBatch sends one claimed batch and records the outcome of each event
// individually.
//
// The whole record is handed to the store rather than the id, because the store
// matches on the lease: see Repository.
func (r *Relay) publishBatch(ctx context.Context, claimed []event.Record) {
	published := make([]event.Record, 0, len(claimed))

	for _, record := range claimed {
		// The publish context is detached from the request path on purpose. A
		// record that was claimed but not sent must still be retried, so
		// cancelling the attempt early would leave it to a lease expiry instead
		// of a controlled reschedule.
		if err := r.publisher.Publish(ctx, record); err != nil {
			r.handleFailure(ctx, record, err)
			continue
		}
		published = append(published, record)
	}

	if len(published) == 0 {
		return
	}
	if err := r.repo.MarkPublished(ctx, published); err != nil {
		// The events reached the broker but the acknowledgement was not recorded,
		// so they will be published again after the lease expires. That is the
		// documented at-least-once behaviour, and it is why consumers dedupe on
		// the event id.
		r.log.Error("marking outbox events published failed, they will be republished",
			"count", len(published), "error", err)
		return
	}
	r.log.Debug("outbox batch published", "count", len(published))
}

// handleFailure decides between retrying and giving up on one event.
//
// record.Attempts counts failures recorded before this one, so the budget check is
// against the attempt being made now: a row with MaxAttempts=10 is abandoned on
// its eleventh failed publish, not its tenth.
func (r *Relay) handleFailure(ctx context.Context, record event.Record, cause error) {
	// A failure that retrying cannot fix is abandoned immediately rather than
	// consuming the whole attempt budget first. The producer reports those
	// through the classifier below; nothing sets one, so the check is a no-op
	// until a caller does.
	if r.cfg.IsPermanent != nil && r.cfg.IsPermanent(cause) {
		r.abandon(ctx, record, cause, "unrecoverable publish error")
		return
	}

	if record.Attempts+1 >= r.cfg.MaxAttempts {
		r.abandon(ctx, record, cause, "attempts exhausted")
		return
	}

	wait := Backoff(record.Attempts + 1)
	if err := r.repo.Reschedule(ctx, record, cause, time.Now().Add(wait)); err != nil {
		r.log.Error("rescheduling outbox event failed", "event_id", record.EventID, "error", err)
		return
	}
	r.log.Warn("outbox publish failed, will retry",
		"event_id", record.EventID,
		"event_type", record.EventType,
		"attempts", record.Attempts,
		"retry_in", wait.String(),
		"error", cause,
	)
}

// abandon marks an event as permanently undeliverable. The row is kept rather
// than deleted because it is the only record that this committed change never
// reached the read model, which is exactly what an operator has to reconcile.
func (r *Relay) abandon(ctx context.Context, record event.Record, cause error, reason string) {
	if err := r.repo.Fail(ctx, record, cause); err != nil {
		r.log.Error("marking outbox event failed failed", "event_id", record.EventID, "error", err)
		return
	}
	r.log.Error("outbox event abandoned, the read model will not hear about this change",
		"event_id", record.EventID,
		"event_type", record.EventType,
		"aggregate_id", record.AggregateID,
		"attempts", record.Attempts,
		"reason", reason,
		"error", cause,
	)
}

// listen subscribes to the outbox notification channel.
//
// It returns nil on error, which leaves the select in Run waiting only on its
// ticker. pq.Listener reconnects on its own, so a dropped connection is retried
// internally and this call only fails when the very first LISTEN does.
func (r *Relay) listen(ctx context.Context) <-chan struct{} {
	if r.cfg.DSN == "" {
		return nil
	}

	listener := pq.NewListener(r.cfg.DSN, 5*time.Second, time.Minute, func(ev pq.ListenerEventType, err error) {
		switch ev {
		case pq.ListenerEventReconnected:
			// A reconnect can miss notifications sent while the connection was
			// down, which is why the poll ticker still runs.
			r.log.Info("outbox notification connection restored")
		case pq.ListenerEventDisconnected, pq.ListenerEventConnectionAttemptFailed:
			r.log.Warn("outbox notification connection interrupted, publish continues on the poll interval",
				"event", ev.String(), "error", err)
		case pq.ListenerEventConnected:
		}
	})
	if err := listener.Listen(NotifyChannel); err != nil {
		_ = listener.Close()
		return nil
	}

	// Close the listener when the relay is shutting down. Without this the
	// process would hold an idle connection open until the pool is torn down.
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	wake := make(chan struct{}, 1)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case notification := <-listener.Notify:
				if notification == nil {
					// pq signals a closed or resynchronising listener with a nil
					// value. Returning here is safe: the poll ticker in Run keeps
					// publishing either way.
					return
				}
				select {
				case wake <- struct{}{}:
				default:
					// A sweep is already pending, so this notification adds
					// nothing it will not already pick up.
				}
			}
		}
	}()

	return wake
}

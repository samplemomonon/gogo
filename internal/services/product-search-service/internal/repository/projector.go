package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"product-search-service/internal/event"
	"product-search-service/internal/model"
	"product-search-service/internal/projection"
)

// Indexer is the document writes the projector needs.
//
// It is an intra-package seam: both the producer (txProjector) and the sole
// implementation (*Engine) live in this package, so nothing outside needs to name
// it. Declaring it here rather than widening search.Engine keeps the read-side
// port at two methods.
//
// SetStock is part of this interface but not of search.Engine: a stock adjustment
// event carries one column, and an engine that cannot patch a single field would
// have to refetch the document. Declaring it here keeps that awkwardness on the
// write side of the boundary.
//
// Every method takes the transaction the write must join, because the dedupe marker
// and the document write have to commit together — see Projector.InTx. An engine
// that writes to a store which cannot join the caller's transaction (a real
// OpenSearch, say) cannot satisfy this interface, and that limitation belongs here
// where it is visible rather than discovered when the projection silently loses
// events.
//
// The *sql.Tx in these signatures is scoped to this seam on purpose. It is not a
// port the application layer declares, so it does not constrain the read path: the
// engine that the search use case depends on takes no transaction, and swapping
// it for Elasticsearch changes nothing above this line.
type Indexer interface {
	Index(ctx context.Context, tx *sql.Tx, doc *model.SearchProduct, eventID string) error
	Remove(ctx context.Context, tx *sql.Tx, productID int64) error
	SetStock(ctx context.Context, tx *sql.Tx, productID int64, stock int, eventID string) error
}

// Projector applies domain events to the search index.
//
// Every method writes through an upsert rather than an insert, which is what makes
// the projection safe under at-least-once delivery. A redelivered event is ordinary
// rather than exceptional, and re-applying one must leave the index in the same
// state. An INSERT would turn a normal retry into a unique violation and stall the
// consumer on the very first redelivery.
//
// Projector is separate from Engine because the two have opposite concerns: one is
// written by the consumer and must tolerate replays, the other is read by the API
// and must be simple. Sharing a type would mean every query carrying the weight of
// the projection's idempotency rules.
type Projector struct {
	db      *sql.DB
	indexer Indexer
}

// NewProjector returns a projector writing through indexer and recording what it
// applied in db.
func NewProjector(db *sql.DB, indexer Indexer) *Projector {
	return &Projector{db: db, indexer: indexer}
}

// InTx runs fn inside a single transaction, committing if fn returns nil and
// rolling back otherwise.
//
// This is why the indexer takes a *sql.Tx. Recording an event as processed and
// writing the document are two statements, and as two autocommit statements they
// have a failure window between them:
//
//	INSERT INTO processed_events ...   -- commits
//	INSERT INTO product_search ...      -- fails
//
// The marker is committed but the index is not. On redelivery the marker is found,
// ON CONFLICT DO NOTHING returns no row, the event is reported as a duplicate and
// the offset is committed — so the product is never searchable, and nothing ever
// retries it. One transaction rolls the marker back with the failed write.
func (p *Projector) InTx(ctx context.Context, fn func(projection.Mutations) error) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin projection transaction: %w", err)
	}

	if err := fn(&txProjector{db: p.db, indexer: p.indexer, tx: tx}); err != nil {
		// A rollback that fails because the connection died is not a second,
		// different reason to retry, so it is reported alongside rather than
		// replacing the error the caller acts on.
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w (rollback failed: %v)", err, rbErr)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit projection transaction: %w", err)
	}
	return nil
}

// txProjector is the transactional writer. It is a distinct type so a transaction
// cannot be started by accident through the shared, pool-bound Projector.
type txProjector struct {
	db      *sql.DB
	indexer Indexer
	tx      *sql.Tx
}

// The compile-time checks tie the projection contract to its two implementations.
var (
	_ projection.Mutations = (*txProjector)(nil)
	_ projection.Writer    = (*Projector)(nil)
)

// ApplyWithPosition records an event as processed, doing nothing if it already
// has been.
//
// This is the idempotency gate. The event id is a database-generated UUID that is
// unique per event, so a redelivery is detectable exactly, and the check plus the
// insert is a single statement. A concurrent duplicate loses the race and reports
// false here, which is the correct outcome: the other consumer already applied it.
//
// The Kafka position is stored alongside so a replay is auditable: a row can be
// traced back to the exact offset in the log that produced it.
//
// The dedupe marker and the document write are only safe together, so this writes
// through the caller's transaction. It is a method on txProjector for that reason,
// not on Projector.
func (p *txProjector) ApplyWithPosition(
	ctx context.Context,
	envelope *event.Envelope,
	topic string,
	partition int32,
	offset int64,
) (bool, error) {
	const insert = `
		INSERT INTO processed_events (event_id, event_type, aggregate_id, topic, partition, kafka_offset)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (event_id) DO NOTHING
		RETURNING event_id`

	var id string
	err := p.tx.QueryRowContext(ctx, insert,
		envelope.EventID, envelope.EventType, envelope.AggregateID,
		topic, partition, offset,
	).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// ON CONFLICT DO NOTHING returns no row, which is how a duplicate is
		// reported. Not an error.
		return false, nil
	case err != nil:
		return false, fmt.Errorf("record processed event %s: %w", envelope.EventID, err)
	}
	return true, nil
}

// IndexProduct writes a product snapshot into the search index.
//
// The document shape is a projection decision, so the conversion lives in the
// projection package. This method only carries the event across and hands it to
// the indexer.
func (p *txProjector) IndexProduct(ctx context.Context, snapshot *event.ProductSnapshot, eventID string) error {
	doc := projection.SearchDocumentFrom(snapshot)
	return p.indexer.Index(ctx, p.tx, doc, eventID)
}

// RemoveProduct drops a product from the search index.
//
// A missing document is not an error. The delete is what makes a redelivered
// delete event harmless, so reporting "not found" here would turn a duplicate into
// a consumer failure.
func (p *txProjector) RemoveProduct(ctx context.Context, productID int64) error {
	return p.indexer.Remove(ctx, p.tx, productID)
}

// SetProductStock updates only the stock column.
//
// A stock adjustment event carries the resulting value and nothing else, so writing
// just that column leaves every other field as the last snapshot put it. Writing
// the whole row instead would be wrong: the event does not know the current values
// of the other columns, and a stale copy would overwrite a newer product update
// that had already been indexed.
func (p *txProjector) SetProductStock(ctx context.Context, productID int64, stock int, eventID string) error {
	return p.indexer.SetStock(ctx, p.tx, productID, stock, eventID)
}

// Checkpoint is how far this service has consumed one partition.
type Checkpoint struct {
	Partition  int32     `json:"partition"`
	LastOffset int64     `json:"last_offset"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SaveCheckpoint records the offset consumed for a partition.
//
// This is operator bookkeeping, not the source of truth: the consumer group holds
// the authoritative offset in Kafka. It exists because the question it answers,
// "how far behind is the index", is one the broker cannot answer.
func (p *Projector) SaveCheckpoint(ctx context.Context, group string, partition int32, offset int64) error {
	const query = `
		INSERT INTO consumer_checkpoints (consumer_group, partition, last_offset)
		VALUES ($1, $2, $3)
		ON CONFLICT (consumer_group, partition) DO UPDATE
		SET last_offset = EXCLUDED.last_offset, updated_at = CURRENT_TIMESTAMP`

	if _, err := p.db.ExecContext(ctx, query, group, partition, offset); err != nil {
		return fmt.Errorf("save checkpoint for partition %d: %w", partition, err)
	}
	return nil
}

// Checkpoints returns the recorded offsets for a consumer group.
func (p *Projector) Checkpoints(ctx context.Context, group string) ([]Checkpoint, error) {
	const query = `
		SELECT partition, last_offset, updated_at
		FROM consumer_checkpoints
		WHERE consumer_group = $1
		ORDER BY partition`

	rows, err := p.db.QueryContext(ctx, query, group)
	if err != nil {
		return nil, fmt.Errorf("read checkpoints: %w", err)
	}
	defer func() { _ = rows.Close() }()

	checkpoints := make([]Checkpoint, 0)
	for rows.Next() {
		var checkpoint Checkpoint
		if err := rows.Scan(&checkpoint.Partition, &checkpoint.LastOffset, &checkpoint.UpdatedAt); err != nil {
			return nil, fmt.Errorf("read checkpoints: scan row: %w", err)
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read checkpoints: %w", err)
	}
	return checkpoints, nil
}

// Lag describes how far the search index has progressed.
type Lag struct {
	// AppliedEvents is how many events this service has projected.
	AppliedEvents int64 `json:"applied_events"`
}

// Stats reports the projection's progress, for the status endpoint.
func (p *Projector) Stats(ctx context.Context) (Lag, error) {
	const query = `SELECT COUNT(*) FROM processed_events`

	var lag Lag
	if err := p.db.QueryRowContext(ctx, query).Scan(&lag.AppliedEvents); err != nil {
		return Lag{}, fmt.Errorf("projection stats: %w", err)
	}
	return lag, nil
}

// PruneProcessedEvents deletes processed-event rows older than the retention
// window.
//
// The table exists to detect redeliveries, and a redelivery arrives within seconds
// or minutes of the original. Once a row is older than the retention window the
// broker will not replay it again, so keeping the row longer only grows a table
// that is written on every event.
func (p *Projector) PruneProcessedEvents(ctx context.Context, olderThan time.Duration) (int64, error) {
	const query = `DELETE FROM processed_events WHERE processed_at < CURRENT_TIMESTAMP - $1 * INTERVAL '1 second'`

	result, err := p.db.ExecContext(ctx, query, olderThan.Seconds())
	if err != nil {
		return 0, fmt.Errorf("prune processed events: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune processed events: rows affected: %w", err)
	}
	return affected, nil
}

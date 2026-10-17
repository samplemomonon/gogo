package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/event"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/projection"
)

// execer is the subset of *sql.DB that the projection writes through. Both
// *sql.DB and *sql.Tx satisfy it, which is what lets the same statements run
// inside or outside a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// writer holds the projection statements. It is shared by the Projector that owns
// the pool and the txProjector that owns a transaction, so the SQL exists once and
// cannot drift between the transactional and non-transactional paths.
type writer struct {
	db execer
}

// Projector applies domain events to the read model.
//
// Every method writes through an upsert rather than an insert, which is what makes
// the projection safe under at-least-once delivery. A redelivered event is
// ordinary rather than exceptional, and re-applying one must leave the read model
// in the same state. An INSERT would turn a normal retry into a unique violation
// and stall the consumer on the very first redelivery.
//
// Projector is separate from ListingRepository because the two have opposite
// concerns: one is written by the consumer and must tolerate replays, the other
// is read by the API and must be simple. Sharing a type would mean every query
// filter carrying the weight of the projection's idempotency rules.
type Projector struct {
	writer
	// pool is kept alongside the statements because starting a transaction needs
	// *sql.DB specifically, which the execer interface deliberately does not
	// expose.
	pool *sql.DB
}

// NewProjector returns a projector writing to db.
func NewProjector(db *sql.DB) *Projector {
	return &Projector{writer: writer{db: db}, pool: db}
}

// InTx runs fn with a writer whose statements join one transaction.
//
// This is the reason the dedupe marker and the read-model write cannot come
// apart. Recording an event as processed and applying it are two statements, and
// as two autocommit statements they have a failure window between them:
//
//	INSERT INTO processed_events ...   -- commits
//	INSERT INTO product_listings ...    -- fails, say on the UNIQUE slug
//
// The marker is committed but the read model is not. On redelivery the marker is
// found, ON CONFLICT DO NOTHING returns no row, the event is reported as a
// duplicate and the offset is committed — so the product silently never appears,
// and nothing ever retries it. Inside one transaction the marker is rolled back
// with the failed write, and the redelivery applies cleanly.
//
// The commit happens only if fn returns nil. A rollback on any error is what
// makes the redelivery correct.
func (p *Projector) InTx(ctx context.Context, fn func(projection.Mutations) error) error {
	tx, err := p.pool.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin projection transaction: %w", err)
	}

	if err := fn(&txProjector{writer{db: tx}}); err != nil {
		// The rollback error is deliberately not joined onto err: the caller
		// retries on err, and a rollback that fails because the connection died is
		// not a second, different reason to retry.
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

// txProjector is the transactional writer. It is a distinct type rather than a
// flag on Projector so that a transaction cannot be started by accident through
// the shared, pool-bound Projector.
type txProjector struct {
	writer
}

// The compile-time checks below are what tie the two implementations of the
// projection contract together. projection.Mutations is the write half;
// projection.Writer is the transaction runner that hands it out.
var (
	_ projection.Mutations = (*txProjector)(nil)
	_ projection.Mutations = (*Projector)(nil)
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
// On its own this statement is half the work. Recording the event and applying it
// have to be one transaction — see Projector.InTx for why — so this is always
// reached through a TxWriter.
func (p *writer) ApplyWithPosition(
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
	err := p.db.QueryRowContext(ctx, insert,
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

// UpsertProduct writes a product snapshot into the read model.
//
// The insert and the update are one statement, so a redelivered event costs the
// same as the first delivery and cannot fail halfway.
func (p *writer) UpsertProduct(ctx context.Context, snapshot *event.ProductSnapshot, eventID string) error {
	productTypeID := snapshot.ProductTypeID
	if productTypeID == "" {
		productTypeID = "general"
	}
	schemaVersion := snapshot.SchemaVersion
	if schemaVersion == 0 {
		schemaVersion = 1
	}
	productAttributes := snapshot.Attributes
	if productAttributes == nil {
		productAttributes = map[string]string{}
	}
	const upsert = `
		INSERT INTO product_listings (
			product_id, category_id, name, slug, description, brand,
			price_cents, currency, stock, status, primary_media_id,
			product_type_id, schema_version, attributes,
			created_at, updated_at, projected_at, last_event_id, merchant_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, CURRENT_TIMESTAMP, $17, $18)
		ON CONFLICT (product_id) DO UPDATE SET
			category_id      = EXCLUDED.category_id,
			name             = EXCLUDED.name,
			slug             = EXCLUDED.slug,
			description      = EXCLUDED.description,
			brand            = EXCLUDED.brand,
			price_cents      = EXCLUDED.price_cents,
			currency         = EXCLUDED.currency,
			stock            = EXCLUDED.stock,
			status           = EXCLUDED.status,
			primary_media_id = EXCLUDED.primary_media_id,
			product_type_id  = EXCLUDED.product_type_id,
			schema_version   = EXCLUDED.schema_version,
			attributes       = EXCLUDED.attributes,
			created_at       = EXCLUDED.created_at,
			updated_at       = EXCLUDED.updated_at,
			projected_at     = CURRENT_TIMESTAMP,
			last_event_id    = EXCLUDED.last_event_id,
			merchant_id      = EXCLUDED.merchant_id`

	attributes, err := json.Marshal(productAttributes)
	if err != nil {
		return fmt.Errorf("marshal product attributes %d: %w", snapshot.ID, err)
	}
	attributesJSON := string(attributes)
	if _, err := p.db.ExecContext(ctx, upsert,
		snapshot.ID, snapshot.CategoryID, snapshot.Name, snapshot.Slug,
		snapshot.Description, snapshot.Brand, snapshot.PriceCents, snapshot.Currency,
		snapshot.Stock, snapshot.Status, snapshot.PrimaryMediaID, productTypeID,
		schemaVersion, attributesJSON, snapshot.CreatedAt, snapshot.UpdatedAt, eventID,
		snapshot.MerchantID,
	); err != nil {
		return fmt.Errorf("upsert product %d: %w", snapshot.ID, err)
	}
	return nil
}

// UpsertVariant writes a variant snapshot into the read model.
func (p *writer) UpsertVariant(ctx context.Context, snapshot *event.VariantSnapshot, eventID string) error {
	const upsert = `
		INSERT INTO product_listing_variants (
			variant_id, product_id, sku, name, price_cents, currency, stock, media_id,
			created_at, updated_at, projected_at, last_event_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, CURRENT_TIMESTAMP, $11)
		ON CONFLICT (variant_id) DO UPDATE SET
			product_id   = EXCLUDED.product_id,
			sku          = EXCLUDED.sku,
			name         = EXCLUDED.name,
			price_cents  = EXCLUDED.price_cents,
			currency     = EXCLUDED.currency,
			stock        = EXCLUDED.stock,
			media_id     = EXCLUDED.media_id,
			created_at   = EXCLUDED.created_at,
			updated_at   = EXCLUDED.updated_at,
			projected_at = CURRENT_TIMESTAMP,
			last_event_id = EXCLUDED.last_event_id`

	if _, err := p.db.ExecContext(ctx, upsert,
		snapshot.ID, snapshot.ProductID, snapshot.SKU, snapshot.Name,
		snapshot.PriceCents, snapshot.Currency, snapshot.Stock, snapshot.MediaID,
		snapshot.CreatedAt, snapshot.UpdatedAt, eventID,
	); err != nil {
		return fmt.Errorf("upsert variant %d: %w", snapshot.ID, err)
	}
	return nil
}

// DeleteProduct removes a product and, by way of the schema, its variants.
//
// A missing row is not an error. The delete is what makes a redelivered delete
// event harmless, so reporting "not found" here would turn a duplicate into a
// consumer failure.
func (p *writer) DeleteProduct(ctx context.Context, productID int64) error {
	if _, err := p.db.ExecContext(ctx, `DELETE FROM product_listings WHERE product_id = $1`, productID); err != nil {
		return fmt.Errorf("delete product %d: %w", productID, err)
	}
	return nil
}

// DeleteVariant removes a variant.
func (p *writer) DeleteVariant(ctx context.Context, variantID int64) error {
	if _, err := p.db.ExecContext(ctx,
		`DELETE FROM product_listing_variants WHERE variant_id = $1`, variantID,
	); err != nil {
		return fmt.Errorf("delete variant %d: %w", variantID, err)
	}
	return nil
}

// SetProductStock updates only the stock column.
//
// A stock adjustment event carries the resulting value and nothing else, so
// writing just that column leaves every other field as the last snapshot put it.
// Writing the whole row instead would be wrong: the event does not know the
// current values of the other columns, and a stale copy would overwrite a newer
// product update that had already been projected.
func (p *writer) SetProductStock(ctx context.Context, productID int64, stock int, eventID string) error {
	const query = `
		UPDATE product_listings
		SET stock = $2, projected_at = CURRENT_TIMESTAMP, last_event_id = $3
		WHERE product_id = $1`

	if _, err := p.db.ExecContext(ctx, query, productID, stock, eventID); err != nil {
		return fmt.Errorf("set stock of product %d: %w", productID, err)
	}
	return nil
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
// "how far behind is the read model", is one the broker cannot answer.
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

// Lag describes how far the read model has progressed.
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
// The table exists to detect redeliveries, and a redelivery arrives within
// seconds or minutes of the original. Once a row is older than the retention
// window the broker will not replay it again, so keeping the row longer only
// grows a table that is written on every event.
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

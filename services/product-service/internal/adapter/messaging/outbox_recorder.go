// Package messaging provides the outbox recorder for derived events.
package messaging

import (
	"context"
	"database/sql"
	"fmt"

	"product-service/internal/domain/product"
	"product-service/internal/ports"
)

// OutboxRecorder writes derived events to the outbox in their own transactions.
// Derived events are those that cannot be part of the original transaction
// (e.g., republishing a product after a variant change).
type OutboxRecorder struct {
	db *sql.DB
}

// NewOutboxRecorder creates a new outbox recorder.
func NewOutboxRecorder(db *sql.DB) *OutboxRecorder {
	return &OutboxRecorder{db: db}
}

// Append writes a derived event in its own transaction.
func (r *OutboxRecorder) Append(ctx context.Context, event *product.DomainEvent) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin append derived event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := appendTo(ctx, tx, event); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit derived event: %w", err)
	}
	return nil
}

// Compile-time interface check
var _ ports.OutboxEventRecorder = (*OutboxRecorder)(nil)

// appendTo writes a single event to the outbox in an existing transaction.
func appendTo(ctx context.Context, tx *sql.Tx, event *product.DomainEvent) error {
	const insert = `
		INSERT INTO outbox_events (
			event_id, topic, event_type, aggregate_type, aggregate_id,
			partition_key, payload
		)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6)`

	if _, err := tx.ExecContext(ctx, insert,
		event.Topic, event.EventType, event.AggregateType,
		event.AggregateID, event.PartitionKey, event.Payload,
	); err != nil {
		return fmt.Errorf("insert outbox event %s: %w", event.EventType, err)
	}
	return nil
}

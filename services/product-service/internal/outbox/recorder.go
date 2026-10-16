package outbox

import (
	"context"
	"database/sql"
	"fmt"

	"product-service/internal/event"
)

// Recorder writes events for changes that have already committed.
//
// It exists because most events belong inside the transaction that made the
// change, and the repository handles those. A few are derived: adding a variant
// also changes how its product reads, so the product is republished once the
// variant transaction has closed. Those cannot join a transaction that no longer
// exists, so they are written on their own.
//
// The trade is deliberate and worth stating plainly. This write is not atomic
// with the change that caused it, so a crash in between loses the event. That is
// acceptable only because of what is lost: a republished product snapshot, not the
// fact that a variant was created. The authoritative events are the ones the
// repository stores in-transaction, and those cannot be lost. If a derived event
// ever carried something a consumer could not reconstruct, it would have to move
// into the same transaction instead.
type Recorder struct {
	db *sql.DB
}

// NewRecorder returns a recorder writing to db.
func NewRecorder(db *sql.DB) *Recorder { return &Recorder{db: db} }

// Append writes one event in its own transaction.
func (r *Recorder) Append(ctx context.Context, record event.Record) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin append derived event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := appendTo(ctx, tx, record); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit derived event: %w", err)
	}
	return nil
}

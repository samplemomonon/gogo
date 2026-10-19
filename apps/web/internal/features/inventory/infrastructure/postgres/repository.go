package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/features/inventory/application/ports"
	"gogo/internal/features/inventory/domain"
)

// StockRepository is the Postgres-backed inventory store. It owns the
// inventory_stock table outright and is the only writer of both the quantity and
// the movement ledger.
type StockRepository struct {
	pool *pgxpool.Pool
}

var _ ports.StockStore = (*StockRepository)(nil)

func NewStockRepository(pool *pgxpool.Pool) *StockRepository {
	return &StockRepository{pool: pool}
}

// Ping reports whether the backing database answers within the timeout.
func (r *StockRepository) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

// Close releases the connection pool.
func (r *StockRepository) Close() {
	r.pool.Close()
}

func (r *StockRepository) GetStock(ctx context.Context, tenant domain.Tenant, productID string) (*domain.Stock, error) {
	var stock domain.Stock
	err := r.pool.QueryRow(ctx,
		`SELECT product_id, quantity, updated_at FROM inventory_stock WHERE tenant_id = $1 AND product_id = $2`,
		string(tenant), productID,
	).Scan(&stock.ProductID, &stock.Quantity, &stock.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrStockNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get stock: %w", err)
	}
	return &stock, nil
}

func (r *StockRepository) ListStocks(ctx context.Context, tenant domain.Tenant, productIDs []string) ([]*domain.Stock, error) {
	query := `SELECT product_id, quantity, updated_at FROM inventory_stock WHERE tenant_id = $1`
	args := []any{string(tenant)}
	if len(productIDs) > 0 {
		query += ` AND product_id = ANY($2)`
		args = append(args, productIDs)
	}
	// A deterministic order matters for the seller console's stock table, which
	// renders one row per product: without ORDER BY the row order is whatever the
	// planner produces, which can change between calls and shuffle the page.
	query += ` ORDER BY product_id`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list stocks: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Stock, 0, 16)
	for rows.Next() {
		var stock domain.Stock
		if err := rows.Scan(&stock.ProductID, &stock.Quantity, &stock.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan stock: %w", err)
		}
		out = append(out, &stock)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list stocks: %w", err)
	}
	return out, nil
}

func (r *StockRepository) AdjustStock(ctx context.Context, tenant domain.Tenant, productID string, delta int, kind, note, actor, serviceActor, reference string) (*domain.Stock, *domain.Movement, error) {
	if err := domain.NewAdjustment(tenant, productID, kind, actor, reference); err != nil {
		return nil, nil, err
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: begin adjustment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Idempotency: a replay of the same reference returns the applied movement.
	if reference != "" {
		if existing, err := findMovementByReference(ctx, tx, string(tenant), reference); err != nil {
			return nil, nil, err
		} else if existing != nil {
			return &domain.Stock{ProductID: existing.ProductID, Quantity: existing.After, UpdatedAt: existing.CreatedAt}, existing, nil
		}
	}

	// Materialise the row first so an adjustment against a product that has never
	// been stocked succeeds from zero. The row is this module's to create — it no
	// longer depends on a product row existing elsewhere.
	if _, err := tx.Exec(ctx,
		`INSERT INTO inventory_stock (tenant_id, product_id, quantity, updated_at)
		 VALUES ($1, $2, 0, now())
		 ON CONFLICT (tenant_id, product_id) DO NOTHING`,
		string(tenant), productID,
	); err != nil {
		return nil, nil, fmt.Errorf("postgres: ensure stock row: %w", err)
	}

	// The conditional UPDATE is what makes the adjustment atomic and what refuses
	// to oversell: the `quantity + delta >= 0` guard is evaluated by Postgres under
	// the row lock, so two concurrent adjustments cannot both pass it.
	var after int
	err = tx.QueryRow(ctx,
		`UPDATE inventory_stock SET quantity = quantity + $3, updated_at = now()
		 WHERE tenant_id = $1 AND product_id = $2 AND quantity + $3 >= 0
		 RETURNING quantity`,
		string(tenant), productID, delta,
	).Scan(&after)
	if errors.Is(err, pgx.ErrNoRows) {
		// The row exists (it was just ensured), so a non-matching UPDATE means the
		// guard refused: this adjustment would drive stock below zero.
		return nil, nil, domain.ErrInsufficientStock
	}
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: adjust stock: %w", err)
	}

	movement := &domain.Movement{
		ID:           uuid.NewString(),
		ProductID:    productID,
		Kind:         kind,
		Delta:        delta,
		Before:       after - delta,
		After:        after,
		Note:         note,
		Actor:        actor,
		ServiceActor: serviceActor,
		Reference:    reference,
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO inventory_movements (id, tenant_id, product_id, kind, quantity_delta, quantity_before, quantity_after, note, actor, service_actor, reference)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		movement.ID, string(tenant), movement.ProductID, movement.Kind, movement.Delta,
		movement.Before, movement.After, movement.Note, movement.Actor, movement.ServiceActor, movement.Reference,
	); err != nil {
		return nil, nil, fmt.Errorf("postgres: record movement: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("postgres: commit adjustment: %w", err)
	}

	return &domain.Stock{ProductID: productID, Quantity: after}, movement, nil
}

func (r *StockRepository) ListMovements(ctx context.Context, tenant domain.Tenant, productID string, limit int) ([]*domain.Movement, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, product_id, kind, quantity_delta, quantity_before, quantity_after, note, actor, service_actor, reference, created_at
	          FROM inventory_movements WHERE tenant_id = $1`
	args := []any{string(tenant)}
	if productID != "" {
		query += ` AND product_id = $2`
		args = append(args, productID)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list movements: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Movement, 0, limit)
	for rows.Next() {
		var movement domain.Movement
		if err := rows.Scan(&movement.ID, &movement.ProductID, &movement.Kind, &movement.Delta,
			&movement.Before, &movement.After, &movement.Note, &movement.Actor, &movement.ServiceActor,
			&movement.Reference, &movement.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan movement: %w", err)
		}
		out = append(out, &movement)
	}
	return out, rows.Err()
}

func findMovementByReference(ctx context.Context, tx pgx.Tx, tenant, reference string) (*domain.Movement, error) {
	var movement domain.Movement
	err := tx.QueryRow(ctx,
		`SELECT id, product_id, kind, quantity_delta, quantity_before, quantity_after, note, actor, service_actor, reference, created_at
		 FROM inventory_movements WHERE tenant_id = $1 AND reference = $2`,
		tenant, reference,
	).Scan(&movement.ID, &movement.ProductID, &movement.Kind, &movement.Delta,
		&movement.Before, &movement.After, &movement.Note, &movement.Actor, &movement.ServiceActor,
		&movement.Reference, &movement.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: find movement: %w", err)
	}
	return &movement, nil
}

package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/modules/sales/application/ports"
	"gogo/internal/modules/sales/domain"
)

// Store is the Postgres-backed transaction store.
type Store struct {
	pool *pgxpool.Pool
}

var _ ports.Store = (*Store)(nil)

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Ping reports whether the backing database answers.
func (r *Store) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}

// Close releases the connection pool.
func (r *Store) Close() {
	r.pool.Close()
}

const transactionColumns = `tenant_id, id, number, total, discount, paid, change, payment_method, cashier, employee_id, voided_at, voided_by, void_reason, created_at`

const itemColumns = `id, product_id, name, quantity, price, discount, subtotal`

func (r *Store) Create(ctx context.Context, tenant domain.Tenant, transaction *domain.Transaction) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin sale: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO sales_transactions (tenant_id, id, number, total, discount, paid, change, payment_method, cashier, employee_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		string(tenant), transaction.ID, transaction.Number, transaction.Total, transaction.Discount,
		transaction.Paid, transaction.Change, transaction.PaymentMethod, transaction.Cashier,
		transaction.EmployeeID, transaction.CreatedAt,
	); err != nil {
		return fmt.Errorf("postgres: insert sale: %w", err)
	}

	for _, item := range transaction.Items {
		if _, err := tx.Exec(ctx,
			`INSERT INTO sales_transaction_items (id, transaction_id, tenant_id, product_id, name, quantity, price, discount, subtotal)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			item.ID, transaction.ID, string(tenant), item.ProductID, item.Name,
			item.Quantity, item.Price, item.Discount, item.Subtotal,
		); err != nil {
			return fmt.Errorf("postgres: insert sale item: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit sale: %w", err)
	}
	return nil
}

func (r *Store) Get(ctx context.Context, tenant domain.Tenant, id string) (*domain.Transaction, error) {
	var transaction domain.Transaction
	err := r.pool.QueryRow(ctx,
		`SELECT `+transactionColumns+` FROM sales_transactions WHERE tenant_id = $1 AND id = $2`,
		string(tenant), id,
	).Scan(&transaction.TenantID, &transaction.ID, &transaction.Number, &transaction.Total,
		&transaction.Discount, &transaction.Paid, &transaction.Change, &transaction.PaymentMethod,
		&transaction.Cashier, &transaction.EmployeeID, &transaction.VoidedAt,
		&transaction.VoidedBy, &transaction.VoidReason, &transaction.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTransactionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get sale: %w", err)
	}

	items, err := r.itemsFor(ctx, transaction.ID)
	if err != nil {
		return nil, err
	}
	transaction.Items = items
	return &transaction, nil
}

func (r *Store) List(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Transaction, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+transactionColumns+` FROM sales_transactions
		 WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2`,
		string(tenant), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list sales: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.Transaction, 0, limit)
	byID := make(map[string]*domain.Transaction, limit)
	for rows.Next() {
		var transaction domain.Transaction
		if err := rows.Scan(&transaction.TenantID, &transaction.ID, &transaction.Number, &transaction.Total,
			&transaction.Discount, &transaction.Paid, &transaction.Change, &transaction.PaymentMethod,
			&transaction.Cashier, &transaction.EmployeeID, &transaction.VoidedAt,
			&transaction.VoidedBy, &transaction.VoidReason, &transaction.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: scan sale: %w", err)
		}
		out = append(out, &transaction)
		byID[transaction.ID] = &transaction
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, transaction := range out {
		items, err := r.itemsFor(ctx, transaction.ID)
		if err != nil {
			return nil, err
		}
		transaction.Items = items
	}
	return out, nil
}

func (r *Store) SaveVoid(ctx context.Context, tenant domain.Tenant, transaction *domain.Transaction) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE sales_transactions SET voided_at = $3, voided_by = $4, void_reason = $5
		 WHERE tenant_id = $1 AND id = $2`,
		string(tenant), transaction.ID, transaction.VoidedAt, transaction.VoidedBy, transaction.VoidReason)
	if err != nil {
		return fmt.Errorf("postgres: void sale: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTransactionNotFound
	}
	return nil
}

func (r *Store) itemsFor(ctx context.Context, transactionID string) ([]domain.Item, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+itemColumns+` FROM sales_transaction_items WHERE transaction_id = $1`, transactionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list sale items: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Item, 0, 8)
	for rows.Next() {
		var item domain.Item
		if err := rows.Scan(&item.ID, &item.ProductID, &item.Name, &item.Quantity,
			&item.Price, &item.Discount, &item.Subtotal); err != nil {
			return nil, fmt.Errorf("postgres: scan sale item: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

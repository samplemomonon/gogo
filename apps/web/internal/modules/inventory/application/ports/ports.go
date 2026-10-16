// Package ports defines the inventory module's persistence and application surface.
package ports

import (
	"context"

	"gogo/internal/modules/inventory/domain"
)

// StockService is the inventory application surface: stock lookup and the atomic
// adjustment ledger. The gRPC adapter depends on this port; the Postgres and
// in-memory repositories implement it.
type StockService interface {
	// GetStock returns one product's stock, or ErrStockNotFound when the product
	// has no stock row.
	GetStock(ctx context.Context, tenant domain.Tenant, productID string) (*domain.Stock, error)

	// ListStocks returns the stock of every product, or of the given product ids.
	ListStocks(ctx context.Context, tenant domain.Tenant, productIDs []string) ([]*domain.Stock, error)

	// AdjustStock applies a signed delta atomically and records a movement. It fails
	// with ErrInsufficientStock when the delta would drive stock below zero, and is
	// idempotent on a non-empty reference.
	AdjustStock(ctx context.Context, tenant domain.Tenant, productID string, delta int, kind, note, actor, reference string) (*domain.Stock, *domain.Movement, error)

	// ListMovements returns the most recent movements for a product, or all products
	// when productID is empty.
	ListMovements(ctx context.Context, tenant domain.Tenant, productID string, limit int) ([]*domain.Movement, error)
}

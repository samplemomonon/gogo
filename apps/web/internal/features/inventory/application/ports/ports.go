// Package ports defines the inventory module's application and persistence contracts.
package ports

import (
	"context"

	"gogo/internal/features/inventory/domain"
)

// StockService is the inventory application surface consumed by delivery adapters.
type StockService interface {
	// GetStock returns one product's stock, or ErrStockNotFound when the product
	// has no stock row.
	GetStock(ctx context.Context, tenant domain.Tenant, productID string) (*domain.Stock, error)

	// ListStocks returns the stock of every product, or of the given product ids.
	ListStocks(ctx context.Context, tenant domain.Tenant, productIDs []string) ([]*domain.Stock, error)

	// AdjustStock applies a signed delta atomically and records a movement. It fails
	// with ErrInsufficientStock when the delta would drive stock below zero, and is
	// idempotent on a non-empty reference.
	AdjustStock(ctx context.Context, tenant domain.Tenant, productID string, delta int, kind, note, actor, serviceActor, reference string) (*domain.Stock, *domain.Movement, error)

	// ListMovements returns the most recent movements for a product, or all products
	// when productID is empty.
	ListMovements(ctx context.Context, tenant domain.Tenant, productID string, limit int) ([]*domain.Movement, error)
}

// StockStore is the persistence boundary used by the application service.
// AdjustStock must apply the quantity change and append its movement atomically;
// a non-empty reference is idempotent within a tenant. Its method set currently
// mirrors StockService because this module's use cases are direct operations; the
// distinct name keeps adapters out of the delivery-facing dependency declaration.
type StockStore interface {
	GetStock(ctx context.Context, tenant domain.Tenant, productID string) (*domain.Stock, error)
	ListStocks(ctx context.Context, tenant domain.Tenant, productIDs []string) ([]*domain.Stock, error)
	AdjustStock(ctx context.Context, tenant domain.Tenant, productID string, delta int, kind, note, actor, serviceActor, reference string) (*domain.Stock, *domain.Movement, error)
	ListMovements(ctx context.Context, tenant domain.Tenant, productID string, limit int) ([]*domain.Movement, error)
}

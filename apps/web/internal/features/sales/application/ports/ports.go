// Package ports defines the sales module's persistence and application surface.
package ports

import (
	"context"

	"gogo/internal/features/sales/domain"
)

// Store is the transaction persistence surface.
type Store interface {
	Create(ctx context.Context, tenant domain.Tenant, transaction *domain.Transaction) error
	Get(ctx context.Context, tenant domain.Tenant, id string) (*domain.Transaction, error)
	List(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Transaction, error)
	SaveVoid(ctx context.Context, tenant domain.Tenant, transaction *domain.Transaction) error
}

// StockAdjuster applies the stock effect of a sale through the inventory service.
// delta is signed; reference makes the adjustment idempotent.
type StockAdjuster interface {
	Adjust(ctx context.Context, tenant domain.Tenant, productID string, delta int, kind, reference string) error
}

// SalesService is the sales application surface. The gRPC adapter depends on it.
type SalesService interface {
	Checkout(ctx context.Context, tenant domain.Tenant, items []domain.CheckoutItem, discount, paid int64, method, cashier, employeeID string) (*domain.Transaction, error)
	Get(ctx context.Context, tenant domain.Tenant, id string) (*domain.Transaction, error)
	List(ctx context.Context, tenant domain.Tenant, limit int) ([]*domain.Transaction, error)
	Void(ctx context.Context, tenant domain.Tenant, id, by, reason string) (*domain.Transaction, error)
}

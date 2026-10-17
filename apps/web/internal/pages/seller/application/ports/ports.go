// Package ports defines what the seller console needs from its remote peers. The
// console owns these interfaces; the product platform, inventory and sales teams own
// the implementations behind the product.v1 / productlisting.v1 / productsearch.v1,
// inventory.v1 and sales.v1 gRPC contracts.
//
// There is no supplier port because suppliers exist in no service. Category writes
// are limited to merchant-local create commands; global category ownership stays
// with category-service's administrator API.
package ports

import (
	"context"

	"gogo/internal/pages/seller/application/results"
)

// ProductCatalog is the product capability the console consumes remotely. It
// splits along the platform's own seam rather than presenting one flat CRUD
// surface:
//
//   - Writes go to product-service (product.v1), the only writer of the aggregate.
//   - Reads come from the browse read model (productlisting.v1), which is a
//     projection of the write side's own event stream.
//
// The split is not an implementation detail of the adapter — it is the platform's
// architecture, and a console that wrote and read through one endpoint would
// couple itself to a design the platform deliberately rejected. The visible
// consequence is that a read can briefly disagree with a just-completed write.
type ProductCatalog interface {
	ListProductTypes(ctx context.Context) ([]results.ProductType, error)
	// List returns the console's product page from the browse read model.
	List(ctx context.Context) ([]*results.Product, error)

	// Create issues a product command carrying the starting quantity.
	Create(ctx context.Context, input results.ProductInput) (*results.Product, error)

	// Update applies a full-form edit. Stock is not among the updatable fields: the
	// inventory module is the sole writer of the sellable quantity after creation.
	Update(ctx context.Context, id string, input results.ProductInput) (*results.Product, error)

	Delete(ctx context.Context, id string) error

	// ListCategories returns global categories and this merchant's local categories.
	ListCategories(ctx context.Context) ([]*results.Category, error)
	// CreateMerchantCategory creates a category scoped to the resolved merchant.
	CreateMerchantCategory(ctx context.Context, input results.CategoryInput) (*results.Category, error)
}

// Inventory is the inventory capability the console consumes remotely.
type Inventory interface {
	ListStocks(ctx context.Context, productIDs []string) ([]*results.Stock, error)
	AdjustStock(ctx context.Context, productID string, delta int, kind, note, actor, reference string) (*results.Stock, *results.Movement, error)
	ListMovements(ctx context.Context, productID string, limit int) ([]*results.Movement, error)
}

// Sales is the point-of-sale capability the console consumes remotely.
type Sales interface {
	List(ctx context.Context) ([]*results.Transaction, error)
	Checkout(ctx context.Context, items []*results.TransactionItem, discount, paid int64, method, cashier, employeeID string) (*results.Transaction, error)
	Void(ctx context.Context, id, by, reason string) (*results.Transaction, error)
}

// EmployeeSessions resolves a browser session to its authenticated employee.
type EmployeeSessions interface {
	Current(ctx context.Context, sessionID string) (*results.Employee, error)
	CheckPermission(ctx context.Context, sessionID, permissionKey string) (bool, error)
	// ListMyMerchants returns the merchants the session employee belongs to.
	ListMyMerchants(ctx context.Context, sessionID string) ([]results.Merchant, error)
	// CanManageProduct is the merchant-scoped PDP: may the employee holding the
	// session modify products in the merchant?
	CanManageProduct(ctx context.Context, sessionID, merchantID string) (bool, error)
}

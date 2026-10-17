// Package seller is the unified seller console: a top-level gateway page (no
// manifest, no lifecycle) that consumes the catalog and inventory modules over
// their gRPC contracts. It is the read/write surface that replaced the old admin
// console.
package seller

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/seller/application/ports"
	"gogo/internal/pages/seller/handlers"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/web/navigation"
)

// Page is the seller console: a handler plus the nav item it contributes.
type Page struct {
	handler *handlers.Handler
}

// New wires the console around its remote peers.
func New(catalog ports.ProductCatalog, inventory ports.Inventory, sales ports.Sales, employees ports.EmployeeSessions, logger *slog.Logger) *Page {
	return &Page{handler: handlers.NewHandler(catalog, inventory, sales, employees, logger)}
}

// Register registers the seller page's graph with the composition root.
func Register(b *fdi.Builder) {
	fdi.ProvideType(b, func(c *fdi.Container) (*Page, error) {
		return New(
			fdi.MustResolveType[ports.ProductCatalog](c),
			fdi.MustResolveType[ports.Inventory](c),
			fdi.MustResolveType[ports.Sales](c),
			fdi.MustResolveType[ports.EmployeeSessions](c),
			fdi.MustResolveType[*slog.Logger](c),
		), nil
	})
}

// RegisterRoutes mounts the seller console under /seller.
//
// The supplier surface is gone because suppliers exist in no service at all, and a console
// that invented its own supplier store would be a second authority for data nothing
// else reads. Categories are listed here with merchant-local creation; global
// categories remain administrator-owned in category-service.
func (p *Page) RegisterRoutes(r chi.Router) {
	r.Route("/seller", func(r chi.Router) {
		r.Use(p.handler.RequireEmployee)
		r.With(p.handler.RequirePermission("seller.dashboard.read")).Get("/", p.handler.Dashboard)
		r.With(p.handler.RequirePermission("seller.catalog.read")).Get("/produk", p.handler.Products)
		r.With(p.handler.RequirePermission("seller.catalog.read")).Get("/produk/attributes", p.handler.ProductAttributeFields)
		r.With(p.handler.RequirePermission("seller.catalog.manage")).Post("/produk", p.handler.ProductCreate)
		r.With(p.handler.RequirePermission("seller.catalog.manage")).Post("/produk/{id}", p.handler.ProductUpdate)
		r.With(p.handler.RequirePermission("seller.catalog.manage")).Post("/produk/{id}/delete", p.handler.ProductDelete)
		r.With(p.handler.RequirePermission("seller.category.read")).Get("/kategori", p.handler.Categories)
		r.With(p.handler.RequirePermission("seller.category.manage")).Post("/kategori", p.handler.CategoryCreate)
		r.With(p.handler.RequirePermission("seller.inventory.read")).Get("/stok", p.handler.Stock)
		r.With(p.handler.RequirePermission("seller.inventory.manage")).Post("/stok", p.handler.StockAdjust)
		r.With(p.handler.RequirePermission("seller.sales.read")).Get("/kasir", p.handler.Kasir)
		r.With(p.handler.RequirePermission("seller.sales.checkout")).Post("/kasir", p.handler.Checkout)
		r.With(p.handler.RequirePermission("seller.sales.read")).Get("/transaksi", p.handler.Transactions)
		r.With(p.handler.RequirePermission("seller.sales.void")).Post("/transaksi/{id}/void", p.handler.TransactionVoid)
	})
}

// Nav returns the navigation the seller console contributes to the shell. The
// console is reached through the profile page's direct link rather than the
// top bar, so it contributes nothing: keeping it out of the federated nav is
// what removes it from the desktop menu and the mobile tab bar.
func (*Page) Nav() []navigation.Item {
	return nil
}

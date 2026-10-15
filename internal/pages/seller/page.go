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
func New(catalog ports.ProductCatalog, inventory ports.Inventory, sales ports.Sales, logger *slog.Logger) *Page {
	return &Page{handler: handlers.NewHandler(catalog, inventory, sales, logger)}
}

// Register registers the seller page's graph with the composition root.
func Register(b *fdi.Builder) {
	fdi.ProvideType(b, func(c *fdi.Container) (*Page, error) {
		return New(
			fdi.MustResolveType[ports.ProductCatalog](c),
			fdi.MustResolveType[ports.Inventory](c),
			fdi.MustResolveType[ports.Sales](c),
			fdi.MustResolveType[*slog.Logger](c),
		), nil
	})
}

// RegisterRoutes mounts the seller console under /seller.
//
// The category and supplier surfaces are gone. Categories are read-only here because
// category.v1 exposes reads only, so that exactly one service mutates the hierarchy
// (see ARCHITECTURE.md D-018); suppliers exist in no service at all, and a console
// that invented its own supplier store would be a second authority for data nothing
// else reads. Both were removed rather than stubbed: a visible button that cannot work
// is worse than an absent one.
func (p *Page) RegisterRoutes(r chi.Router) {
	r.Route("/seller", func(r chi.Router) {
		r.Get("/", p.handler.Dashboard)
		r.Get("/produk", p.handler.Products)
		r.Post("/produk", p.handler.ProductCreate)
		r.Post("/produk/{id}", p.handler.ProductUpdate)
		r.Post("/produk/{id}/delete", p.handler.ProductDelete)
		r.Get("/kategori", p.handler.Categories)
		r.Get("/stok", p.handler.Stock)
		r.Post("/stok", p.handler.StockAdjust)
		r.Get("/kasir", p.handler.Kasir)
		r.Post("/kasir", p.handler.Checkout)
		r.Get("/transaksi", p.handler.Transactions)
		r.Post("/transaksi/{id}/void", p.handler.TransactionVoid)
	})
}

// Nav returns the navigation the seller console contributes to the shell. The
// console is reached through the profile page's direct link rather than the
// top bar, so it contributes nothing: keeping it out of the federated nav is
// what removes it from the desktop menu and the mobile tab bar.
func (*Page) Nav() []navigation.Item {
	return nil
}

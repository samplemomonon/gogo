// Package home implements the public landing page. It is a top-level page
// of the gateway, NOT a feature module: it carries no manifest and no
// lifecycle. Like the seller console it imports no product-domain runtime code —
// it consumes the product platform remotely, through the productlisting.v1 and
// productsearch.v1 gRPC contracts (infrastructure/listingclient over
// application/ports.Listing), and renders the listing with its own views.
//
// Freshness note: the grid is a revalidated island with a short TTL, and this page
// no longer subscribes to any product event stream. The platform owns product
// changes in another process, so a mutation there is invisible to this process's
// event bus; the TTL is what bounds how long the grid can lag. That trade is the
// price of not owning the write side, and it is recorded in ARCHITECTURE.md.
package home

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/home/application/ports"
	homedetail "gogo/internal/pages/home/handlers/products/detail"
	homelist "gogo/internal/pages/home/handlers/products/list"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/web/navigation"
	"gogo/internal/shared/web/pagekit"
)

// Page is the home page: a handler plus the nav item it contributes to the
// shared shell. Pages are wired by the composition root, not registered as
// modules.
type Page struct {
	handler *homelist.Handler
	detail  *homedetail.Handler
}

// New wires the home page around its remote listing. listing is the remote
// consumer port (implemented by listingclient); logger receives the page's
// diagnostic output.
//
// The grid cache is *not* event-invalidated any more. It used to subscribe to the
// catalog's product events and drop its entries when one arrived; the write side now
// lives in product-service, another process, so no event reaches this bus. What
// bounds staleness instead is the rendering contract's TTL (30s, set by the
// handler). That is a weaker guarantee — a product created elsewhere appears within
// the TTL rather than immediately — and it is the honest cost of not owning the
// write side. An event-driven invalidation would need a Kafka change feed consumed
// here, which is deliberately out of scope: a 30s lag on a storefront grid does not
// justify a Kafka dependency in the gateway.
func New(listing ports.Listing, logger *slog.Logger) *Page {
	return &Page{
		handler: homelist.NewHandler(listing, pagekit.NewComposer(), logger),
		detail:  homedetail.NewHandler(listing, logger),
	}
}

// Register registers the home page's graph with the composition root, keyed by
// its type. Its inputs — the listing port and the logger — must already be
// registered by type, so the page owns its own wiring without any caller naming it.
func Register(b *fdi.Builder) {
	fdi.ProvideType(b, func(c *fdi.Container) (*Page, error) {
		return New(
			fdi.MustResolveType[ports.Listing](c),
			fdi.MustResolveType[*slog.Logger](c),
		), nil
	})
}

// RegisterRoutes mounts the home page at the gateway root.
func (p *Page) RegisterRoutes(r chi.Router) {
	r.Get("/", p.handler.Handle)
	r.Get("/catalog/{id}", p.detail.Handle)
}

// Nav returns the navigation the home page contributes to the shared shell.
// Pages are not modules, so their nav items do not live in a module
// manifest: the gateway compiles them ahead of the module-federated items.
func (*Page) Nav() []navigation.Item {
	return []navigation.Item{navigation.Home}
}

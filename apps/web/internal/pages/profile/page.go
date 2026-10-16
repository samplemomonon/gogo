// Package profile is the storefront account page: a top-level gateway page (no
// manifest, no lifecycle) that shows the signed-in shopper's account and links
// into the seller console. It consumes the auth module over its auth.v1 gRPC
// contract through its own client adapter, exactly as home and seller consume
// their backends, and never imports auth runtime code.
package profile

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/profile/application/ports"
	"gogo/internal/pages/profile/handlers"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/web/navigation"
)

// Page is the storefront account page: a handler plus the nav item it
// contributes to the shared shell.
type Page struct {
	handler *handlers.Handler
}

// New wires the account page around its remote auth peer.
func New(auth ports.Accounts, logger *slog.Logger) *Page {
	return &Page{handler: handlers.NewHandler(auth, logger)}
}

// Register registers the profile page's graph with the composition root.
func Register(b *fdi.Builder) {
	fdi.ProvideType(b, func(c *fdi.Container) (*Page, error) {
		return New(
			fdi.MustResolveType[ports.Accounts](c),
			fdi.MustResolveType[*slog.Logger](c),
		), nil
	})
}

// RegisterRoutes mounts the account page and its section routes at /profile.
func (p *Page) RegisterRoutes(r chi.Router) {
	r.Route("/profile", func(r chi.Router) {
		r.Get("/", p.handler.Section)
		r.Get("/pesanan", p.handler.Section)
		r.Get("/alamat", p.handler.Section)
		r.Get("/pengaturan", p.handler.Section)
	})
}

// Nav returns the navigation the account page contributes to the shell. Profile
// is the storefront's account destination, so it leads the mobile tab bar next
// to Home.
func (*Page) Nav() []navigation.Item {
	return []navigation.Item{navigation.Profile}
}

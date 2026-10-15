// Package auth is the login page for the gateway.
package auth

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/auth/application/ports"
	"gogo/internal/pages/auth/handlers"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/web/navigation"
	"gogo/internal/shared/web/session"
)

// Page wraps the auth page's routes.
type Page struct {
	handler *handlers.Handler
}

// New wires the login page around its remote peer. cookie is the deployment's
// session-cookie policy, resolved from the composition root rather than guessed
// here: whether the app sits behind TLS is not something a page can know.
func New(auth ports.Auth, logger *slog.Logger, cookie session.Policy) *Page {
	return &Page{handler: handlers.NewHandler(auth, logger, cookie)}
}

// Register registers the auth page's graph with the composition root.
func Register(b *fdi.Builder) {
	fdi.ProvideType(b, func(c *fdi.Container) (*Page, error) {
		return New(
			fdi.MustResolveType[ports.Auth](c),
			fdi.MustResolveType[*slog.Logger](c),
			fdi.MustResolveType[session.Policy](c),
		), nil
	})
}

// RegisterRoutes registers the auth page's routes.
func (p *Page) RegisterRoutes(r chi.Router) {
	r.Get("/login", p.handler.Login)
	r.Post("/login", p.handler.LoginPost)
	r.Post("/logout", p.handler.Logout)
}

// Nav returns the navigation items the auth page contributes to the shell.
func (*Page) Nav() []navigation.Item {
	return []navigation.Item{}
}

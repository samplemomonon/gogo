// Package navigation defines the shell navigation model shared by the
// gateway, its feature modules and its pages: the ordered top-bar menu
// items plus the per-request facts (menu, current path) the gateway
// injects so the shell can highlight the active item.
//
// Navigation is deliberately NOT owned by the module layer. Modules
// federate nav items through their manifest (module.Descriptor.Nav), and
// pages contribute items through the gateway's page contract; both produce
// this package's Item. Having the type here keeps each layer decoupled:
// pages never import the module registry, and the shell never imports
// modules or pages.
package navigation

import (
	"context"

	"github.com/a-h/templ"

	"gogo/internal/shared/web/ui"
)

// Item is one link of the shared shell navigation: what the shell renders
// and the route it links to. Icon is an optional glyph rendered ahead of the
// label by the shell chrome (the mobile tab bar and the dropdown); a plain
// text menu leaves it nil.
type Item struct {
	Label string          `json:"label"`
	Path  string          `json:"path"`
	Icon  templ.Component `json:"-"`
}

// The canonical top-level destinations of the gogo shell. They live here, not in a
// page or module, so a label is defined once and a page that also registers a route can
// reference the same wording the shell shows.
var (
	Home = Item{Label: "Beranda", Path: "/", Icon: ui.IconHome("size-5")}
	// Billing carries an icon like the other two. Without one the shell's
	// mobile tab bar dropped it — the bar renders only icon-led items — so
	// /billing was reachable from the top bar (which is hidden below md) and
	// from nowhere else. A destination a phone cannot open is not a
	// destination.
	Billing = Item{Label: "Tagihan", Path: "/billing", Icon: ui.IconDocument("size-5")}
	Profile = Item{Label: "Akun", Path: "/profile", Icon: ui.IconUser("size-5")}
)

// navKey is the private context key carrying the shell navigation items
// injected by the gateway.
type navKey struct{}

// WithNav returns a context carrying the shell navigation items the gateway
// compiled from its registered pages and module manifests.
func WithNav(ctx context.Context, nav []Item) context.Context {
	return context.WithValue(ctx, navKey{}, nav)
}

// NavFromContext returns the shell navigation items for ctx, or nil when
// the request was not routed through the gateway (e.g. a page rendered
// standalone in a test).
func NavFromContext(ctx context.Context) []Item {
	nav, _ := ctx.Value(navKey{}).([]Item)
	return nav
}

// pathKey is the private context key carrying the current request path the
// gateway injected, used by the shell to highlight the active nav item.
type pathKey struct{}

// WithPath returns a context carrying the request path of the current
// request, injected by the gateway so the shell can render nav state.
func WithPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, pathKey{}, path)
}

// PathFromContext returns the request path for ctx, or "" when the request
// was not routed through the gateway.
func PathFromContext(ctx context.Context) string {
	path, _ := ctx.Value(pathKey{}).(string)
	return path
}

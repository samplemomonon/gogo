// Package pagekit holds the composition helpers shared by the gateway's
// top-level pages (the home storefront). A page is not a module: it has no manifest
// and no lifecycle, but every page still assembles the same three things — a
// rendering composer, a subscription that keeps its island caches coherent
// when domain events arrive, and a page route. This package owns that
// repeated wiring so a page declares only what is decisively its own (its
// island key, its port, its views).
//
// The package is deliberately domain-agnostic: it imports no module or page,
// so consuming it never couples two pages. The event TYPES a page reacts to
// are supplied by the caller, keeping the catalog event contract owned by the
// catalog module.
package pagekit

import (
	"context"

	"gogo/internal/shared/events"
	"gogo/internal/shared/web/render"
)

// composerFactory is the shared factory for all page composers.
var composerFactory = render.DefaultFactory()

// NewComposer returns a rendering composer backed by its own bounded TTL
// cache — the standard composer for a top-level page's islands.
func NewComposer() *render.Composer {
	return composerFactory.NewComposer()
}

// InvalidateOnEvents keeps an island coherent with a source of events: it
// subscribes to exactly the given event types on the shared bus and clears
// the named islands from the composer's cache whenever one arrives. The
// subscription is contract-keyed (it matches only the declared types), and
// the returned subscription can be cancelled when the page shuts down.
//
// This is a page's typical touchpoint with another capability's event
// contract: the page never publishes, it only reacts to keep its own cache
// fresh.
func InvalidateOnEvents(
	bus events.Bus,
	composer *render.Composer,
	eventTypes []string,
	islands ...string,
) *events.Subscription {
	return events.Subscribe(bus, func(_ context.Context, _ events.Event) {
		composer.Invalidate(islands...)
	}, eventTypes...)
}

// InvalidatePrefixOnEvents is the paged-list variant of InvalidateOnEvents:
// it clears every island whose cache key begins with one of the prefixes on
// the matching event, instead of a fixed set of keys. A paginated storefront
// caches one entry per page, so after a mutation every page must be dropped —
// the exact-key form cannot reach them.
func InvalidatePrefixOnEvents(
	bus events.Bus,
	composer *render.Composer,
	eventTypes []string,
	prefixes ...string,
) *events.Subscription {
	return events.Subscribe(bus, func(_ context.Context, _ events.Event) {
		for _, prefix := range prefixes {
			composer.InvalidatePrefix(prefix)
		}
	}, eventTypes...)
}

// Package billing implements the billing feature module: it satisfies the
// gateway's module contract (a manifest plus route registration) and, beyond
// its own routes, consumes product change events from the shared module event
// bus — the server-side counterpart of htmx custom DOM events.
//
// The events it consumes are published by the gateway's productevents feed, not by
// anything it imports: the product platform owns writes in another process, and this
// module observes that fact without knowing who performed it. Keep the bus subscription
// wired whenever you change this module, so billing's activity projection stays
// coherent with the catalogue.
package billing

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/go-chi/chi/v5"

	"gogo/internal/modules/billing/activity"
	"gogo/internal/modules/billing/handlers"
	"gogo/internal/shared/events"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/kernel"
	"gogo/internal/shared/module"
	"gogo/internal/shared/productevents"
	"gogo/internal/shared/web/navigation"
)

// Module is the composition root of the billing feature.
type Module struct {
	handler *handlers.Handler

	// cancel ends the product-event subscription. It is held so Stop can drain it:
	// without that, the subscription outlives the module and keeps receiving events
	// for the life of the process.
	cancel func()
}

// NewModule wires the billing module. bus is the gateway's event bus under the
// kernel EventBus contract — the same in-process bus modules publish on, reached
// through its kernel view (the builder resolves kernel.EventBus from the container).
// Billing subscribes to the product-changed vocabulary through it, keyed to the exact
// types it records, so the feed never needs an all-events type switch.
//
// Both the subscription and a subscription failure are handled rather than discarded.
// The old wiring threw away the returned Subscription and the error, which made a
// subscription failure silent and left the feed living for the whole process; a
// failure now surfaces at boot, and Stop cancels what NewModule started.
func NewModule(bus kernel.EventBus, logger *slog.Logger) (*Module, error) {
	feed := activity.New(20)
	ctx := context.Background()

	sub, err := bus.SubscribeTypes(ctx, handler(feed.OnEvent), productevents.Types()...)
	if err != nil {
		return nil, fmt.Errorf("billing: subscribe to product events: %w", err)
	}

	// A logger that panics on nil would turn a wiring mistake into a crash in a
	// constructor, so default it rather than dereference it blindly.
	if logger == nil {
		logger = slog.Default()
	}

	module := &Module{handler: handlers.New(feed)}
	module.cancel = sync.OnceFunc(func() { _ = sub.Unsubscribe() })

	logger.Info("billing subscribed to product change events", "types", productevents.Types())
	return module, nil
}

// Start satisfies module.Lifer. The subscription is already live — it was created in
// NewModule, before the module was registered, so no product change could slip through
// the gap between construction and boot — so there is nothing to bring up here.
func (m *Module) Start(context.Context) error { return nil }

// Stop cancels the product-event subscription so the module stops observing once the
// gateway has drained it. Being a module.Lifer means the registry starts the module
// before the server listens and drains it in reverse order on shutdown.
func (m *Module) Stop(context.Context) error {
	m.cancel()
	return nil
}

// Register declares the billing module's graph with the composition root, keyed by
// its type, so the root wires modules by listing units the way it wires pages. Its
// single input — the shared bus under the kernel.EventBus contract — must already be
// registered by type, so billing owns its own wiring and no caller has to name it.
func Register(b *fdi.Builder) {
	fdi.ProvideType(b, func(c *fdi.Container) (*Module, error) {
		bus, err := fdi.ResolveType[kernel.EventBus](c)
		if err != nil {
			return nil, fmt.Errorf("billing: resolve kernel.EventBus: %w", err)
		}
		logger, err := fdi.ResolveType[*slog.Logger](c)
		if err != nil {
			return nil, fmt.Errorf("billing: resolve logger: %w", err)
		}
		return NewModule(bus, logger)
	})
}

// handler adapts the activity projection to the kernel.EventHandler contract, which
// returns an error. The projection has nothing to report — it appends to a bounded
// feed and cannot fail — so the adapter always succeeds.
func handler(fn func(context.Context, events.Event)) kernel.EventHandler {
	return func(ctx context.Context, event kernel.Event) error {
		fn(ctx, events.Event{Type: event.Type(), Payload: event.Payload()})
		return nil
	}
}

// Manifest is the module's remote contract: the only thing the shell and
// other modules may rely on. Bump Version whenever the fragment contract
// of a route changes so consumers can pin against it.
func (m *Module) Manifest() module.Descriptor {
	return module.Descriptor{
		Name:        "billing",
		Version:     "0.2.0",
		Entry:       "/billing",
		Description: "Subscription billing and invoice management",
		Routes: map[string]string{
			"index":    "/billing",
			"fragment": "/billing/fragment",
		},
		Nav: []navigation.Item{
			navigation.Billing,
		},
	}
}

// RegisterRoutes lets the gateway mount the module without knowing its
// routes: the browser discovers them through the manifest + fragment
// contract, the same way htmx lazy-loads islands.
func (m *Module) RegisterRoutes(r chi.Router) {
	m.handler.RegisterRoutes(r)
}

var (
	_ module.Interface = (*Module)(nil)
	_ module.Lifer     = (*Module)(nil)
)

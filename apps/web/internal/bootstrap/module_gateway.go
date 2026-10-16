// BuildGateway composes the gateway application.
package bootstrap

import (
	"context"
	"log/slog"

	"gogo/internal/shared/config"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/httpserver"
	"gogo/internal/shared/kernel"
	"gogo/internal/shared/productevents"
)

// BuildGateway composes the gateway: the feature modules (auth, inventory, sales,
// billing, ops) and the top-level pages (home, seller, profile) that consume them
// over their gRPC contracts, plus the connections to the product platform — the
// external services that own the product domain (see ARCHITECTURE.md D-018).
//
// Every product-platform address is required, so the graph is fully remote for the
// product domain and fully in-process for everything else. There is no longer a
// "modular monolith vs microservices" toggle: this process never owned products.
//
// The whole object graph is declared in buildGatewayContainer and resolved here by
// name: the config, the one module bus (also reachable as kernel.EventBus for
// kernel-adopting components), the kernel logger, the module registry, the
// connections/clients/breakers, the pages, the modules and the app itself.
// BuildGateway adds only what cannot live in a provider — the kernel-contract
// observability subscriber and the product change feed — and returns the lifecycle's
// shutdown hook. A build failure drains whatever the graph had already acquired so a
// partial boot leaves no resource behind.
func BuildGateway(
	parent context.Context,
	cfg *config.AppConfig,
	logger *slog.Logger,
) (*httpserver.App, func(), error) {
	container := buildGatewayContainer(parent, cfg, logger)
	lifecycle := gatewayLifecycle(container)

	// Resolving the app walks the graph: connections open, in-process modules
	// register, pages subscribe. A failure here means some resource may already have
	// been acquired, so release the lifecycle before surfacing the error.
	app, err := fdi.ResolveAs[*httpserver.App](container, serviceApp)
	if err != nil {
		lifecycle.Close()
		return nil, nil, err
	}

	// The product change feed: it turns the platform's Kafka stream into events on the
	// shared in-process bus, which is how modules learn about a product that changed in
	// another process. It is registered after the observer below so the lifecycle's
	// reverse-order drain stops the observer first, then the feed, then the connections.
	stopFeed, err := startProductChangeFeed(parent, cfg, gatewayBus(container), logger)
	if err != nil {
		// A misconfigured change feed is worth failing on only when the operator asked
		// for one. With no brokers configured the feed is already a no-op and logs a
		// warning; here the brokers were configured and could not be used.
		lifecycle.Close()
		return nil, nil, err
	}
	lifecycle.Add(stopFeed)

	// Product changes are observable through the kernel.EventBus contract too: the
	// adapter shares the underlying module bus, so a kernel subscriber receives the
	// same product events modules publish directly. Observation is best-effort debug
	// telemetry and must never fail gateway startup.
	lifecycle.Add(observeKernelProductEvents(parent, gatewayKernelBus(container), gatewayKernelLogger(container)))

	return app, lifecycle.Close, nil
}

// observeKernelProductEvents subscribes to product change events through the
// kernel.EventBus contract and logs them at debug level. It is the gateway's
// demonstration seam for the kernel layer: components that adopt the kernel contracts
// can resolve kernelBus from the container and reach the same event stream the modules
// use. The returned func cancels the observer (idempotent).
func observeKernelProductEvents(ctx context.Context, bus kernel.EventBus, logger kernel.Logger) func() {
	sub, err := bus.Subscribe(ctx, func(_ context.Context, event kernel.Event) error {
		if isProductEvent(event.Type()) {
			logger.Debug("product event observed on gateway bus", "type", event.Type())
		}
		return nil
	})
	if err != nil {
		// A debug-observability subscriber must never fail gateway startup.
		logger.Warn("product event observation unavailable", "error", err)
		return func() {}
	}
	return func() { _ = sub.Unsubscribe() }
}

// isProductEvent reports whether a type belongs to the product-change vocabulary. It
// is the gateway's own predicate rather than the productevents package's Types slice so
// the observer does not allocate on every event it sees.
func isProductEvent(eventType string) bool {
	for _, typ := range productevents.Types() {
		if typ == eventType {
			return true
		}
	}
	return false
}

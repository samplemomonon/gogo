// Container assembly for the gateway composition root.
package bootstrap

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"

	"gogo/internal/modules/billing"
	"gogo/internal/modules/ops"
	"gogo/internal/pages/auth"
	authports "gogo/internal/pages/auth/application/ports"
	authclient "gogo/internal/pages/auth/infrastructure/authclient"
	"gogo/internal/pages/contentcms"
	"gogo/internal/pages/home"
	homeports "gogo/internal/pages/home/application/ports"
	homelistingclient "gogo/internal/pages/home/infrastructure/listingclient"
	"gogo/internal/pages/profile"
	profileports "gogo/internal/pages/profile/application/ports"
	profileauthclient "gogo/internal/pages/profile/infrastructure/authclient"
	"gogo/internal/pages/seller"
	sellerports "gogo/internal/pages/seller/application/ports"
	sellerauthclient "gogo/internal/pages/seller/infrastructure/authclient"
	sellercatalogclient "gogo/internal/pages/seller/infrastructure/catalogclient"
	sellerinventoryclient "gogo/internal/pages/seller/infrastructure/inventoryclient"
	sellersalesclient "gogo/internal/pages/seller/infrastructure/salesclient"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/authn"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/config"
	"gogo/internal/shared/events"
	"gogo/internal/shared/foundation/adapter"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/httpserver"
	"gogo/internal/shared/kernel"
	"gogo/internal/shared/module"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/web/session"
)

// Upstream names. They qualify the per-upstream registrations — the connections
// and the breakers — and label each breaker in the contributed group, so the gateway's
// metrics and the ops readiness probe report the same identity a reader sees in a
// config file.
//
// The four product-platform names come from productconsumer.Upstream rather than
// being restated here, so the name a breaker is filed under, the name in the metrics
// and the name the kit reports cannot drift apart.
const (
	upstreamInventory = "inventory"
	upstreamSales     = "sales"
	upstreamAuth      = "auth"

	upstreamProduct        = string(productconsumer.UpstreamWrite)
	upstreamProductListing = string(productconsumer.UpstreamBrowse)
	upstreamProductSearch  = string(productconsumer.UpstreamSearch)
	upstreamCategory       = string(productconsumer.UpstreamCategory)
)

// Service names for the gateway's dependency graph. Only what still needs a
// string name is named here: the process primitives and the page-owned remote
// clients. Everything a unit can publish by type or by type-plus-name is keyed
// by that instead, so a package's wiring stays encapsulated in the package that
// owns the type rather than in shared name constants.
const (
	serviceParentContext = "context.parent"      // context.Context
	serviceConfig        = "config.AppConfig"    // *config.AppConfig
	serviceLogger        = "slog.Logger"         // *slog.Logger, the process logger
	serviceBus           = "events.TypedBus"     // *events.MemoryBus, the shared bus
	serviceLifecycle     = "bootstrap.Lifecycle" // *fdi.Lifecycle, the shutdown accumulator

	serviceSellerCatalogClient   = "gateway.sellerCatalogClient"   // *sellercatalogclient.Client
	serviceSellerInventoryClient = "gateway.sellerInventoryClient" // *sellerinventoryclient.Client
	serviceSellerSalesClient     = "gateway.sellerSalesClient"     // *sellersalesclient.Client
	serviceListingClient         = "gateway.listingClient"         // *homelistingclient.Client
	serviceAuthClient            = "gateway.authClient"            // *authconsumer.Client
	serviceSalesCapability       = "gateway.salesInventoryCapability"
	serviceProductPlatform       = "gateway.productPlatform" // *productPlatform
	// serviceProductPlatformBreakers is productconsumer.Breakers, the four platform
	// breakers as one value, so a surface takes the whole set rather than four names.
	serviceProductPlatformBreakers = "gateway.productPlatformBreakers"
	serviceApp                     = "gateway.app" // *httpserver.App
)

// buildGatewayContainer declares the gateway's whole object graph: the shared
// primitives, the catalog/listing connections the pages consume, the remote
// clients and breakers around them, the pages, the feature modules and the
// gateway app itself. Registration is declarative and lazy — a provider runs
// the first time its service is resolved — so resolving serviceApp walks the
// dependency chain in order and a component is constructed at most once.
//
// The registered *events.MemoryBus is the single source of truth for
// in-process events: modules and pages publish on it directly (events.Bus)
// while kernel-adopting components reach the same bus through the
// kernel.EventBus adapter — one bus, two contracts (see ARCHITECTURE.md D-004).
//
// Resources a provider acquires (a connection, the kernel observer) register
// their release with the shared Lifecycle, so BuildGateway returns one
// shutdown hook regardless of how much of the graph was built.
func buildGatewayContainer(parent context.Context, cfg *config.AppConfig, logger *slog.Logger) *fdi.Container {
	bus := events.NewMemoryBus()
	// A panicking subscriber is contained (so it never breaks a request) but
	// reported, so a broken projection is visible in the logs instead of
	// silently going stale.
	bus.WithPanicReporter(func(event events.Event, recovered any) {
		logger.Error("event subscriber panicked",
			"event", event.Type,
			"panic", recovered,
		)
	})

	b := fdi.NewBuilder()
	fdi.ProvideInstance(b, serviceParentContext, parent)
	fdi.ProvideInstance(b, serviceConfig, cfg)
	fdi.ProvideInstance(b, serviceLogger, logger)
	fdi.ProvideInstance(b, serviceBus, bus)
	fdi.ProvideInstance(b, serviceLifecycle, fdi.NewLifecycle())

	// The process primitives the pages resolve by type: the shared bus under
	// its interface, the logger, and the registry — so a unit's Register needs
	// no name string.
	fdi.ProvideInstanceType[events.Bus](b, bus)
	fdi.ProvideInstanceType[*slog.Logger](b, logger)
	fdi.ProvideInstanceType[*module.Registry](b, module.NewRegistry(cfg.ServiceName, cfg.ServiceVersion))
	fdi.Provide(b, serviceSalesCapability, func(*fdi.Container) (authn.Capability, error) {
		return authn.NewCapability()
	})

	// Kernel views over the process primitives: one bus and one logger, two
	// contracts each.
	fdi.ProvideType[kernel.Logger](b, func(c *fdi.Container) (kernel.Logger, error) {
		return adapter.NewLoggerAdapter(gatewayLogger(c)), nil
	})
	fdi.ProvideType[kernel.EventBus](b, func(c *fdi.Container) (kernel.EventBus, error) {
		return adapter.NewEventBusAdapter(gatewayBus(c)), nil
	})

	// The product platform: one provider dials all four services, because they are
	// always needed together and dialing them independently would let a partial
	// configuration start the gateway with a subset of the platform. Connections are
	// resources, so the platform's close hook lands in the lifecycle.
	fdi.Provide(b, serviceProductPlatform, func(c *fdi.Container) (*productPlatform, error) {
		platform, err := connectProductPlatform(gatewayConfig(c), gatewayLogger(c))
		if err != nil {
			return nil, err
		}
		gatewayLifecycle(c).Add(platform.Close)
		return platform, nil
	})

	// The inventory service is always in-process: the seller console consumes it
	// over its inventory.v1 contract on a bufconn transport, and the module joins
	// the registry like any other.
	fdi.ProvideNamed(b, upstreamInventory, func(c *fdi.Container) (*grpc.ClientConn, error) {
		authorizer, err := fdi.ResolveAs[*authconsumer.Client](c, serviceAuthClient)
		if err != nil {
			return nil, err
		}
		capability, err := fdi.ResolveAs[authn.Capability](c, serviceSalesCapability)
		if err != nil {
			return nil, err
		}
		conn, closeConn, err := connectInventory(
			gatewayParent(c),
			gatewayConfig(c),
			gatewayLogger(c),
			gatewayRegistry(c),
			moduleSecurity{authorizer: authorizer, capability: capability},
		)
		if err != nil {
			return nil, err
		}
		gatewayLifecycle(c).Add(closeConn)
		return conn, nil
	})

	// The sales service is always in-process too. It composes the inventory
	// connection to apply a sale's stock effect, so it resolves the inventory
	// connection and breaker it depends on before it is built.
	fdi.ProvideNamed(b, upstreamSales, func(c *fdi.Container) (*grpc.ClientConn, error) {
		inventoryConn, err := fdi.ResolveNamedAs[*grpc.ClientConn](c, upstreamInventory)
		if err != nil {
			return nil, err
		}
		inventoryBreaker, err := fdi.ResolveNamedAs[*breaker.Breaker](c, upstreamInventory)
		if err != nil {
			return nil, err
		}
		authorizer, err := fdi.ResolveAs[*authconsumer.Client](c, serviceAuthClient)
		if err != nil {
			return nil, err
		}
		capability, err := fdi.ResolveAs[authn.Capability](c, serviceSalesCapability)
		if err != nil {
			return nil, err
		}
		conn, closeConn, err := connectSales(
			gatewayParent(c),
			gatewayConfig(c),
			inventoryConn,
			inventoryBreaker,
			gatewayLogger(c),
			gatewayRegistry(c),
			moduleSecurity{authorizer: authorizer, capability: capability},
		)
		if err != nil {
			return nil, err
		}
		gatewayLifecycle(c).Add(closeConn)
		return conn, nil
	})

	// The auth service is always in-process. It manages employees and sessions.
	fdi.ProvideNamed(b, upstreamAuth, func(c *fdi.Container) (*grpc.ClientConn, error) {
		conn, closeConn, err := connectAuth(
			gatewayParent(c),
			gatewayConfig(c),
			gatewayLogger(c),
			gatewayRegistry(c),
		)
		if err != nil {
			return nil, err
		}
		gatewayLifecycle(c).Add(closeConn)
		return conn, nil
	})

	// One breaker per upstream, each named so the client that renders through it
	// resolves its own, and each contributed to the breaker group so the consumers that
	// care about *all* upstreams — the gateway's metrics and the ops readiness probe —
	// pick up a new upstream from one contribution.
	//
	// The four product-platform breakers are deliberately separate rather than one
	// shared guard: a search outage must not take the storefront's browse with it, and
	// a write outage must not stop the console reading what it needs to diagnose it.
	registerUpstreamBreakers(b)

	// productPlatformBreakers resolves the four platform breakers as one value, so a
	// surface that needs the whole set takes it as a bundle instead of resolving four
	// names and risking a mismatch between the connections and the breakers it hands
	// over.
	fdi.Provide(b, serviceProductPlatformBreakers, func(c *fdi.Container) (productconsumer.Breakers, error) {
		return resolvePlatformBreakers(c)
	})

	// Remote clients: each page owns its adapter over the shared consumer kit.
	// A client depends on its own connection and breaker, so a failed dial or a
	// missing registration surfaces here as an error rather than a panic.
	fdi.Provide(b, serviceSellerCatalogClient, func(c *fdi.Container) (*sellercatalogclient.Client, error) {
		platform, err := fdi.ResolveAs[*productPlatform](c, serviceProductPlatform)
		if err != nil {
			return nil, err
		}
		breakers, err := fdi.ResolveAs[productconsumer.Breakers](c, serviceProductPlatformBreakers)
		if err != nil {
			return nil, err
		}
		return sellercatalogclient.New(platform.Connections(), breakers), nil
	})
	fdi.Provide(b, serviceSellerInventoryClient, func(c *fdi.Container) (*sellerinventoryclient.Client, error) {
		conn, err := fdi.ResolveNamedAs[*grpc.ClientConn](c, upstreamInventory)
		if err != nil {
			return nil, err
		}
		inventoryBreaker, err := fdi.ResolveNamedAs[*breaker.Breaker](c, upstreamInventory)
		if err != nil {
			return nil, err
		}
		return sellerinventoryclient.New(conn, inventoryBreaker), nil
	})
	fdi.Provide(b, serviceSellerSalesClient, func(c *fdi.Container) (*sellersalesclient.Client, error) {
		conn, err := fdi.ResolveNamedAs[*grpc.ClientConn](c, upstreamSales)
		if err != nil {
			return nil, err
		}
		salesBreaker, err := fdi.ResolveNamedAs[*breaker.Breaker](c, upstreamSales)
		if err != nil {
			return nil, err
		}
		return sellersalesclient.New(conn, salesBreaker), nil
	})
	fdi.Provide(b, serviceListingClient, func(c *fdi.Container) (*homelistingclient.Client, error) {
		platform, err := fdi.ResolveAs[*productPlatform](c, serviceProductPlatform)
		if err != nil {
			return nil, err
		}
		breakers, err := fdi.ResolveAs[productconsumer.Breakers](c, serviceProductPlatformBreakers)
		if err != nil {
			return nil, err
		}
		return homelistingclient.New(platform.Connections(), breakers), nil
	})
	// The auth contract is consumed by two pages, so the client over it is
	// published once and each page adapts it to its own read model. Building a
	// client per page gave one contract two vocabularies and one breaker two
	// sets of counters.
	fdi.Provide(b, serviceAuthClient, func(c *fdi.Container) (*authconsumer.Client, error) {
		conn, err := fdi.ResolveNamedAs[*grpc.ClientConn](c, upstreamAuth)
		if err != nil {
			return nil, err
		}
		authBreaker, err := fdi.ResolveNamedAs[*breaker.Breaker](c, upstreamAuth)
		if err != nil {
			return nil, err
		}
		return authconsumer.New(conn, authBreaker), nil
	})

	// Whether the session cookie is marked Secure is a property of the
	// deployment, so it is decided once here. Hard-coding it in a handler is
	// what left the login page writing a non-Secure cookie with a comment
	// promising someone would fix it.
	fdi.ProvideType(b, func(c *fdi.Container) (session.Policy, error) {
		return session.Policy{Secure: gatewayConfig(c).IsProduction()}, nil
	})

	// The pages' input ports, resolved by type so each page's Register can
	// resolve exactly the capability it declares, without a shared name.
	fdi.ProvideType[homeports.Listing](b, func(c *fdi.Container) (homeports.Listing, error) {
		return fdi.ResolveAs[*homelistingclient.Client](c, serviceListingClient)
	})
	fdi.ProvideType[contentcms.Reader](b, func(c *fdi.Container) (contentcms.Reader, error) {
		cfg := gatewayConfig(c)
		if cfg.BuilderPublicAPIKey == "" {
			return contentcms.DisabledReader(), nil
		}
		return contentcms.NewBuilderReader(cfg.BuilderModel, cfg.BuilderPublicAPIKey)
	})
	fdi.ProvideType[*contentcms.PageHandler](b, func(c *fdi.Container) (*contentcms.PageHandler, error) {
		reader, err := fdi.ResolveType[contentcms.Reader](c)
		if err != nil {
			return nil, err
		}
		return contentcms.NewPageHandler(reader, gatewayLogger(c)), nil
	})
	fdi.ProvideType[sellerports.ProductCatalog](b, func(c *fdi.Container) (sellerports.ProductCatalog, error) {
		return fdi.ResolveAs[*sellercatalogclient.Client](c, serviceSellerCatalogClient)
	})
	fdi.ProvideType[sellerports.Inventory](b, func(c *fdi.Container) (sellerports.Inventory, error) {
		return fdi.ResolveAs[*sellerinventoryclient.Client](c, serviceSellerInventoryClient)
	})
	fdi.ProvideType[sellerports.Sales](b, func(c *fdi.Container) (sellerports.Sales, error) {
		return fdi.ResolveAs[*sellersalesclient.Client](c, serviceSellerSalesClient)
	})
	fdi.ProvideType[sellerports.EmployeeSessions](b, func(c *fdi.Container) (sellerports.EmployeeSessions, error) {
		shared, err := fdi.ResolveAs[*authconsumer.Client](c, serviceAuthClient)
		if err != nil {
			return nil, err
		}
		return sellerauthclient.New(shared), nil
	})
	fdi.ProvideType[authports.Auth](b, func(c *fdi.Container) (authports.Auth, error) {
		shared, err := fdi.ResolveAs[*authconsumer.Client](c, serviceAuthClient)
		if err != nil {
			return nil, err
		}
		return authclient.New(shared), nil
	})
	fdi.ProvideType[profileports.Accounts](b, func(c *fdi.Container) (profileports.Accounts, error) {
		shared, err := fdi.ResolveAs[*authconsumer.Client](c, serviceAuthClient)
		if err != nil {
			return nil, err
		}
		return profileauthclient.New(shared), nil
	})

	// Top-level pages and feature modules own their own graphs: each Register
	// declares the unit keyed by type, so the composition root stays a list of
	// units. The list itself lives in units.go, enforced by units_test.go —
	// including that every unit in the tree offering a Register is declared.
	//
	// Billing adopts the kernel bus contract; ops derives readiness from the
	// registry plus the contributed breaker group, so an instance that cannot
	// reach a dependency it renders from leaves the load balancer.
	registerUnits(b)

	// The gateway itself: resolving it forces the rest of the graph.
	fdi.Provide(b, serviceApp, func(c *fdi.Container) (*httpserver.App, error) {
		registry := gatewayRegistry(c)

		// The pages are resolved first, and that ordering is load-bearing: an
		// in-process connection registers its module as a side effect of being
		// built, so building the pages is what puts catalog and listing in the
		// registry ahead of the feature modules below.
		homePage, err := fdi.ResolveType[*home.Page](c)
		if err != nil {
			return nil, err
		}
		sellerPage, err := fdi.ResolveType[*seller.Page](c)
		if err != nil {
			return nil, err
		}
		authPage, err := fdi.ResolveType[*auth.Page](c)
		if err != nil {
			return nil, err
		}
		profilePage, err := fdi.ResolveType[*profile.Page](c)
		if err != nil {
			return nil, err
		}
		contentPage, err := fdi.ResolveType[*contentcms.PageHandler](c)
		if err != nil {
			return nil, err
		}

		billingModule, err := fdi.ResolveType[*billing.Module](c)
		if err != nil {
			return nil, err
		}
		opsModule, err := fdi.ResolveType[*ops.Module](c)
		if err != nil {
			return nil, err
		}
		registry.AddAll(billingModule, opsModule)

		breakers, err := upstreamBreakers(c)
		if err != nil {
			return nil, err
		}

		return httpserver.NewWithRegistry(
			gatewayConfig(c),
			gatewayLogger(c),
			registry,
			breakers,
			homePage,
			contentPage,
			sellerPage,
			authPage,
			profilePage,
		), nil
	})

	return b.Build()
}

// contributeBreaker registers a breaker under its upstream name and adds it to
// the breaker group, so the two stay in lockstep by construction.
func contributeBreaker(b *fdi.Builder, upstream string) {
	fdi.ProvideNamed(b, upstream, func(*fdi.Container) (*breaker.Breaker, error) {
		return breaker.NewDefault(), nil
	})
	fdi.Contribute(b, upstream, func(c *fdi.Container) (*breaker.Breaker, error) {
		return fdi.ResolveNamedAs[*breaker.Breaker](c, upstream)
	})
}

// resolvePlatformBreakers collects the four product-platform breakers into the bundle
// the shared consumer kit takes. Every one is resolved, so a missing registration fails
// here rather than surfacing later as a nil breaker guarding a live call.
func resolvePlatformBreakers(c *fdi.Container) (productconsumer.Breakers, error) {
	write, err := fdi.ResolveNamedAs[*breaker.Breaker](c, upstreamProduct)
	if err != nil {
		return productconsumer.Breakers{}, err
	}
	browse, err := fdi.ResolveNamedAs[*breaker.Breaker](c, upstreamProductListing)
	if err != nil {
		return productconsumer.Breakers{}, err
	}
	search, err := fdi.ResolveNamedAs[*breaker.Breaker](c, upstreamProductSearch)
	if err != nil {
		return productconsumer.Breakers{}, err
	}
	category, err := fdi.ResolveNamedAs[*breaker.Breaker](c, upstreamCategory)
	if err != nil {
		return productconsumer.Breakers{}, err
	}
	return productconsumer.Breakers{
		Write:    write,
		Browse:   browse,
		Search:   search,
		Category: category,
	}, nil
}

// upstreamBreakers projects the contributed breaker group into the
// name→breaker map the gateway's metrics read. Reading the map off the group is
// what makes a new upstream automatic: one more contribution reaches both the
// metrics and the readiness probe with no edit in the composition root.
func upstreamBreakers(c *fdi.Container) (map[string]*breaker.Breaker, error) {
	members, err := fdi.ResolveGroupAs[*breaker.Breaker](c)
	if err != nil {
		return nil, err
	}
	breakers := make(map[string]*breaker.Breaker, len(members))
	for _, member := range members {
		breakers[member.Name] = member.Value
	}
	return breakers, nil
}

// gatewayBus returns the shared in-process bus from the container.
func gatewayBus(c *fdi.Container) *events.MemoryBus {
	return fdi.MustResolveAs[*events.MemoryBus](c, serviceBus)
}

// gatewayRegistry returns the module registry from the container.
func gatewayRegistry(c *fdi.Container) *module.Registry {
	return fdi.MustResolveType[*module.Registry](c)
}

// gatewayKernelBus returns the kernel.EventBus view over the shared bus.
func gatewayKernelBus(c *fdi.Container) kernel.EventBus {
	return fdi.MustResolveType[kernel.EventBus](c)
}

// gatewayKernelLogger returns the kernel.Logger view over the process logger.
func gatewayKernelLogger(c *fdi.Container) kernel.Logger {
	return fdi.MustResolveType[kernel.Logger](c)
}

// gatewayParent returns the process context the graph was built around.
func gatewayParent(c *fdi.Container) context.Context {
	return fdi.MustResolveAs[context.Context](c, serviceParentContext)
}

// gatewayConfig returns the process configuration from the container.
func gatewayConfig(c *fdi.Container) *config.AppConfig {
	return fdi.MustResolveAs[*config.AppConfig](c, serviceConfig)
}

// gatewayLogger returns the process logger from the container.
func gatewayLogger(c *fdi.Container) *slog.Logger {
	return fdi.MustResolveAs[*slog.Logger](c, serviceLogger)
}

// gatewayLifecycle returns the shutdown accumulator from the container.
func gatewayLifecycle(c *fdi.Container) *fdi.Lifecycle {
	return fdi.MustResolveAs[*fdi.Lifecycle](c, serviceLifecycle)
}

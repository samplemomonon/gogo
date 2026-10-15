package httpserver

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"gogo/internal/shared/breaker"
	"gogo/internal/shared/config"
	"gogo/internal/shared/module"
	"gogo/internal/shared/netutil"
	"gogo/internal/shared/tenant"
	"gogo/internal/shared/web/assets"
	"gogo/internal/shared/web/navigation"
	"gogo/internal/shared/web/shell"
)

// Page is a top-level page of the gateway: a plain HTTP surface without a
// module manifest or lifecycle. Pages are composition-root artifacts rather
// than independently deployable capabilities — the public home page at "/"
// and the seller console at "/seller" are pages, not modules.
type Page interface {
	// RegisterRoutes mounts the page's routes on the gateway router.
	RegisterRoutes(r chi.Router)

	// Nav returns the shell navigation items the page contributes. The
	// gateway renders them ahead of the module-federated items
	// (registry.Nav), so page entry points always lead the menu.
	Nav() []navigation.Item
}

// App is the gateway: it owns the module registry, mounts the default
// middleware stack and static assets, lets every registered module mount
// its own routes and exposes the federated registry at /module-registry.
// Top-level pages (the home page, the seller console) register their routes
// after the modules and contribute their own nav items to the shared shell.
type App struct {
	cfg      *config.AppConfig
	logger   *slog.Logger
	registry *module.Registry
	router   chi.Router
}

// New builds an App from the given modules, wrapping them in a fresh
// registry. The default middleware stack provides request IDs, real IP
// detection, structured logging, panic recovery, compression and
// per-request timeouts. Static assets are served from /assets and the
// module registry from /module-registry.
func New(cfg *config.AppConfig, logger *slog.Logger, modules ...module.Interface) *App {
	return NewWithRegistry(cfg, logger, module.New(cfg.ServiceName, cfg.ServiceVersion, modules...), nil)
}

// NewWithRegistry builds an App around an already-populated registry. Use
// it when the composition root must hold the registry first — e.g. to wire
// the ops module's aggregated readiness probe against registry.Ready (see
// cmd/server/main.go). pages are the gateway's top-level pages; each one
// mounts its routes after the module routes and contributes its nav items
// to the shell, ahead of the module-federated nav. breakers maps a dependency
// name to the client circuit breaker guarding that dependency, so every
// upstream service this process calls is reported under its own metric
// prefix; nil is valid.
func NewWithRegistry(cfg *config.AppConfig, logger *slog.Logger, registry *module.Registry, breakers map[string]*breaker.Breaker, pages ...Page) *App {
	return newApp(cfg, logger, registry, breakers, pages)
}

func newApp(cfg *config.AppConfig, logger *slog.Logger, registry *module.Registry, breakers map[string]*breaker.Breaker, pages []Page) *App {
	metrics := NewMetrics(breakers)
	router := buildRouter(cfg, logger, registry, pages, metrics)

	return &App{
		cfg:      cfg,
		logger:   logger,
		registry: registry,
		router:   router,
	}
}

// buildRouter assembles the gateway's chi router: the default middleware
// stack (request IDs, real IP, structured logging, recovery, security
// headers, body limits, compression, timeouts), the federated nav injection,
// observability endpoints, static assets and every module and page route.
func buildRouter(
	cfg *config.AppConfig,
	logger *slog.Logger,
	registry *module.Registry,
	pages []Page,
	metrics *Metrics,
) chi.Router {
	router := chi.NewRouter()

	// Metrics run first so every request below is counted regardless of how
	// downstream middleware handles it.
	router.Use(metrics.Middleware)
	router.Use(middleware.RequestID)
	// RealIP only honors X-Forwarded-For from configured trusted proxies, so
	// a public client cannot spoof its address; it must run before the
	// logger for access logs to carry the resolved client. The trusted set
	// is parsed once here (config.Validate has already rejected bad CIDRs).
	trustedProxies, err := netutil.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		// Defensive: config.Validate should have caught this. Fail closed
		// (trust nothing) rather than trust a malformed set, and surface it.
		logger.Error("ignoring malformed trusted proxies", "error", err)
		trustedProxies, _ = netutil.ParseTrustedProxies(nil)
	}
	router.Use(RealIP(trustedProxies))
	router.Use(tenant.Middleware(tenantResolver(cfg, logger), logger))
	router.Use(Logger(logger, cfg.SlowRequest))
	router.Use(middleware.Recoverer)
	router.Use(SecurityHeaders(cfg.IsProduction()))
	router.Use(BodyLimit(cfg.MaxBodyBytes))
	router.Use(middleware.Compress(5, "text/html", "text/css", "application/javascript"))
	router.Use(middleware.Timeout(cfg.RequestTimeout))

	// The shell navigation is federated from two sources: the gateway's
	// top-level pages (page.Nav, which always lead the menu) and the
	// modules' manifests (registry.Nav). The gateway injects the compiled
	// menu into every request so the shell renders it without knowing any
	// page's or module's internals. The request path rides along so the
	// shell can highlight the active nav item.
	nav := compileNav(pages, registry)
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := navigation.WithNav(r.Context(), nav)
			ctx = navigation.WithPath(ctx, r.URL.Path)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})

	router.Mount("/assets", assets.Handler())
	router.Mount("/module-registry", registryHandler(registry, logger))
	router.Mount("/metrics", metrics.Handler())
	// expvar exposes runtime internals on the public surface; it is
	// deliberately absent in production unless explicitly enabled.
	if cfg.EnableDebugEndpoint {
		router.Handle("/debug/vars", expvar.Handler())
	}

	// The gateway does not route for modules — each module mounts its own
	// routes and declares them in its manifest. New modules deploy
	// independently: registering them here is the only shell-side change.
	for _, mod := range registry.Modules() {
		mod.RegisterRoutes(router)
		logger.Info("module registered",
			"module", mod.Manifest().Name,
			"version", mod.Manifest().Version,
			"entry", mod.Manifest().Entry,
		)
	}

	// Pages come after modules: they are plain routes (no manifest), so
	// mounting them is the whole of their gateway contract. Page nav items
	// were already compiled into the shared menu above.
	for _, page := range pages {
		page.RegisterRoutes(router)
	}

	return router
}

// tenantResolver builds the request→tenant resolver from configuration. A
// malformed declaration should have failed config.Validate at startup; should
// one slip through, fail closed to a default-only registry so the process
// still serves the default tenant rather than guessing a boundary.
func tenantResolver(cfg *config.AppConfig, logger *slog.Logger) tenant.Resolver {
	hosts := make(map[string]tenant.ID, len(cfg.Tenant.Hosts))
	for host, id := range cfg.Tenant.Hosts {
		hosts[host] = tenant.ID(id)
	}
	ids := make([]tenant.ID, 0, len(cfg.Tenant.IDs))
	for _, id := range cfg.Tenant.IDs {
		ids = append(ids, tenant.ID(id))
	}

	reg, err := tenant.NewRegistry(tenant.Options{
		DefaultID: tenant.ID(cfg.Tenant.DefaultID),
		Hosts:     hosts,
		IDs:       ids,
	})
	if err != nil {
		logger.Error("ignoring malformed tenant declaration", "error", err)
		reg, _ = tenant.NewRegistry(tenant.Options{DefaultID: tenant.Default})
	}
	return tenant.NewHostResolver(reg, cfg.Tenant.TrustHeader)
}

// compileNav merges the pages' nav (which always lead the menu) with the
// module-federated items from the registry, preserving each source's registration
// order. Every destination in the shell therefore belongs to a registered page or
// module — there is no hardcoded list to fall out of step with the routes that exist.
func compileNav(pages []Page, registry *module.Registry) []navigation.Item {
	nav := make([]navigation.Item, 0, len(pages))
	for _, page := range pages {
		nav = append(nav, page.Nav()...)
	}
	return append(nav, registry.Nav()...)
}

// Registry exposes the module registry, e.g. to middleware or tests.
func (a *App) Registry() *module.Registry {
	return a.registry
}

// registryHandler serves the federated module registry: the JSON contract
// for API clients and a rendered HTML page for browsers. Both go through
// the same document shell, so the registry page carries the federated nav
// like every other page.
func registryHandler(registry *module.Registry, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// The registry is read-only on both representations; reject other
		// methods up front so the HTML branch cannot bypass the JSON
		// handler's own method check.
		if req.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if wantsHTML(req) {
			page := shell.RegistryPage(
				registry.Service(),
				registry.Version(),
				registryViews(registry.Manifests()),
			)
			if err := page.Render(req.Context(), w); err != nil {
				logger.Error("failed to render module registry", "error", err)
				http.Error(w, "failed to render registry", http.StatusInternalServerError)
			}
			return
		}
		registry.Handler().ServeHTTP(w, req)
	})
}

// registryViews maps the federation manifests to the shell's render model.
// The mapping lives here, at the gateway composition boundary, so the shell
// never imports the module contract — it only renders plain data.
func registryViews(manifests []module.Descriptor) []shell.RegistryView {
	views := make([]shell.RegistryView, 0, len(manifests))
	for _, manifest := range manifests {
		views = append(views, shell.RegistryView{
			Name:        manifest.Name,
			Version:     manifest.Version,
			Entry:       manifest.Entry,
			Description: manifest.Description,
		})
	}
	return views
}

// wantsHTML reports whether the request accepts text/html (i.e. a browser)
// so the gateway can serve the registry's human-readable view.
func wantsHTML(req *http.Request) bool {
	for _, value := range req.Header.Values("Accept") {
		for _, part := range strings.Split(value, ",") {
			kind := part
			if idx := strings.IndexByte(kind, ';'); idx >= 0 {
				kind = kind[:idx]
			}
			if strings.TrimSpace(kind) == "text/html" {
				return true
			}
		}
	}
	return false
}

// Handler returns the fully configured http.Handler.
func (a *App) Handler() http.Handler {
	return a.router
}

// Run starts the server and blocks until ctx is cancelled, then performs
// a graceful shutdown bounded by ShutdownTimeout. Modules implementing the
// optional module.Lifer lifecycle are started before the server listens and
// stopped during shutdown, in reverse registration order.
func (a *App) Run(ctx context.Context) error {
	// Start is atomic: on any failure it rolls back the modules it already
	// started, so there is nothing left running when it returns an error and
	// no defer is needed on this path.
	if err := a.registry.Start(ctx); err != nil {
		return fmt.Errorf("gateway: start modules: %w", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(
			context.Background(),
			a.cfg.ShutdownTimeout,
		)
		defer cancel()
		if err := a.registry.Stop(stopCtx); err != nil {
			a.logger.Error("module shutdown failed", "error", err)
		}
	}()

	addr := net.JoinHostPort(a.cfg.Host, a.cfg.Port)

	server := &http.Server{
		Addr:              addr,
		Handler:           a.router,
		ReadTimeout:       a.cfg.ReadTimeout,
		ReadHeaderTimeout: a.cfg.ReadHeaderTimeout,
		WriteTimeout:      a.cfg.WriteTimeout,
		IdleTimeout:       a.cfg.IdleTimeout,
		// Override only when configured; zero keeps the net/http default of
		// 1 MiB, bounding header floods on a slow-loris-style attempt.
		MaxHeaderBytes: a.cfg.MaxHeaderBytes,
		// Route the http.Server's own errors (accept/TLS timeout failures)
		// through the same structured logger as everything else.
		ErrorLog: slog.NewLogLogger(a.logger.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() {
		a.logger.Info("server listening",
			"addr", addr,
			"env", a.cfg.AppEnv,
		)
		if err := server.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		a.logger.Info("shutting down server")

		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			a.cfg.ShutdownTimeout,
		)
		defer cancel()

		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		// The listener failed on its own. Close it cleanly before returning
		// so a partially-listening server cannot linger, then surface the
		// cause.
		_ = server.Close()
		return err
	}
}

package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"gogo/internal/shared/config"
	"gogo/internal/shared/module"
	"gogo/internal/shared/web/navigation"
	"gogo/internal/shared/web/site"
)

// fixtureModule is a module with an observable lifecycle and an optional
// health report, used to exercise the gateway's orchestration.
type fixtureModule struct {
	name   string
	health module.Health

	mu       sync.Mutex
	started  bool
	stopped  bool
	startedC chan struct{}
	stoppedC chan struct{}
}

func newFixture(name string) *fixtureModule {
	return &fixtureModule{
		name:     name,
		health:   module.Health{Status: module.StatusOK},
		startedC: make(chan struct{}),
		stoppedC: make(chan struct{}),
	}
}

func (f *fixtureModule) Manifest() module.Descriptor {
	return module.Descriptor{
		Name:    f.name,
		Version: "1.0.0",
		Entry:   "/" + f.name,
		Nav:     []navigation.Item{{Label: f.name, Path: "/" + f.name}},
	}
}

func (f *fixtureModule) RegisterRoutes(r chi.Router) {
	r.Get("/"+f.name, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func (f *fixtureModule) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	close(f.startedC)
	return nil
}

func (f *fixtureModule) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	close(f.stoppedC)
	return nil
}

func (f *fixtureModule) Healthy(context.Context) module.Health {
	f.mu.Lock()
	defer f.mu.Unlock()
	report := f.health
	if report.Module == "" {
		report.Module = f.name
	}
	return report
}

func (f *fixtureModule) wasStarted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

func (f *fixtureModule) wasStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

var _ module.Lifer = (*fixtureModule)(nil)
var _ module.HealthReporter = (*fixtureModule)(nil)

func testConfig() *config.AppConfig {
	return &config.AppConfig{
		AppEnv:          "test",
		Host:            "127.0.0.1",
		Port:            "0",
		ServiceName:     "gogo",
		ServiceVersion:  "test",
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		IdleTimeout:     time.Second,
		RequestTimeout:  time.Second,
		ShutdownTimeout: time.Second,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// healthProbeModule is a local stand-in for the ops module: it exposes
// liveness/readiness endpoints backed by a readiness probe, so the gateway's
// health-aggregation behavior can be tested without the shared package
// importing a feature module (which would invert the dependency direction).
type healthProbeModule struct {
	probe func(ctx context.Context) error
}

func (m *healthProbeModule) Manifest() module.Descriptor {
	return module.Descriptor{Name: "ops", Version: "1.0.0", Entry: "/healthz"}
}

func (m *healthProbeModule) RegisterRoutes(r chi.Router) {
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if m.probe != nil {
			if err := m.probe(r.Context()); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (m *healthProbeModule) Healthy(ctx context.Context) module.Health {
	if m.probe != nil {
		if err := m.probe(ctx); err != nil {
			return module.Health{Module: "ops", Status: module.StatusDown, Message: err.Error()}
		}
	}
	return module.Health{Module: "ops", Status: module.StatusOK}
}

var _ module.HealthReporter = (*healthProbeModule)(nil)

func newTestApp(t *testing.T, reg *module.Registry) *App {
	t.Helper()
	return NewWithRegistry(testConfig(), testLogger(), reg, nil)
}

func TestReadinessAggregatesModuleHealth(t *testing.T) {
	t.Parallel()

	reg := module.NewRegistry("gogo", "test")
	reg.AddAll(newFixture("catalog"), newFixture("billing"))

	// The ops module probes the registry's aggregate health (excluding
	// itself), exactly like the composition root wires it.
	reg.Add(&healthProbeModule{probe: func(ctx context.Context) error {
		return reg.ReadyExcluding(ctx, "ops")
	}})

	app := newTestApp(t, reg)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d, want 200 (all modules healthy)", rec.Code)
	}

	// Break a module: readiness must fail while liveness stays up.
	reg.Modules()[0].(*fixtureModule).health = module.Health{
		Module:  "catalog",
		Status:  module.StatusDown,
		Message: "no database",
	}

	rec2 := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status after break = %d, want 503", rec2.Code)
	}

	rec3 := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec3.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200 even when readiness fails", rec3.Code)
	}
}

func TestRunStartsAndStopsModules(t *testing.T) {
	t.Parallel()

	first := newFixture("catalog")
	second := newFixture("billing")
	reg := module.NewRegistry("gogo", "test")
	reg.AddAll(first, second)

	app := newTestApp(t, reg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	select {
	case <-first.startedC:
	case <-time.After(2 * time.Second):
		t.Fatal("module was not started before the server ran")
	}
	select {
	case <-second.startedC:
	case <-time.After(2 * time.Second):
		t.Fatal("second module was not started before the server ran")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error after shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	// Modules must be stopped on graceful shutdown.
	select {
	case <-first.stoppedC:
	case <-time.After(2 * time.Second):
		t.Fatal("module was not stopped during shutdown")
	}
	select {
	case <-second.stoppedC:
	case <-time.After(2 * time.Second):
		t.Fatal("second module was not stopped during shutdown")
	}
}

// fixturePage is a minimal top-level page: a route and one nav item. It is
// the unit the gateway tests pages against (home → "/", admin → "/admin").
type fixturePage struct {
	path string
	item navigation.Item
}

func (p fixturePage) RegisterRoutes(r chi.Router) {
	r.Get(p.path, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func (p fixturePage) Nav() []navigation.Item {
	return []navigation.Item{p.item}
}

var _ Page = fixturePage{}

// shellSitePage contributes a route that echoes the request-scoped shell
// settings, proving the gateway's optional provider runs before page handlers.
type shellSitePage struct {
	settings site.Settings
	err      error
}

func (p shellSitePage) RegisterRoutes(r chi.Router) {
	r.Get("/site-shell", func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write([]byte(site.FromContext(req.Context()).SiteName))
	})
	r.Get("/admin/site-shell", func(w http.ResponseWriter, req *http.Request) {
		settings := site.FromContext(req.Context())
		if settings.UseDefaultNavigation {
			_, _ = w.Write([]byte("federated"))
			return
		}
		_, _ = w.Write([]byte("cms"))
	})
}

func (shellSitePage) Nav() []navigation.Item { return nil }

func (p shellSitePage) SiteSettings(context.Context) (site.Settings, error) {
	return p.settings, p.err
}

var _ SiteSettingsProvider = shellSitePage{}

func TestPublishedSiteSettingsReachPageHandlers(t *testing.T) {
	t.Parallel()
	app := NewWithRegistry(
		testConfig(), testLogger(), module.NewRegistry("gogo", "test"), nil,
		shellSitePage{settings: site.Settings{SiteName: "Toko Baru", HomePageSlug: "home", FooterText: "footer"}},
	)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/site-shell", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "Toko Baru" {
		t.Fatalf("site shell response = %d %q, want Toko Baru", rec.Code, rec.Body.String())
	}
}

func TestApplicationSurfacesKeepFederatedNavigation(t *testing.T) {
	t.Parallel()
	app := NewWithRegistry(
		testConfig(), testLogger(), module.NewRegistry("gogo", "test"), nil,
		shellSitePage{settings: site.Settings{
			SiteName: "Toko Baru", HomePageSlug: "home",
			HeaderLinks: []site.Link{{Label: "Beranda", Href: "/"}},
		}},
	)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/site-shell", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "federated" {
		t.Fatalf("admin shell navigation = %d %q, want federated", rec.Code, rec.Body.String())
	}
}

func TestSiteProviderFailureFallsBackToDefaultShell(t *testing.T) {
	t.Parallel()
	app := NewWithRegistry(
		testConfig(), testLogger(), module.NewRegistry("gogo", "test"), nil,
		shellSitePage{err: context.DeadlineExceeded},
	)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/site-shell", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != site.Default().SiteName {
		t.Fatalf("fallback response = %d %q, want %q", rec.Code, rec.Body.String(), site.Default().SiteName)
	}
}

func TestPagesRegisteredAheadOfModuleNav(t *testing.T) {
	t.Parallel()

	reg := module.NewRegistry("gogo", "test")
	reg.Add(newFixture("billing"))

	app := NewWithRegistry(
		testConfig(),
		testLogger(),
		reg,
		nil,
		fixturePage{path: "/", item: navigation.Item{Label: "Home", Path: "/"}},
		fixturePage{path: "/admin", item: navigation.Item{Label: "Admin", Path: "/admin"}},
	)

	// Page routes are mounted after the module routes, exactly like the
	// production home ("/") and admin ("/admin") pages.
	for _, path := range []string{"/", "/admin"} {
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("page %s status = %d, want 200", path, rec.Code)
		}
	}

	// The shell menu is compiled with page nav first, then the module nav:
	// pages lead the top bar, just as the production root orders Home, Admin,
	// then the module-federated items. The registry page renders through the
	// shell, so its nav bar exposes the ordering.
	req := httptest.NewRequest(http.MethodGet, "/module-registry", nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	home := strings.Index(body, `href="/"`)
	admin := strings.Index(body, `href="/admin"`)
	billing := strings.Index(body, `href="/billing"`)
	if home == -1 || admin == -1 || billing == -1 {
		t.Fatalf("shell nav missing items: home=%d admin=%d billing=%d", home, admin, billing)
	}
	if !(home < admin && admin < billing) {
		t.Errorf("nav order = home(%d) admin(%d) billing(%d), want home < admin < billing", home, admin, billing)
	}
}

func TestRegistryMountedInGateway(t *testing.T) {
	t.Parallel()

	reg := module.NewRegistry("gogo", "test")
	reg.Add(newFixture("catalog"))

	app := newTestApp(t, reg)

	req := httptest.NewRequest(http.MethodGet, "/module-registry", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"catalog"`) {
		t.Errorf("registry JSON missing catalog manifest: %s", rec.Body.String())
	}

	// Browsers get the rendered registry page through the shared shell.
	req = httptest.NewRequest(http.MethodGet, "/module-registry", nil)
	req.Header.Set("Accept", "text/html")
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("html status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Error("browser registry page should render through the shell")
	}
}

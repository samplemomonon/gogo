package ops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"gogo/internal/shared/breaker"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/module"
)

// reportedModule is a registered module that volunteers a fixed health report,
// so a test can drive the registry's aggregation.
type reportedModule struct {
	moduleName string
	health     module.Health
}

func (m reportedModule) Manifest() module.Descriptor {
	return module.Descriptor{Name: m.moduleName}
}

func (reportedModule) RegisterRoutes(chi.Router) {}

func (m reportedModule) Healthy(context.Context) module.Health { return m.health }

// tripOpen returns a breaker already open: one failure against a threshold of
// one, so the state is deterministic without waiting on a cooldown.
func tripOpen(t *testing.T) *breaker.Breaker {
	t.Helper()
	b := breaker.New(1, time.Minute, 1)
	if err := b.Execute(func() error { return errors.New("dial refused") }); err == nil {
		t.Fatal("Execute succeeded, so the breaker never tripped")
	}
	if b.State() != breaker.StateOpen {
		t.Fatalf("breaker state = %v, want open", b.State())
	}
	return b
}

// TestReadinessNamesTheUnreachableUpstream pins the reason ops depends on the
// contributed breaker group: readiness reports on every upstream the root
// wired, and the failure says *which* one is unreachable — without ops knowing
// the set of upstream names in advance.
func TestReadinessNamesTheUnreachableUpstream(t *testing.T) {
	t.Parallel()

	registry := module.NewRegistry("gogo", "test")
	registry.Add(reportedModule{
		moduleName: "catalog",
		health:     module.Health{Module: "catalog", Status: module.StatusOK},
	})

	probe := Readiness(registry, []fdi.Member[*breaker.Breaker]{
		{Name: "catalog", Value: tripOpen(t)},
	}, ReadinessPolicy{})

	err := probe(context.Background())
	if err == nil {
		t.Fatal("Readiness reported ready while an upstream breaker was open")
	}
	if !strings.Contains(err.Error(), "catalog") {
		t.Errorf("error %q does not name the unreachable upstream", err)
	}
}

// TestReadinessSkipsOptionalUpstreams pins the classification: an open breaker
// on an optional upstream degrades the surfaces that need it without taking the
// instance out of rotation, while a required upstream's open breaker still
// fails readiness and is named in the error.
func TestReadinessSkipsOptionalUpstreams(t *testing.T) {
	t.Parallel()

	registry := module.NewRegistry("gogo", "test")
	registry.Add(reportedModule{
		moduleName: "catalog",
		health:     module.Health{Module: "catalog", Status: module.StatusOK},
	})

	policy := ReadinessPolicy{Optional: []string{"search"}}

	probe := Readiness(registry, []fdi.Member[*breaker.Breaker]{
		{Name: "browse", Value: breaker.NewDefault()},
		{Name: "search", Value: tripOpen(t)},
	}, policy)
	if err := probe(context.Background()); err != nil {
		t.Errorf("Readiness = %v, want ready while only an optional upstream is down", err)
	}

	probe = Readiness(registry, []fdi.Member[*breaker.Breaker]{
		{Name: "browse", Value: tripOpen(t)},
		{Name: "search", Value: tripOpen(t)},
	}, policy)
	err := probe(context.Background())
	if err == nil {
		t.Fatal("Readiness reported ready while a required upstream breaker was open")
	}
	if !strings.Contains(err.Error(), "browse") {
		t.Errorf("error %q does not name the required upstream", err)
	}
}

// TestReadinessPassesWhenEveryUpstreamIsClosed pins that a closed breaker set
// contributes nothing to the verdict, so readiness is not permanently down
// just because upstreams are wired.
func TestReadinessPassesWhenEveryUpstreamIsClosed(t *testing.T) {
	t.Parallel()

	registry := module.NewRegistry("gogo", "test")
	registry.Add(reportedModule{
		moduleName: "catalog",
		health:     module.Health{Module: "catalog", Status: module.StatusOK},
	})

	probe := Readiness(registry, []fdi.Member[*breaker.Breaker]{
		{Name: "catalog", Value: breaker.NewDefault()},
		{Name: "listing", Value: breaker.NewDefault()},
	}, ReadinessPolicy{})

	if err := probe(context.Background()); err != nil {
		t.Errorf("Readiness = %v, want ready with closed breakers", err)
	}
}

// TestReadinessPassesWithNoUpstreams pins that a root wiring no upstreams still
// yields a ready probe, matching the empty-group contract the DI group
// guarantees.
func TestReadinessPassesWithNoUpstreams(t *testing.T) {
	t.Parallel()

	probe := Readiness(module.NewRegistry("gogo", "test"), nil, ReadinessPolicy{})

	if err := probe(context.Background()); err != nil {
		t.Errorf("Readiness = %v, want ready with no upstreams", err)
	}
}

// TestReadinessExcludesOpsOwnReport pins the anti-recursion property: ops is
// itself a registered module, so a probe that aggregated *every* module would
// call itself. Readiness must exclude the manifest name ops registered under.
func TestReadinessExcludesOpsOwnReport(t *testing.T) {
	t.Parallel()

	registry := module.NewRegistry("gogo", "test")
	registry.Add(NewModuleWithProbe(func(context.Context) error {
		return errors.New("ops is down")
	}))

	probe := Readiness(registry, nil, ReadinessPolicy{})

	if err := probe(context.Background()); err != nil {
		t.Errorf("Readiness = %v, want the probe to exclude ops from its own aggregation", err)
	}
}

// TestReadinessFailsWhenAnotherModuleIsDown pins that the registry half of the
// probe still does its job: an unhealthy peer takes the instance out of
// rotation even when every upstream breaker is closed.
func TestReadinessFailsWhenAnotherModuleIsDown(t *testing.T) {
	t.Parallel()

	registry := module.NewRegistry("gogo", "test")
	registry.Add(reportedModule{
		moduleName: "billing",
		health:     module.Health{Module: "billing", Status: module.StatusDown, Message: "db unreachable"},
	})

	probe := Readiness(registry, []fdi.Member[*breaker.Breaker]{
		{Name: "catalog", Value: breaker.NewDefault()},
	}, ReadinessPolicy{})

	if err := probe(context.Background()); err == nil {
		t.Error("Readiness reported ready while another module was down")
	}
}

func newTestRouter() (chi.Router, *Module) {
	router := chi.NewRouter()
	module := NewModule()
	module.RegisterRoutes(router)
	return router, module
}

func newTestRouterWithProbe(probe Probe) (chi.Router, *Module) {
	router := chi.NewRouter()
	module := NewModuleWithProbe(probe)
	module.RegisterRoutes(router)
	return router, module
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	router, _ := newTestRouter()

	for _, tt := range []struct {
		path string
		want string
	}{
		{"/healthz", "ok"},
		{"/readyz", "ready"},
	} {
		tt := tt
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if body := rec.Body.String(); body != tt.want+"\n" {
				t.Errorf("body = %q, want %q", body, tt.want+"\n")
			}
		})
	}
}

func TestReadinessWithFailingProbe(t *testing.T) {
	t.Parallel()

	router, _ := newTestRouterWithProbe(func(context.Context) error {
		return errors.New("catalog not ready")
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); body != "not ready\n" {
		t.Errorf("body = %q, want %q", body, "not ready\n")
	}

	// Liveness is unaffected by the readiness probe.
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("liveness status = %d, want 200", rec.Code)
	}
}

func TestHealthyReport(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	ready := NewModuleWithProbe(func(context.Context) error { return nil })
	if report := ready.Healthy(ctx); report.Status != module.StatusOK {
		t.Errorf("Healthy(ready) = %+v, want ok", report)
	}

	down := NewModuleWithProbe(func(context.Context) error { return errors.New("db unreachable") })
	report := down.Healthy(ctx)
	if report.Status != module.StatusDown || report.Message == "" {
		t.Errorf("Healthy(down) = %+v, want down with a message", report)
	}
	if report.Ready() {
		t.Error("a down report must not be ready")
	}
}

func TestManifest(t *testing.T) {
	t.Parallel()

	_, module := newTestRouter()

	manifest := module.Manifest()
	if manifest.Name != "ops" {
		t.Errorf("Name = %q, want ops", manifest.Name)
	}
	if manifest.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", manifest.Version)
	}
	if manifest.Entry != "/healthz" {
		t.Errorf("Entry = %q, want /healthz", manifest.Entry)
	}
	if _, ok := manifest.Routes["readiness"]; !ok {
		t.Error("manifest should declare the readiness route")
	}
}

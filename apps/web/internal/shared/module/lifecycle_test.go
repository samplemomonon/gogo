package module

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// lifecycleFixture records Start/Stop calls and can be made to fail or be
// unhealthy on demand.
type lifecycleFixture struct {
	fixtureModule

	startCalls *[]string
	stopCalls  *[]string
	startErr   error
	stopErr    error
	health     *Health
}

func (l *lifecycleFixture) Start(context.Context) error {
	if l.startCalls != nil {
		*l.startCalls = append(*l.startCalls, l.Manifest().Name)
	}
	return l.startErr
}

func (l *lifecycleFixture) Stop(context.Context) error {
	if l.stopCalls != nil {
		*l.stopCalls = append(*l.stopCalls, l.Manifest().Name)
	}
	return l.stopErr
}

func (l *lifecycleFixture) Healthy(context.Context) Health {
	if l.health != nil {
		return *l.health
	}
	return Health{Module: l.Manifest().Name, Status: StatusOK}
}

func TestRegistryStartStopOrder(t *testing.T) {
	t.Parallel()

	var started, stopped []string
	first := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "catalog", Entry: "/"}},
		startCalls:    &started,
		stopCalls:     &stopped,
	}
	second := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "admin", Entry: "/admin"}},
		startCalls:    &started,
		stopCalls:     &stopped,
	}

	registry := New("gogo", "dev", first, second)

	if err := registry.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := registry.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if strings.Join(started, ",") != "catalog,admin" {
		t.Errorf("start order = %v, want [catalog admin]", started)
	}
	// Stop runs in reverse registration order so dependents stop before
	// their dependencies.
	if strings.Join(stopped, ",") != "admin,catalog" {
		t.Errorf("stop order = %v, want [admin catalog]", stopped)
	}
}

func TestRegistryStartStopErrorAggregation(t *testing.T) {
	t.Parallel()

	var started []string
	bad := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "bad", Entry: "/bad"}},
		startErr:      errors.New("boom"),
		stopErr:       errors.New("down"),
		startCalls:    &started,
	}
	good := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "good", Entry: "/good"}},
		startCalls:    &started,
	}

	registry := New("gogo", "dev", bad, good)

	err := registry.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), `module "bad" start: boom`) {
		t.Errorf("Start error = %v, want aggregated failure naming bad", err)
	}
	// A failing module must not stop the remaining modules from starting.
	if strings.Join(started, ",") != "bad,good" {
		t.Errorf("start order = %v, want both modules attempted", started)
	}

	if err := registry.Stop(context.Background()); err == nil ||
		!strings.Contains(err.Error(), `module "bad" stop: down`) {
		t.Errorf("Stop error = %v, want aggregated failure naming bad", err)
	}
}

func TestRegistryModulesWithoutLifecycleAreNoOps(t *testing.T) {
	t.Parallel()

	// A plain module (no Lifer, no HealthReporter) must not break lifecycle
	// or readiness: no start/stop, always ready.
	registry := New("gogo", "dev", fixtureModule{manifest: Descriptor{Name: "plain", Entry: "/"}})

	if err := registry.Start(context.Background()); err != nil {
		t.Errorf("Start with plain module: %v", err)
	}
	if err := registry.Stop(context.Background()); err != nil {
		t.Errorf("Stop with plain module: %v", err)
	}
	if err := registry.Ready(context.Background()); err != nil {
		t.Errorf("Ready with plain module: %v", err)
	}

	reports := registry.Health(context.Background())
	if len(reports) != 1 || reports[0].Status != StatusOK {
		t.Errorf("Health = %+v, want single ok report", reports)
	}
}

func TestRegistryHealthAggregation(t *testing.T) {
	t.Parallel()

	healthy := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "up", Entry: "/"}},
		health:        &Health{Module: "up", Status: StatusOK},
	}
	degraded := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "slow", Entry: "/"}},
		health:        &Health{Module: "slow", Status: StatusDegraded, Message: "cache warming"},
	}
	down := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "broken", Entry: "/"}},
		health:        &Health{Module: "broken", Status: StatusDown, Message: "db unreachable"},
	}
	plain := fixtureModule{manifest: Descriptor{Name: "opslike", Entry: "/"}}

	registry := New("gogo", "dev", healthy, degraded, down, plain)

	reports := registry.Health(context.Background())
	if len(reports) != 4 {
		t.Fatalf("len(Health) = %d, want 4", len(reports))
	}
	if reports[3].Status != StatusOK || reports[3].Module != "opslike" {
		t.Errorf("default report = %+v, want opslike ok", reports[3])
	}

	err := registry.Ready(context.Background())
	if err == nil {
		t.Fatal("Ready should fail when a module is down")
	}
	if !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "db unreachable") {
		t.Errorf("Ready error = %v, want to name the failing module", err)
	}
	// A degraded module is still ready to serve traffic.
	if strings.Contains(err.Error(), "slow") {
		t.Errorf("Ready error = %v, should not name a degraded module", err)
	}
}

func TestRegistryReadyExcludingSelf(t *testing.T) {
	t.Parallel()

	down := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "broken", Entry: "/"}},
		health:        &Health{Module: "broken", Status: StatusDown, Message: "db unreachable"},
	}
	opsLike := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "ops", Entry: "/healthz"}},
		health:        &Health{Module: "ops", Status: StatusDown, Message: "probe recursion"},
	}

	registry := New("gogo", "dev", down, opsLike)

	// ReadyExcluding lets a module's own probe aggregate the OTHER modules
	// without recursing into its own health report.
	if err := registry.ReadyExcluding(context.Background(), "ops"); err == nil ||
		!strings.Contains(err.Error(), "broken") {
		t.Errorf("ReadyExcluding(ops) = %v, want the other failing module only", err)
	} else if strings.Contains(err.Error(), "ops") {
		t.Errorf("ReadyExcluding(ops) = %v, should not include self", err)
	}
}

// probeFixture mirrors the real ops module: its health report is derived
// live from an external probe rather than a precomputed value.
type probeFixture struct {
	fixtureModule
	probe func(context.Context) error
}

func (p *probeFixture) Healthy(ctx context.Context) Health {
	if p.probe == nil {
		return Health{Module: p.Manifest().Name, Status: StatusOK}
	}
	if err := p.probe(ctx); err != nil {
		return Health{Module: p.Manifest().Name, Status: StatusDown, Message: err.Error()}
	}
	return Health{Module: p.Manifest().Name, Status: StatusOK}
}

func TestRegistryReadyExcludingDoesNotReenterSelf(t *testing.T) {
	t.Parallel()

	// Regression: ReadyExcluding must not invoke the self module's
	// HealthReporter at all. Before the fix it collected every report first
	// (including "ops") before filtering, and a self-reporter that probes
	// the registry recursed into itself until the stack overflowed.
	var registry *Registry
	opsLike := &probeFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "ops", Entry: "/healthz"}},
		// The ops probe aggregates the rest of the gateway, excluding itself.
		probe: func(ctx context.Context) error {
			return registry.ReadyExcluding(ctx, "ops")
		},
	}
	down := &lifecycleFixture{
		fixtureModule: fixtureModule{manifest: Descriptor{Name: "broken", Entry: "/"}},
		health:        &Health{Module: "broken", Status: StatusDown, Message: "db unreachable"},
	}
	registry = New("gogo", "dev", down, opsLike)

	// ReadyExcluding must report the OTHER module's failure and terminate.
	err := registry.ReadyExcluding(context.Background(), "ops")
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("ReadyExcluding(ops) = %v, want failure naming broken only", err)
	} else if strings.Contains(err.Error(), "ops") {
		t.Errorf("ReadyExcluding(ops) = %v, must not include self", err)
	}
}

func TestRegistryAddAll(t *testing.T) {
	t.Parallel()

	registry := NewRegistry("gogo", "dev")
	registry.AddAll(testModules()...)

	if got := len(registry.Manifests()); got != 2 {
		t.Errorf("len(Manifests) = %d, want 2", got)
	}
}

// Compile-time guards: the fixture exercises the optional interfaces.
var (
	_ Lifer          = (*lifecycleFixture)(nil)
	_ HealthReporter = (*lifecycleFixture)(nil)
)

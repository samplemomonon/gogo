// Package ops is the operational module: liveness/readiness endpoints the
// gateway exposes for deployment tooling. It deliberately carries no views
// (plain text responses only) to show how small a federated module can be —
// a module is a manifest plus routes, not a heavy vertical.
package ops

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"gogo/internal/shared/breaker"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/module"
)

// name is this module's manifest identity: the handle the registry keys it by
// and the one readiness excludes when aggregating every other module. Declared
// once so the manifest and the probe cannot disagree.
const name = "ops"

// Probe reports whether the gateway's readiness dependency is satisfied.
// The composition root wires it to the module registry's aggregated health
// (see cmd/server/main.go); a nil probe means "always ready".
type Probe func(ctx context.Context) error

// Register declares the ops module's graph with the composition root, keyed by
// its type, mirroring the pages' Register.
//
// Readiness is derived from two capabilities rather than from concrete page
// clients: the registry's aggregated health, and the group of contributed
// upstream breakers. Depending on the group is what keeps this module honest —
// it reports on every upstream the root wired, so adding one needs no edit
// here, and ops never imports the page adapters that own them.
func Register(b *fdi.Builder) {
	fdi.ProvideType(b, func(c *fdi.Container) (*Module, error) {
		registry, err := fdi.ResolveType[*module.Registry](c)
		if err != nil {
			return nil, fmt.Errorf("ops: resolve module.Registry: %w", err)
		}
		upstreams, err := fdi.ResolveGroupAs[*breaker.Breaker](c)
		if err != nil {
			return nil, fmt.Errorf("ops: resolve upstream breakers: %w", err)
		}
		return NewModuleWithProbe(Readiness(registry, upstreams)), nil
	})
}

// Readiness reports whether every module and every contributed upstream the
// gateway depends on can serve traffic: the registry's health across all
// modules except ops itself (whose probe would otherwise recurse), then each
// upstream breaker. A breaker carries the upstream's name, so the failure says
// which dependency is unreachable.
func Readiness(registry *module.Registry, upstreams []fdi.Member[*breaker.Breaker]) Probe {
	return func(ctx context.Context) error {
		if err := registry.ReadyExcluding(ctx, name); err != nil {
			return err
		}
		for _, upstream := range upstreams {
			if upstream.Value.State() == breaker.StateOpen {
				return fmt.Errorf("%s circuit breaker is open", upstream.Name)
			}
		}
		return nil
	}
}

// Module implements module.Interface for the ops stack.
type Module struct {
	probe Probe
}

// NewModule returns the ops module with a nil probe: liveness and readiness
// both pass whenever the process is up.
func NewModule() *Module {
	return &Module{}
}

// NewModuleWithProbe returns the ops module whose readiness depends on
// probe. Use it to aggregate the gateway's actual module health: liveness
// stays "process is up", readiness becomes "every module is ready".
func NewModuleWithProbe(probe Probe) *Module {
	return &Module{probe: probe}
}

// Manifest declares the ops module's remote contract.
func (m *Module) Manifest() module.Descriptor {
	return module.Descriptor{
		Name:        name,
		Version:     "1.0.0",
		Entry:       "/healthz",
		Description: "Operational health endpoints (liveness / readiness).",
		Routes: map[string]string{
			"liveness":  "/healthz",
			"readiness": "/readyz",
			"health":    "/healthz",
		},
	}
}

// RegisterRoutes mounts the ops endpoints directly on the gateway router.
func (m *Module) RegisterRoutes(r chi.Router) {
	r.Get("/healthz", m.liveness)
	r.Get("/readyz", m.readiness)
}

// liveness reports that the process is up and serving.
func (m *Module) liveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// readiness reports that the gateway can serve traffic. Without a probe it
// mirrors liveness; with a probe (the registry's aggregated module health)
// it reflects the real readiness of every registered module.
func (m *Module) readiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	if m.probe != nil {
		if err := m.probe(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready\n"))
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

// Healthy reports the ops module's health: ready when the probe (if any)
// passes, down when it fails. Modules that only serve the gateway report
// their own liveness; the probe aggregates the OTHER modules.
func (m *Module) Healthy(ctx context.Context) module.Health {
	if m.probe != nil {
		if err := m.probe(ctx); err != nil {
			return module.Health{Module: name, Status: module.StatusDown, Message: err.Error()}
		}
	}
	return module.Health{Module: name, Status: module.StatusOK}
}

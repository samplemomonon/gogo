// Module lifecycle and health. The module contract stays minimal
// (Manifest + RegisterRoutes); this file adds OPTIONAL extensions a module
// may implement to join the gateway's lifecycle:
//
//   - Lifer:        a module that allocates resources or spawns background
//     workers (Start) and releases them (Stop). The gateway
//     starts every module before serving and stops them on
//     shutdown, so an independently deployable module controls
//     its own start/stop.
//   - HealthReporter: a module that contributes to the gateway readiness /
//     liveness decision. The ops module aggregates every
//     reporter into its /readyz endpoint.
//
// A module that implements neither is still a first-class citizen: the
// gateway treats "no lifecycle" as always-ready and start/stop as no-ops,
// so scaffolding a module never has to grow into the lifecycle.
package module

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Status is a module health report status.
type Status string

const (
	// StatusOK means the module can serve traffic.
	StatusOK Status = "ok"

	// StatusDegraded means the module is up but impaired.
	StatusDegraded Status = "degraded"

	// StatusDown means the module cannot serve traffic.
	StatusDown Status = "down"
)

// Health is one module's contribution to the gateway readiness decision.
type Health struct {
	// Module is the reporting module's manifest name.
	Module string `json:"module"`

	// Status is the module's health.
	Status Status `json:"status"`

	// Message carries human-readable detail when the module is not OK.
	Message string `json:"message,omitempty"`
}

// Ready reports whether the module is healthy enough to serve traffic.
func (h Health) Ready() bool {
	return h.Status == StatusOK || h.Status == StatusDegraded
}

// Lifer is an optional module extension for lifecycle management. Modules
// that allocate resources (worker goroutines, connections, projections)
// implement Start/Stop; modules that need nothing leave the interface
// unimplemented.
type Lifer interface {
	// Start brings the module's dependencies up. Called by the gateway
	// before it starts serving, in registration order.
	Start(ctx context.Context) error

	// Stop releases the module's resources. Called by the gateway during
	// graceful shutdown, in reverse registration order.
	Stop(ctx context.Context) error
}

// HealthReporter is an optional module extension that lets a module
// contribute to the gateway's readiness/liveness decision.
type HealthReporter interface {
	// Healthy returns the module's current health. It must be fast and
	// must not block on long-lived dependencies.
	Healthy(ctx context.Context) Health
}

// Healthy returns a ready (ok) report for a module, whatever its concrete
// type — the default for modules without a HealthReporter.
func healthy(name string) Health {
	return Health{Module: name, Status: StatusOK}
}

// Start brings every registered module that implements Lifer up, in
// registration order. Errors are aggregated; a failing module does not stop
// the remaining modules from starting.
//
// Start is atomic with respect to failure: when any module fails to start,
// every module that started successfully in this call is stopped again (in
// reverse order) before the aggregated error is returned. A caller therefore
// never inherits a partially-started registry — either all lifers are up, or
// none is left running.
func (r *Registry) Start(ctx context.Context) error {
	started := make([]Lifer, 0)

	var errs []error
	for _, mod := range r.snapshot() {
		lifer, ok := mod.(Lifer)
		if !ok {
			continue
		}
		if err := lifer.Start(ctx); err != nil {
			errs = append(errs, fmt.Errorf("module %q start: %w", mod.Manifest().Name, err))
			continue
		}
		started = append(started, lifer)
	}

	if len(errs) == 0 {
		return nil
	}

	// Roll back the successful starts in reverse order so a failed boot
	// leaves no module holding resources it will never release.
	for i := len(started) - 1; i >= 0; i-- {
		if err := started[i].Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("module start rollback stop: %w", err))
		}
	}
	return errors.Join(errs...)
}

// Stop brings every registered module that implements Lifer down, in
// reverse registration order, so dependencies stop after their dependents.
func (r *Registry) Stop(ctx context.Context) error {
	modules := r.snapshot()

	var errs []error
	for i := len(modules) - 1; i >= 0; i-- {
		if lifer, ok := modules[i].(Lifer); ok {
			if err := lifer.Stop(ctx); err != nil {
				errs = append(errs, fmt.Errorf("module %q stop: %w", modules[i].Manifest().Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// Health collects a report from every registered module that implements
// HealthReporter. Modules without a reporter are reported as ready, so a
// minimal module never makes the gateway unhealthy.
func (r *Registry) Health(ctx context.Context) []Health {
	return r.healthExcluding(ctx, "")
}

// healthExcluding collects health reports, but never invokes the reporter
// of the named module. A reader (such as the ops probe) gathers every other
// module's report without recursing through its own probe.
func (r *Registry) healthExcluding(ctx context.Context, self string) []Health {
	reports := make([]Health, 0, len(r.order))
	for _, mod := range r.snapshot() {
		if mod.Manifest().Name == self {
			continue
		}
		name := mod.Manifest().Name
		if reporter, ok := mod.(HealthReporter); ok {
			report := reporter.Healthy(ctx)
			if report.Module == "" {
				report.Module = name
			}
			reports = append(reports, report)
			continue
		}
		reports = append(reports, healthy(name))
	}
	return reports
}

// Ready reports whether the gateway is ready to serve: it is ready when
// every registered module reports ready. Modules without a HealthReporter
// are always ready, so adding a minimal module never blocks readiness.
func (r *Registry) Ready(ctx context.Context) error {
	return r.readyExcluding(ctx, "")
}

// ReadyExcluding reports readiness of every module except the named one.
// It exists so a module's OWN health probe can aggregate the rest of the
// gateway without recursing into itself (the ops module does this).
func (r *Registry) ReadyExcluding(ctx context.Context, self string) error {
	return r.readyExcluding(ctx, self)
}

func (r *Registry) readyExcluding(ctx context.Context, self string) error {
	var failing []string
	// healthExcluding never invokes the self module's reporter, so it cannot
	// recurse through the caller's own probe.
	for _, report := range r.healthExcluding(ctx, self) {
		if report.Ready() {
			continue
		}
		if report.Message != "" {
			failing = append(failing, fmt.Sprintf("%s (%s)", report.Module, report.Message))
		} else {
			failing = append(failing, string(report.Module))
		}
	}
	if len(failing) == 0 {
		return nil
	}
	return fmt.Errorf("modules not ready: %s", strings.Join(failing, ", "))
}

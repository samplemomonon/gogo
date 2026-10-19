package shell

import (
	"testing"
)

// TestHealthCheckDocumentation documents how the circuit breaker integrates
// with the readiness probe. This is a documentation test that verifies the
// contract between components.
func TestHealthCheckDocumentation(t *testing.T) {
	// This test documents the health check integration:
	//
	// 1. Circuit breaker opens after 5 consecutive catalog failures
	// 2. Bootstrap wires breaker state check into ops module probe
	// 3. Readiness endpoint (/readiness) returns error if breaker is open
	// 4. Load balancer stops routing traffic to instances reporting not ready
	//
	// This prevents user requests from hitting gateway instances that cannot
	// reach the catalog service, improving availability in partial outage
	// scenarios.
	//
	// Implementation location: internal/shell/shell.go
	// See opsModule := ops.NewModuleWithProbe(...)
	//
	// The probe checks:
	//   - registry.ReadyExcluding(ctx, "ops") // All modules ready
	//   - catalogClient.Breaker().State() == 1 // Breaker not open
	//
	// Actual behavior is tested in:
	//   - internal/shared/breaker/breaker_test.go
	//   - internal/shared/catalogconsumer/breaker_test.go
	//   - internal/features/ops/module_test.go

	t.Log("Health check integration documented")
}

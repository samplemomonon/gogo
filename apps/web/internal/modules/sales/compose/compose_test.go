// Package compose_test covers the sales composition root's two startup paths:
// no database means the module runs in memory and Close is safe; a database
// configuration that cannot be reached surfaces as a startup error with no
// module and no close function to leak.
package compose_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gogo/internal/modules/sales/compose"
	"gogo/internal/modules/sales/domain"
	"gogo/internal/shared/config"
	"gogo/internal/shared/module"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// noopStock satisfies the StockAdjuster port the composition root supplies.
type noopStock struct{}

func (noopStock) Adjust(context.Context, domain.Tenant, string, int, string, string) error {
	return nil
}

// TestBuildWithoutADatabaseRunsInMemory pins the contract the local and test
// deployments rely on: an empty DatabaseURL yields a working sales module over
// the in-memory store, a no-op close that is safe to call more than once, and
// no error.
func TestBuildWithoutADatabaseRunsInMemory(t *testing.T) {
	t.Parallel()

	salesModule, closeModule, err := compose.Build(t.Context(), &config.AppConfig{}, noopStock{}, discardLogger())
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if salesModule == nil {
		t.Fatal("Build returned a nil module for the in-memory path")
	}
	if closeModule == nil {
		t.Fatal("Build returned a nil close function for the in-memory path")
	}

	closeModule()
	closeModule()

	if got := salesModule.Manifest().Name; got != "sales" {
		t.Errorf("Manifest().Name = %q, want sales", got)
	}
	// nil ping is the in-memory store's "always ready".
	if health := salesModule.Healthy(t.Context()); health.Status != module.StatusOK {
		t.Errorf("Health = %q, want %q", health.Status, module.StatusOK)
	}
}

// TestBuildRejectsAMalformedDatabaseURL pins startup failure behaviour: an
// unparseable DSN must fail Build before any connection attempt, and must not
// return a module or a close function.
func TestBuildRejectsAMalformedDatabaseURL(t *testing.T) {
	t.Parallel()

	cfg := &config.AppConfig{DatabaseURL: "not a dsn", DBPingTimeout: 2 * time.Second}

	module, closeModule, err := compose.Build(t.Context(), cfg, noopStock{}, discardLogger())

	if err == nil {
		t.Fatal("Build accepted a malformed DatabaseURL")
	}
	if module != nil {
		t.Error("Build returned a module alongside an error")
	}
	if closeModule != nil {
		t.Error("Build returned a close function alongside an error")
	}
	if !strings.Contains(err.Error(), "parse DSN") {
		t.Errorf("error %q should name the parse step", err)
	}
}

// TestBuildRejectsAnUnreachableDatabase pins that a well-formed but down
// database fails startup with the ping error rather than handing the gateway a
// module whose first checkout would fail.
func TestBuildRejectsAnUnreachableDatabase(t *testing.T) {
	t.Parallel()

	cfg := &config.AppConfig{
		DatabaseURL:   "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1",
		DBPingTimeout: 2 * time.Second,
	}

	module, closeModule, err := compose.Build(t.Context(), cfg, noopStock{}, discardLogger())

	if err == nil {
		closeModule()
		t.Fatal("Build succeeded against an unreachable database")
	}
	if module != nil {
		t.Error("Build returned a module alongside an error")
	}
	if closeModule != nil {
		t.Error("Build returned a close function alongside an error")
	}
	if !strings.Contains(err.Error(), "postgres: ping") {
		t.Errorf("error %q should name the ping step", err)
	}
}

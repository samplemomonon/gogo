// Package compose_test covers the auth module's composition root: the
// in-memory fallback without a database, and the startup failure contract
// when a configured database is unreachable.
package compose_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"gogo/internal/modules/auth/compose"
	"gogo/internal/shared/config"
	"gogo/internal/shared/module"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestBuildWithoutDatabaseRunsInMemory pins the documented fallback
// (compose.go:23-26): no DatabaseURL means the module comes back over the
// in-memory store, with a close func the caller can always call and a nil
// ping — the module therefore reports ok without a database to probe.
func TestBuildWithoutDatabaseRunsInMemory(t *testing.T) {
	t.Parallel()

	mod, closeModule, err := compose.Build(context.Background(), &config.AppConfig{}, testLogger())
	if err != nil {
		t.Fatalf("Build without a database = %v, want nil", err)
	}
	if mod == nil {
		t.Fatal("Build without a database returned no module")
	}
	if closeModule == nil {
		t.Fatal("Build without a database returned no close func; the caller always calls it")
	}
	closeModule()

	if report := mod.Healthy(context.Background()); report.Status != module.StatusOK {
		t.Errorf("Healthy = %+v, want ok over the in-memory store", report)
	}
}

// TestBuildFailsOnAnUnreachableDatabase pins the startup contract: Build
// proves the database before wiring it (pgxkit.Connect pings eagerly), so an
// unreachable server surfaces as an error with no module and no close func —
// never a module that fails on its first request. The caller must have
// nothing to clean up when Build fails.
func TestBuildFailsOnAnUnreachableDatabase(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := &config.AppConfig{
		DatabaseURL:   "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1",
		DBPingTimeout: time.Second,
	}

	mod, closeModule, err := compose.Build(ctx, cfg, testLogger())
	if err == nil {
		t.Fatal("Build succeeded against an unreachable database")
	}
	if mod != nil {
		t.Error("Build returned a module alongside the error")
	}
	if closeModule != nil {
		t.Error("Build returned a close func alongside the error")
	}
}

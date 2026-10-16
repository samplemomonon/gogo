package compose_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"gogo/internal/modules/inventory/compose"
	"gogo/internal/shared/config"
	sharedmodule "gogo/internal/shared/module"
)

// unreachableDSN points at a closed port so a connection attempt fails
// immediately instead of hanging. connect_timeout bounds it even if the port
// answers. It is the same shape the shared pgxkit tests use.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1"

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestBuildWithoutADatabaseRunsInMemory pins the development default: an empty
// DatabaseURL must yield a working module on the in-memory store, a close
// function the composition root can call unconditionally (never nil), and no
// error. The close function on this path releases nothing, so calling it must
// be safe.
func TestBuildWithoutADatabaseRunsInMemory(t *testing.T) {
	t.Parallel()

	module, closeModule, err := compose.Build(context.Background(), &config.AppConfig{}, testLogger())
	if err != nil {
		t.Fatalf("Build without a database = %v, want nil", err)
	}
	if module == nil {
		t.Fatal("Build without a database returned no module")
	}
	if closeModule == nil {
		t.Fatal("Build without a database returned no close function; the caller would have to nil-check it during shutdown")
	}
	closeModule()

	if got := module.Manifest().Name; got != "inventory" {
		t.Errorf("Manifest().Name = %q, want inventory", got)
	}
	if health := module.Healthy(context.Background()); health.Status != sharedmodule.StatusOK {
		t.Errorf("in-memory inventory health = %+v, want ok", health)
	}
}

// TestBuildRejectsAMalformedDatabaseURL pins the composition root's failure
// shape: a configuration error must reach the caller as an error with no module
// and no close function, so a caller cannot accidentally serve on a nil module
// or call a nil close during the crash path.
func TestBuildRejectsAMalformedDatabaseURL(t *testing.T) {
	t.Parallel()

	cfg := &config.AppConfig{DatabaseURL: "not a dsn"}

	module, closeModule, err := compose.Build(context.Background(), cfg, testLogger())

	if err == nil {
		t.Fatal("Build accepted a malformed DatabaseURL")
	}
	if module != nil {
		t.Error("Build returned a module alongside an error")
	}
	if closeModule != nil {
		t.Error("Build returned a close function alongside an error; releasing nothing must not be the caller's job")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("error %q does not name the postgres step that failed", err)
	}
}

// TestBuildRejectsAnUnreachableDatabase exercises Connect's eager ping through
// the composition root: a DSN that parses but names a dead server must abort
// startup with an error, not hand back a module that fails on its first
// request. The DSN's closed port makes the ping fail in milliseconds.
func TestBuildRejectsAnUnreachableDatabase(t *testing.T) {
	t.Parallel()

	cfg := &config.AppConfig{DatabaseURL: unreachableDSN}

	module, closeModule, err := compose.Build(context.Background(), cfg, testLogger())

	if err == nil {
		t.Fatal("Build succeeded against an unreachable database")
	}
	if module != nil {
		t.Error("Build returned a module alongside an error")
	}
	if closeModule != nil {
		t.Error("Build returned a close function alongside an error")
	}
	if !strings.Contains(err.Error(), "postgres: ping") {
		t.Errorf("error %q should name the ping step that failed", err)
	}
}

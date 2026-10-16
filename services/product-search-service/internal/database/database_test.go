// Package database_test pins the boot contract: Connect must fail loudly and
// quickly against a dead server rather than hand back a pool that fails later,
// Ping must wrap the dial failure for the readiness probe, and the embedded
// migration set must stay loadable by Migrate's lexical, .sql-only reader.
//
// The tests are hermetic: they point the driver at a closed port and never
// require a live PostgreSQL.
package database_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"product-search-service/internal/database"
)

// unreachableDSN points at a closed port so an attempted use fails with a
// dial error instead of hanging; connect_timeout bounds it even if the port
// starts answering.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1&sslmode=disable"

func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestConnectFailsWhenTheServerIsUnreachable pins that the ping failure is
// wrapped and named. A pool that is opened but unusable must not be returned:
// every later query would fail somewhere far away from the real cause.
func TestConnectFailsWhenTheServerIsUnreachable(t *testing.T) {
	t.Parallel()

	db, err := database.Connect(testContext(t), unreachableDSN, database.Options{
		MaxOpenConns:    2,
		MaxIdleConns:    1,
		ConnMaxLifetime: time.Minute,
	})
	if err == nil {
		_ = db.Close()
		t.Fatal("Connect succeeded against an unreachable server")
	}
	if db != nil {
		_ = db.Close()
		t.Fatal("Connect returned a pool alongside an error")
	}
	if !strings.Contains(err.Error(), "ping database") {
		t.Errorf("error %q should name the ping step", err)
	}
}

// TestPingWrapsTheDialFailure pins the readiness-probe contract: Ping reports
// an error for an unreachable database, and the message names the step so the
// probe log is self-explanatory.
func TestPingWrapsTheDialFailure(t *testing.T) {
	t.Parallel()

	// The driver is registered by the package under test's blank import.
	db, err := sql.Open("postgres", unreachableDSN)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := database.Ping(testContext(t), db); err == nil {
		t.Fatal("Ping succeeded against an unreachable server")
	} else if !strings.Contains(err.Error(), "ping database") {
		t.Errorf("error %q should name the ping step", err)
	}
}

// TestMigrateSurfacesTheBookkeepingFailure pins that a migration run against a
// dead server fails at the first honest step — creating schema_migrations —
// instead of reporting success or panic.
func TestMigrateSurfacesTheBookkeepingFailure(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("postgres", unreachableDSN)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	err = database.Migrate(testContext(t), db, quietLogger())
	if err == nil {
		t.Fatal("Migrate reported success against an unreachable server")
	}
	if !strings.Contains(err.Error(), "create schema_migrations") {
		t.Errorf("error %q should name the bookkeeping step", err)
	}
}

// TestMigrationsUseTheSQLSuffix pins the input contract of Migrate's reader:
// it rejects any embedded file without a .sql suffix, so a stray README in
// migrations/ would abort every boot. The check runs against the on-disk
// directory, which is the same set the go:embed directive captures.
func TestMigrationsUseTheSQLSuffix(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("migrations directory is empty; the embed would not compile")
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(filepath.Base(entry.Name()), ".sql") {
			t.Errorf("migration %q lacks the .sql suffix Migrate requires", entry.Name())
		}
	}
}

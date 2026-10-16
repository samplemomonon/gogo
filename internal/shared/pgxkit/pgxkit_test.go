package pgxkit

import (
	"context"
	"embed"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed testdata/*.sql
var migrations embed.FS

// unreachableDSN points at a closed port so pool use fails immediately with a
// dial error instead of hanging. connect_timeout bounds it even if the port
// answers.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1"

func TestConnectRejectsAMalformedDSN(t *testing.T) {
	t.Parallel()

	pool, err := Connect(context.Background(), "not a dsn", 0, 0)

	if err == nil {
		pool.Close()
		t.Fatal("Connect accepted a malformed DSN")
	}
	if pool != nil {
		t.Error("Connect returned a pool alongside an error")
	}
	if !strings.Contains(err.Error(), "parse DSN") {
		t.Errorf("error %q should name the parse step", err)
	}
}

// TestConnectRejectsAnUnreachableServer pins the reason Connect pings at all:
// a bad DSN or a down server must surface as a clear startup error, not as the
// first request's 500.
func TestConnectRejectsAnUnreachableServer(t *testing.T) {
	t.Parallel()

	pool, err := Connect(context.Background(), unreachableDSN, 0, 0)

	if err == nil {
		pool.Close()
		t.Fatal("Connect succeeded against an unreachable server")
	}
	if pool != nil {
		t.Error("Connect returned a pool alongside an error")
	}
	if !strings.Contains(err.Error(), "postgres: ping") {
		t.Errorf("error %q should name the ping step", err)
	}
}

// TestMigrateFailsOnAnUnreachableServer exercises the first statement Migrate
// issues. A live-Postgres happy path belongs in an integration test; this
// guards that a dead database aborts startup with a named error rather than a
// panic or a silent success.
func TestMigrateFailsOnAnUnreachableServer(t *testing.T) {
	t.Parallel()

	cfg, err := pgxpool.ParseConfig(unreachableDSN)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	defer pool.Close()

	err = Migrate(context.Background(), pool, migrations, "testdata")

	if err == nil {
		t.Fatal("Migrate succeeded without a database")
	}
	if !strings.Contains(err.Error(), "schema_migrations") {
		t.Errorf("error %q should name the first step that failed", err)
	}
}

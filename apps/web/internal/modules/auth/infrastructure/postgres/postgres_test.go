// Package postgres_test covers the PostgreSQL adapter's behaviour that does
// not need a live database: DSN validation, the startup ping, and the
// constructors. Everything here is hermetic — the DSN points at a closed port
// and connect_timeout bounds every dial attempt.
package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	authpostgres "gogo/internal/modules/auth/infrastructure/postgres"
)

// unreachableDSN points at a closed port so an attempted use fails with a
// dial error instead of hanging; connect_timeout bounds it even if the port
// answers.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1"

// unreachablePool builds a live pool object over an unreachable server. pgx
// connects lazily, so construction succeeds and every query fails with a dial
// error — the shape the repository error tests need.
func unreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(unreachableDSN)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewWithConfig: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestConnectRejectsAMalformedDSN pins the first validation step: a DSN pgx
// cannot parse is named as a parse failure, and no pool escapes alongside the
// error, so a caller can always treat err != nil as "nothing to close".
func TestConnectRejectsAMalformedDSN(t *testing.T) {
	t.Parallel()

	pool, err := authpostgres.Connect(context.Background(), "not a dsn", 0, 0)
	if err == nil {
		pool.Close()
		t.Fatal("Connect accepted a malformed DSN")
	}
	if pool != nil {
		t.Error("Connect returned a pool alongside the error")
	}
	if !strings.Contains(err.Error(), "parse DSN") {
		t.Errorf("error %q should name the parse step", err)
	}
}

// TestConnectRejectsAnUnreachableServer pins the eager ping: a valid DSN
// whose server is down fails at Connect, not at the first request, and no
// pool escapes. This is the reason compose.Build can promise "error means no
// module".
func TestConnectRejectsAnUnreachableServer(t *testing.T) {
	t.Parallel()

	pool, err := authpostgres.Connect(context.Background(), unreachableDSN, 0, 0)
	if err == nil {
		pool.Close()
		t.Fatal("Connect succeeded against an unreachable server")
	}
	if pool != nil {
		t.Error("Connect returned a pool alongside the error")
	}
	if !strings.Contains(err.Error(), "postgres: ping") {
		t.Errorf("error %q should name the ping step", err)
	}
}

// TestConstructorsReturnNonNilStores pins that both constructors are plain
// wiring: they never validate or dial, so a caller can build stores over a
// pool it owns without the constructors pre-empting the error path.
func TestConstructorsReturnNonNilStores(t *testing.T) {
	t.Parallel()

	pool := unreachablePool(t)

	if store := authpostgres.NewEmployeeStore(pool); store == nil {
		t.Error("NewEmployeeStore returned nil")
	}
	if store := authpostgres.NewSessionStore(pool); store == nil {
		t.Error("NewSessionStore returned nil")
	}
}

// TestMigrateFailsOnAnUnreachableServer exercises Migrate's first statement
// (creating schema_migrations). A dead database must abort startup with a
// named error rather than a panic or a silent success.
func TestMigrateFailsOnAnUnreachableServer(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := authpostgres.Migrate(ctx, unreachablePool(t))
	if err == nil {
		t.Fatal("Migrate succeeded without a database")
	}
	if !strings.Contains(err.Error(), "schema_migrations") {
		t.Errorf("error %q should name the first step that failed", err)
	}
}

// Package postgres_test covers the sales Postgres seam without a database:
// an unparseable DSN must fail Connect with a named parse error, and every
// repository call against a valid-but-unreachable pool must return a wrapped
// error rather than panic or lie with a domain sentinel.
package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/features/sales/domain"
	"gogo/internal/features/sales/infrastructure/postgres"
)

// unreachableDSN points at a closed port so pool use fails immediately with a
// dial error instead of hanging. connect_timeout bounds it even if the port
// answers.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1"

// unreachablePool builds a lazy pool the way pgxkit_test does: construction
// does not dial, so the first repository call is what surfaces the outage.
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

// aTransaction is a minimal valid sale; the repository calls under test fail
// before any column is read, so only identity matters.
func aTransaction() *domain.Transaction {
	return &domain.Transaction{TenantID: "acme", ID: "tx-1", Number: "TRX-1"}
}

// TestConnectRejectsAMalformedDSN pins the startup error for a bad DSN: a nil
// pool and an error naming the parse step, so the composition root crashes
// with a readable message instead of the first request's 500.
func TestConnectRejectsAMalformedDSN(t *testing.T) {
	t.Parallel()

	pool, err := postgres.Connect(t.Context(), "not a dsn", 0, 0)

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

// TestRepositoryCallsFailCleanlyOnAnUnreachablePool pins that every store call
// degrades to a wrapped error naming the failing step. In particular Get must
// not report ErrTransactionNotFound — an outage and a missing row must never
// look the same.
func TestRepositoryCallsFailCleanlyOnAnUnreachablePool(t *testing.T) {
	t.Parallel()

	store := postgres.NewStore(unreachablePool(t))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	t.Run("Ping", func(t *testing.T) {
		if err := store.Ping(ctx); err == nil {
			t.Error("Ping succeeded without a database")
		}
	})

	t.Run("Create", func(t *testing.T) {
		err := store.Create(ctx, "acme", aTransaction())
		if err == nil || !strings.Contains(err.Error(), "postgres: begin sale") {
			t.Errorf("Create = %v, want an error naming the begin step", err)
		}
	})

	t.Run("Get", func(t *testing.T) {
		_, err := store.Get(ctx, "acme", "tx-1")
		if err == nil || !strings.Contains(err.Error(), "postgres: get sale") {
			t.Errorf("Get = %v, want an error naming the get step", err)
		}
		if errors.Is(err, domain.ErrTransactionNotFound) {
			t.Error("Get reported not-found for an unreachable database")
		}
	})

	t.Run("List", func(t *testing.T) {
		_, err := store.List(ctx, "acme", 10)
		if err == nil || !strings.Contains(err.Error(), "postgres: list sales") {
			t.Errorf("List = %v, want an error naming the list step", err)
		}
	})

	t.Run("SaveVoid", func(t *testing.T) {
		err := store.SaveVoid(ctx, "acme", aTransaction())
		if err == nil || !strings.Contains(err.Error(), "postgres: void sale") {
			t.Errorf("SaveVoid = %v, want an error naming the void step", err)
		}
	})
}

// TestMigrateFailsOnAnUnreachablePool pins that startup migration aborts with a
// named error rather than a panic or a silent success.
func TestMigrateFailsOnAnUnreachablePool(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := postgres.Migrate(ctx, unreachablePool(t))

	if err == nil {
		t.Fatal("Migrate succeeded without a database")
	}
	if !strings.Contains(err.Error(), "schema_migrations") {
		t.Errorf("error %q should name the first step that failed", err)
	}
}

// TestNewStoreAndCloseAreSafe pins the constructor's non-nil result and that
// Close releases the pool without panicking (a second Close comes from the
// test cleanup).
func TestNewStoreAndCloseAreSafe(t *testing.T) {
	t.Parallel()

	store := postgres.NewStore(unreachablePool(t))
	if store == nil {
		t.Fatal("NewStore returned nil")
	}
	store.Close()
}

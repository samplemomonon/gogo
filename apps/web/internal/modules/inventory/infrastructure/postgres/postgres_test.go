package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/modules/inventory/domain"
	inventorypostgres "gogo/internal/modules/inventory/infrastructure/postgres"
)

// unreachableDSN points at a closed port so a connection attempt fails
// immediately with a dial error instead of hanging. connect_timeout bounds it
// even if something answers on the port. These tests never need a live
// database: they pin how the adapter fails, which is what a startup error and
// a request-path error both depend on.
const unreachableDSN = "postgres://user:pass@127.0.0.1:1/db?connect_timeout=1"

// dbTimeout bounds every call these tests make, so a bug that ignores the
// caller's context would fail the test instead of hanging it.
const dbTimeout = 2 * time.Second

func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	t.Cleanup(cancel)
	return ctx
}

// unreachablePool builds a pool without pinging it, so a test can reach the
// repository methods and observe their error wrapping. Connect would refuse to
// return a pool at all; NewWithConfig does not connect until first use.
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

// TestConnectRejectsAMalformedDSN pins the module-level wrapper's error path:
// an unparseable DSN must fail before any socket is opened, with no pool to
// leak.
func TestConnectRejectsAMalformedDSN(t *testing.T) {
	t.Parallel()

	pool, err := inventorypostgres.Connect(context.Background(), "not a dsn", 0, 0)

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

// TestConnectRejectsAnUnreachableServer pins the reason Connect pings: a
// reachable-looking but dead server is a startup error, not a first request's
// 500, and the error names the ping step so an operator knows where to look.
func TestConnectRejectsAnUnreachableServer(t *testing.T) {
	t.Parallel()

	pool, err := inventorypostgres.Connect(context.Background(), unreachableDSN, 0, 0)

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

// TestReadsFailCleanlyOnAnUnreachablePool walks every read method with a pool
// that cannot dial. Each must return an error naming its own step and a nil
// result — no panic, and no partial value a caller could mistake for data.
// The store is deliberately constructed from an unreachable pool: that is the
// only condition under which a no-database test can exercise these paths.
func TestReadsFailCleanlyOnAnUnreachablePool(t *testing.T) {
	t.Parallel()

	repo := inventorypostgres.NewStockRepository(unreachablePool(t))
	if repo == nil {
		t.Fatal("NewStockRepository returned nil")
	}

	tests := []struct {
		name     string
		wantStep string
		call     func(context.Context) error
	}{
		{
			name:     "get stock",
			wantStep: "postgres: get stock",
			call: func(ctx context.Context) error {
				stock, err := repo.GetStock(ctx, "acme", "p-1")
				if stock != nil {
					t.Error("GetStock returned a stock alongside an error")
				}
				return err
			},
		},
		{
			name:     "list stocks",
			wantStep: "postgres: list stocks",
			call: func(ctx context.Context) error {
				stocks, err := repo.ListStocks(ctx, "acme", nil)
				if stocks != nil {
					t.Error("ListStocks returned stocks alongside an error")
				}
				return err
			},
		},
		{
			name:     "list movements",
			wantStep: "postgres: list movements",
			call: func(ctx context.Context) error {
				movements, err := repo.ListMovements(ctx, "acme", "", 0)
				if movements != nil {
					t.Error("ListMovements returned movements alongside an error")
				}
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.call(testContext(t))
			if err == nil {
				t.Fatal("call against an unreachable pool succeeded")
			}
			if !strings.Contains(err.Error(), tc.wantStep) {
				t.Errorf("error %q should name the %q step", err, tc.wantStep)
			}
		})
	}
}

// TestAdjustStockValidatesBeforeTouchingTheDatabase pins the ordering of the
// two guards inside AdjustStock: domain validation runs before the pool is
// touched, so an invalid adjustment fails fast with the domain sentinel — and
// not with the dial error an unreachable pool would produce if validation
// happened after Begin. These calls make no connection at all.
func TestAdjustStockValidatesBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()

	repo := inventorypostgres.NewStockRepository(unreachablePool(t))
	ctx := testContext(t)

	stock, movement, err := repo.AdjustStock(ctx, "", "p-1", 1, "receive", "", "tester", "", "")
	if !errors.Is(err, domain.ErrInvalidTenant) {
		t.Errorf("missing tenant = %v, want ErrInvalidTenant", err)
	}
	if !errors.Is(err, domain.ErrInvalidAdjustment) {
		t.Errorf("missing tenant = %v, want an ErrInvalidAdjustment", err)
	}
	if stock != nil || movement != nil {
		t.Error("a rejected adjustment returned a stock or movement")
	}

	stock, movement, err = repo.AdjustStock(ctx, "acme", "  ", 1, "receive", "", "tester", "", "")
	if !errors.Is(err, domain.ErrInvalidProductID) {
		t.Errorf("missing product = %v, want ErrInvalidProductID", err)
	}
	if stock != nil || movement != nil {
		t.Error("a rejected adjustment returned a stock or movement")
	}
}

// TestAdjustStockFailsCleanlyOnAnUnreachablePool pins the first step a valid
// adjustment reaches: the store opens a transaction before any statement, so
// a dead pool surfaces as a wrapped begin error rather than a panic or a
// domain error the caller would misread.
func TestAdjustStockFailsCleanlyOnAnUnreachablePool(t *testing.T) {
	t.Parallel()

	repo := inventorypostgres.NewStockRepository(unreachablePool(t))

	stock, movement, err := repo.AdjustStock(testContext(t), "acme", "p-1", 1, "receive", "note", "tester", "", "ref-1")

	if err == nil {
		t.Fatal("AdjustStock succeeded against an unreachable pool")
	}
	if !strings.Contains(err.Error(), "postgres: begin adjustment") {
		t.Errorf("error %q should name the begin step", err)
	}
	if stock != nil || movement != nil {
		t.Error("a failed adjustment returned a stock or movement")
	}
}

// TestMigrateFailsOnAnUnreachablePool pins the wrapper's first step: Migrate
// always ensures schema_migrations before reading the embedded migrations, so
// a dead database aborts startup with a named error instead of a panic. The
// live-Postgres happy path is an integration concern and is not simulated
// here.
func TestMigrateFailsOnAnUnreachablePool(t *testing.T) {
	t.Parallel()

	err := inventorypostgres.Migrate(testContext(t), unreachablePool(t))

	if err == nil {
		t.Fatal("Migrate succeeded without a database")
	}
	if !strings.Contains(err.Error(), "schema_migrations") {
		t.Errorf("error %q should name the first statement that failed", err)
	}
}

// TestPingFailsOnAnUnreachablePool pins the health path: Ping surfaces the
// pool's dial error, and after Close a ping must fail too rather than panic.
// The repository's Ping is the function readyz calls, so its failure must be a
// value, not a crash.
func TestPingFailsOnAnUnreachablePool(t *testing.T) {
	t.Parallel()

	pool := unreachablePool(t)
	repo := inventorypostgres.NewStockRepository(pool)

	if err := repo.Ping(testContext(t)); err == nil {
		t.Fatal("Ping succeeded against an unreachable pool")
	}

	repo.Close()

	if err := repo.Ping(testContext(t)); err == nil {
		t.Error("Ping after Close succeeded")
	}
}

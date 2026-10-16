// Package inventory_test covers the inventory module as the gateway sees it:
// the manifest it federates, the health report it contributes, and the gRPC
// surface its consumers reach over an in-process bufconn transport. Like the
// billing module test, it exercises contracts rather than internals — the
// store's own behaviour belongs to infrastructure and domain tests.
package inventory_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	inventoryv1 "gogo/contracts/inventory/v1"
	"gogo/internal/modules/inventory"
	"gogo/internal/modules/inventory/domain"
	"gogo/internal/modules/inventory/infrastructure"
	"gogo/internal/shared/grpcx"
	sharedmodule "gogo/internal/shared/module"
)

// The gateway registers every module through module.Interface and reads
// health through the optional HealthReporter extension. Declared at package
// scope so the compiler fails this package — not a runtime assertion — the
// moment the module stops satisfying either contract, mirroring the
// production assertion billing's module.go carries.
var (
	_ sharedmodule.Interface      = (*inventory.Module)(nil)
	_ sharedmodule.HealthReporter = (*inventory.Module)(nil)
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// serveModule mounts the module the way the composition root does — over the
// shared grpcx in-process bufconn transport, tenant interceptors included —
// and returns a client connection. No socket is opened.
func serveModule(t *testing.T, module *inventory.Module) *grpc.ClientConn {
	t.Helper()

	conn, closeServer, err := grpcx.InProcess(module.RegisterGRPC)
	if err != nil {
		t.Fatalf("grpcx.InProcess: %v", err)
	}
	t.Cleanup(func() {
		closeServer()
		_ = conn.Close()
	})
	return conn
}

// TestManifest pins the federated contract the shell serves: the module's
// identity, remote entry, and route names. Other modules and the seller console
// address inventory by these values.
func TestManifest(t *testing.T) {
	t.Parallel()

	manifest := inventory.NewModule(testLogger()).Manifest()

	if manifest.Name != "inventory" {
		t.Errorf("Name = %q, want inventory", manifest.Name)
	}
	if manifest.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", manifest.Version)
	}
	if manifest.Entry != "/seller/stok" {
		t.Errorf("Entry = %q, want /seller/stok", manifest.Entry)
	}
	if got := manifest.Routes["stock"]; got != "/seller/stok" {
		t.Errorf("Routes[stock] = %q, want /seller/stok", got)
	}
	if manifest.Description == "" {
		t.Error("manifest should describe the module for the federation endpoint")
	}
}

// TestHealthy pins the readiness contract. A module with no ping configured
// (the in-memory default) is always ok; one with a failing ping is down with a
// fixed, non-leaking message; one with a succeeding ping is ok. Ready() is the
// gateway's decision and must agree with the status.
func TestHealthy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ping        func(context.Context) error
		wantStatus  sharedmodule.Status
		wantMessage string
	}{
		{
			name:       "no ping configured is ok",
			ping:       nil,
			wantStatus: sharedmodule.StatusOK,
		},
		{
			name:       "a succeeding ping is ok",
			ping:       func(context.Context) error { return nil },
			wantStatus: sharedmodule.StatusOK,
		},
		{
			name:        "a failing ping is down",
			ping:        func(context.Context) error { return errors.New("driver: connection refused") },
			wantStatus:  sharedmodule.StatusDown,
			wantMessage: "inventory database unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			module := inventory.NewModuleWithDeps(infrastructure.NewStockRepository(), tc.ping, testLogger())

			health := module.Healthy(context.Background())
			if health.Module != "inventory" {
				t.Errorf("Health.Module = %q, want inventory", health.Module)
			}
			if health.Status != tc.wantStatus {
				t.Errorf("Health.Status = %q, want %q", health.Status, tc.wantStatus)
			}
			if health.Message != tc.wantMessage {
				t.Errorf("Health.Message = %q, want %q", health.Message, tc.wantMessage)
			}
			if wantReady := tc.wantStatus == sharedmodule.StatusOK; health.Ready() != wantReady {
				t.Errorf("Health.Ready() = %v, want %v", health.Ready(), wantReady)
			}
		})
	}
}

// TestRegisterRoutesMountsNothing pins the documented no-op: inventory has no
// browsable HTTP surface, so registering it must not claim its manifest entry
// on the gateway's router. The seller console reaches it over the gRPC
// contract instead.
func TestRegisterRoutesMountsNothing(t *testing.T) {
	t.Parallel()

	module := inventory.NewModule(testLogger())
	router := chi.NewRouter()

	module.RegisterRoutes(router)

	if router.Match(chi.NewRouteContext(), http.MethodGet, "/seller/stok") {
		t.Error("RegisterRoutes mounted an HTTP route; the module exposes stock over gRPC only")
	}
}

// TestListStocksOnAnEmptyStoreReturnsOK is the module's gRPC smoke: the
// registration is complete and a client can call the contract before any stock
// exists, receiving an explicit empty result.
func TestListStocksOnAnEmptyStoreReturnsOK(t *testing.T) {
	t.Parallel()

	client := inventoryv1.NewInventoryServiceClient(serveModule(t, inventory.NewModule(testLogger())))

	resp, err := client.ListStocks(context.Background(), &inventoryv1.ListStocksRequest{})
	if err != nil {
		t.Fatalf("ListStocks on an empty store: %v", err)
	}
	if len(resp.GetStocks()) != 0 {
		t.Errorf("ListStocks returned %d stocks, want 0", len(resp.GetStocks()))
	}
}

// TestHealthCheckServesServing pins that RegisterGRPC also mounts the standard
// health service: with no ping configured (the in-memory default) it reports
// SERVING, which is what the gateway's readiness probe reads over the same
// connection consumers use.
func TestHealthCheckServesServing(t *testing.T) {
	t.Parallel()

	conn := serveModule(t, inventory.NewModule(testLogger()))

	resp, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("health status = %v, want SERVING", resp.GetStatus())
	}
}

// TestNewModuleWiresTheInMemoryStore pins NewModule's one promise: the module
// it builds serves stock without a database. Adjusting and reading back over
// the contract proves the store is wired and shared between calls, not merely
// present.
func TestNewModuleWiresTheInMemoryStore(t *testing.T) {
	t.Parallel()

	client := inventoryv1.NewInventoryServiceClient(serveModule(t, inventory.NewModule(testLogger())))
	ctx := context.Background()

	adjust, err := client.AdjustStock(ctx, &inventoryv1.AdjustStockRequest{
		ProductId: "p-1", Delta: 5, Kind: "receive", Actor: "tester", Reference: "ref-1",
	})
	if err != nil {
		t.Fatalf("AdjustStock: %v", err)
	}
	if got := adjust.GetStock().GetQuantity(); got != 5 {
		t.Fatalf("quantity after adjustment = %d, want 5", got)
	}

	got, err := client.GetStock(ctx, &inventoryv1.GetStockRequest{ProductId: "p-1"})
	if err != nil {
		t.Fatalf("GetStock: %v", err)
	}
	if got.GetStock().GetQuantity() != 5 {
		t.Errorf("quantity read back = %d, want 5", got.GetStock().GetQuantity())
	}
}

// TestNewModuleWithDepsServesTheInjectedService pins the seam the composition
// root uses: the store passed to NewModuleWithDeps — not the in-memory default
// — answers the contract. The fixture returns a quantity and product the
// in-memory store could never produce, so serving it can only mean the
// injected store is the one wired.
func TestNewModuleWithDepsServesTheInjectedService(t *testing.T) {
	t.Parallel()

	fixture := &fixedStockService{
		stock: &domain.Stock{ProductID: "from-the-store", Quantity: 42},
	}
	module := inventory.NewModuleWithDeps(fixture, nil, testLogger())
	client := inventoryv1.NewInventoryServiceClient(serveModule(t, module))

	resp, err := client.GetStock(context.Background(), &inventoryv1.GetStockRequest{ProductId: "from-the-store"})
	if err != nil {
		t.Fatalf("GetStock through the injected store: %v", err)
	}
	if got := resp.GetStock().GetQuantity(); got != 42 {
		t.Errorf("quantity = %d, want 42; the injected store did not serve the call", got)
	}
}

// fixedStockService is a ports.StockService whose reads return one fixed stock.
// It exists only to prove NewModuleWithDeps wires what it is given; the port's
// own behaviour is tested elsewhere.
type fixedStockService struct {
	stock *domain.Stock
}

func (f *fixedStockService) GetStock(context.Context, domain.Tenant, string) (*domain.Stock, error) {
	return f.stock, nil
}

func (f *fixedStockService) ListStocks(context.Context, domain.Tenant, []string) ([]*domain.Stock, error) {
	return nil, nil
}

func (f *fixedStockService) AdjustStock(context.Context, domain.Tenant, string, int, string, string, string, string) (*domain.Stock, *domain.Movement, error) {
	return nil, nil, nil
}

func (f *fixedStockService) ListMovements(context.Context, domain.Tenant, string, int) ([]*domain.Movement, error) {
	return nil, nil
}

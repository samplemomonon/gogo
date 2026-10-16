// Package sales_test covers the sales module contract: its federated manifest,
// the health report the gateway aggregates, the deliberately empty HTTP surface,
// and the gRPC surface it mounts for the seller console.
package sales_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/health/grpc_health_v1"

	salesv1 "gogo/contracts/sales/v1"
	"gogo/internal/modules/sales"
	"gogo/internal/modules/sales/application/ports"
	"gogo/internal/modules/sales/domain"
	"gogo/internal/modules/sales/infrastructure"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/module"
)

// The gateway registers modules through module.Interface and aggregates health
// through module.HealthReporter; these pins fail the build if sales.Module drifts
// away from either extension point.
var (
	_ module.Interface      = (*sales.Module)(nil)
	_ module.HealthReporter = (*sales.Module)(nil)
	_ ports.StockAdjuster   = noopStock{}
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// noopStock satisfies the StockAdjuster port for tests that never reserve stock.
type noopStock struct{}

func (noopStock) Adjust(context.Context, domain.Tenant, string, int, string, string) error {
	return nil
}

func newModule(ping func(context.Context) error) *sales.Module {
	return sales.NewModule(infrastructure.NewStore(), noopStock{}, ping, discardLogger())
}

// TestManifest pins the module's federated contract: the identity the registry
// keys on, the remote entry the shell links to, and the two seller-console
// routes the manifest advertises.
func TestManifest(t *testing.T) {
	t.Parallel()

	manifest := newModule(nil).Manifest()

	if manifest.Name != "sales" {
		t.Errorf("Name = %q, want sales", manifest.Name)
	}
	if manifest.Entry != "/seller/kasir" {
		t.Errorf("Entry = %q, want /seller/kasir", manifest.Entry)
	}
	if got := manifest.Routes["pos"]; got != "/seller/kasir" {
		t.Errorf(`Routes["pos"] = %q, want /seller/kasir`, got)
	}
	if got := manifest.Routes["history"]; got != "/seller/transaksi" {
		t.Errorf(`Routes["history"] = %q, want /seller/transaksi`, got)
	}
}

// TestRegisterRoutesIsANoOp pins that the sales module owns no browsable HTTP
// surface: the seller console reaches it remotely over the sales.v1 contract,
// so mounting it must add no routes to the gateway router.
func TestRegisterRoutesIsANoOp(t *testing.T) {
	t.Parallel()

	router := chi.NewRouter()
	newModule(nil).RegisterRoutes(router)

	routes := 0
	err := chi.Walk(router, func(method, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		routes++
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	if routes != 0 {
		t.Errorf("RegisterRoutes mounted %d routes, want 0: the sales module has no HTTP surface", routes)
	}
}

// TestHealthy pins the readiness report the gateway aggregates. A nil ping is
// the in-memory store's "always ready"; a failing ping must name the module and
// report down with the stable message, never leak the ping error to the caller.
func TestHealthy(t *testing.T) {
	t.Parallel()

	errPing := errors.New("connection refused")

	tests := []struct {
		name        string
		ping        func(context.Context) error
		wantStatus  module.Status
		wantMessage string
	}{
		{"no ping is ok", nil, module.StatusOK, ""},
		{"a healthy ping is ok", func(context.Context) error { return nil }, module.StatusOK, ""},
		{"a failing ping is down", func(context.Context) error { return errPing }, module.StatusDown, "sales database unavailable"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			health := newModule(tt.ping).Healthy(t.Context())
			if health.Module != "sales" {
				t.Errorf("Module = %q, want sales", health.Module)
			}
			if health.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", health.Status, tt.wantStatus)
			}
			if health.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", health.Message, tt.wantMessage)
			}
		})
	}
}

// TestHealthyPingsWithTheCallersContext pins that the probe context flows into
// the backing-database ping, so a request-scoped timeout bounds it.
func TestHealthyPingsWithTheCallersContext(t *testing.T) {
	t.Parallel()

	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "probe")

	var got context.Context
	newModule(func(c context.Context) error {
		got = c
		return nil
	}).Healthy(ctx)

	if got != ctx {
		t.Error("Healthy did not pass its context to the ping")
	}
}

// TestRegisterGRPC pins that the module mounts the sales.v1 contract and the
// standard gRPC health service on a registrar, over the repo's bufconn pairing:
// an empty store answers an empty list, and a nil ping reports SERVING.
func TestRegisterGRPC(t *testing.T) {
	t.Parallel()

	conn, closeServer, err := grpcx.InProcess(newModule(nil).RegisterGRPC)
	if err != nil {
		t.Fatalf("grpcx.InProcess: %v", err)
	}
	defer closeServer()

	sales := salesv1.NewSalesServiceClient(conn)
	resp, err := sales.ListTransactions(t.Context(), &salesv1.ListTransactionsRequest{PageSize: 10})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if got := len(resp.GetTransactions()); got != 0 {
		t.Errorf("empty store listed %d transactions, want 0", got)
	}

	health, err := grpc_health_v1.NewHealthClient(conn).Check(t.Context(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health Check: %v", err)
	}
	if got := health.GetStatus(); got != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("health = %v, want SERVING", got)
	}
}

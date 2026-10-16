// Package inventoryclient_test covers the sales side of the inventory.v1
// contract: the port call is mapped onto the wire request, the tenant rides
// call metadata, business rejections become the sales domain's sentinels, and
// the breaker only trips on transport failures. The stub server runs over the
// repo's bufconn pairing, so there is no network.
package inventoryclient_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "gogo/contracts/inventory/v1"
	"gogo/internal/modules/sales/domain"
	"gogo/internal/modules/sales/infrastructure/inventoryclient"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/tenant"
)

// stubInventory records every AdjustStock call and answers with a fixed error.
type stubInventory struct {
	inventoryv1.UnimplementedInventoryServiceServer

	mu      sync.Mutex
	calls   []*inventoryv1.AdjustStockRequest
	tenants []string
	err     error
}

func (s *stubInventory) AdjustStock(ctx context.Context, req *inventoryv1.AdjustStockRequest) (*inventoryv1.AdjustStockResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, req)
	id, _ := tenant.FromContext(ctx)
	s.tenants = append(s.tenants, string(id))
	return &inventoryv1.AdjustStockResponse{}, s.err
}

func (s *stubInventory) snapshot() (calls []*inventoryv1.AdjustStockRequest, tenants []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*inventoryv1.AdjustStockRequest(nil), s.calls...), append([]string(nil), s.tenants...)
}

func dialStub(t *testing.T, stub inventoryv1.InventoryServiceServer) *grpc.ClientConn {
	t.Helper()

	conn, closeServer, err := grpcx.InProcess(func(s grpc.ServiceRegistrar) {
		inventoryv1.RegisterInventoryServiceServer(s, stub)
	})
	if err != nil {
		t.Fatalf("grpcx.InProcess: %v", err)
	}
	t.Cleanup(closeServer)
	return conn
}

// TestAdjustMapsThePortCallOntoTheContract pins the translation: product,
// signed delta, kind and idempotency reference all arrive on the wire request,
// and the tenant rides call metadata so inventory scopes the adjustment.
func TestAdjustMapsThePortCallOntoTheContract(t *testing.T) {
	t.Parallel()

	stub := &stubInventory{}
	client := inventoryclient.New(dialStub(t, stub), breaker.NewDefault())

	ctx := tenant.WithID(t.Context(), "acme")
	if err := client.Adjust(ctx, "acme", "p-1", -2, "sale", "TRX-1:sale:p-1"); err != nil {
		t.Fatalf("Adjust: %v", err)
	}

	calls, tenants := stub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("AdjustStock calls = %d, want 1", len(calls))
	}
	req := calls[0]
	if req.GetProductId() != "p-1" || req.GetDelta() != -2 || req.GetKind() != "sale" || req.GetReference() != "TRX-1:sale:p-1" {
		t.Errorf("AdjustStock request = %+v, want (p-1, -2, sale, TRX-1:sale:p-1)", req)
	}
	if tenants[0] != "acme" {
		t.Errorf("server saw tenant %q, want acme", tenants[0])
	}
}

// TestAdjustSendsNoTenantWhenThereIsNone pins the shared interceptor rule: an
// empty tenant is silent on the wire, not defaulted by the client, so the
// receiver's own default applies.
func TestAdjustSendsNoTenantWhenThereIsNone(t *testing.T) {
	t.Parallel()

	stub := &stubInventory{}
	client := inventoryclient.New(dialStub(t, stub), breaker.NewDefault())

	if err := client.Adjust(context.Background(), "", "p-1", 1, "adjustment", "ref-1"); err != nil {
		t.Fatalf("Adjust: %v", err)
	}

	_, tenants := stub.snapshot()
	if len(tenants) != 1 || tenants[0] != "" {
		t.Errorf("server saw tenants %v, want none", tenants)
	}
}

// TestAdjustMapsBusinessRejectionsToDomainSentinels pins the error translation:
// FailedPrecondition becomes insufficient stock, NotFound becomes unknown
// product, and everything else keeps its gRPC status so the caller can see the
// transport-level failure.
func TestAdjustMapsBusinessRejectionsToDomainSentinels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rpcErr   error
		wantErr  error
		wantCode codes.Code
	}{
		{"failed precondition is insufficient stock", status.Error(codes.FailedPrecondition, "no stock"), domain.ErrInsufficientStock, codes.OK},
		{"not found is unknown product", status.Error(codes.NotFound, "no product"), domain.ErrProductNotFound, codes.OK},
		{"invalid argument is passed through", status.Error(codes.InvalidArgument, "bad delta"), nil, codes.InvalidArgument},
		{"unavailable is passed through", status.Error(codes.Unavailable, "inventory down"), nil, codes.Unavailable},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubInventory{err: tt.rpcErr}
			client := inventoryclient.New(dialStub(t, stub), breaker.NewDefault())

			err := client.Adjust(tenant.WithID(t.Context(), "acme"), "acme", "p-1", -1, "sale", "ref-1")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Adjust = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if got := status.Code(err); got != tt.wantCode {
				t.Errorf("Adjust status code = %v, want %v", got, tt.wantCode)
			}
		})
	}
}

// TestAdjustTripsTheBreakerOnlyOnTransportFailures pins classify's contract:
// an expected business rejection is a clean answer and must not open the
// circuit, while a transport failure must. With a one-failure threshold the
// second call after an Unavailable is rejected by the breaker itself, without
// reaching the server.
func TestAdjustTripsTheBreakerOnlyOnTransportFailures(t *testing.T) {
	t.Parallel()

	t.Run("a business rejection keeps the circuit closed", func(t *testing.T) {
		t.Parallel()

		stub := &stubInventory{err: status.Error(codes.FailedPrecondition, "no stock")}
		guard := breaker.New(1, time.Minute, 1)
		client := inventoryclient.New(dialStub(t, stub), guard)

		for i := 0; i < 2; i++ {
			err := client.Adjust(tenant.WithID(t.Context(), "acme"), "acme", "p-1", -1, "sale", "ref-1")
			if !errors.Is(err, domain.ErrInsufficientStock) {
				t.Fatalf("Adjust call %d = %v, want ErrInsufficientStock", i+1, err)
			}
		}
		if got := guard.State(); got != breaker.StateClosed {
			t.Errorf("breaker state = %v, want closed after a business rejection", got)
		}
		if calls, _ := stub.snapshot(); len(calls) != 2 {
			t.Errorf("AdjustStock calls = %d, want 2 (the circuit must not reject them)", len(calls))
		}
	})

	t.Run("a transport failure opens the circuit", func(t *testing.T) {
		t.Parallel()

		stub := &stubInventory{err: status.Error(codes.Unavailable, "inventory down")}
		guard := breaker.New(1, time.Minute, 1)
		client := inventoryclient.New(dialStub(t, stub), guard)

		ctx := tenant.WithID(t.Context(), "acme")
		if err := client.Adjust(ctx, "acme", "p-1", -1, "sale", "ref-1"); status.Code(err) != codes.Unavailable {
			t.Fatalf("first Adjust = %v, want Unavailable", err)
		}
		if err := client.Adjust(ctx, "acme", "p-1", -1, "sale", "ref-1"); !errors.Is(err, breaker.ErrOpen) {
			t.Errorf("second Adjust = %v, want breaker.ErrOpen", err)
		}
		if got := guard.State(); got != breaker.StateOpen {
			t.Errorf("breaker state = %v, want open after a transport failure", got)
		}
		if calls, _ := stub.snapshot(); len(calls) != 1 {
			t.Errorf("AdjustStock calls = %d, want 1 (the open circuit must not call through)", len(calls))
		}
	})
}

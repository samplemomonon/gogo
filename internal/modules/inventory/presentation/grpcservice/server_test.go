package grpcservice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "gogo/contracts/inventory/v1"
	"gogo/internal/modules/inventory/application/ports"
	"gogo/internal/modules/inventory/domain"
	"gogo/internal/modules/inventory/infrastructure"
	"gogo/internal/shared/tenant"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeStockService records the arguments the adapter passes down and answers
// with configured values. It implements the full port so a test can state only
// the method under test; the unused methods still return the configured error
// so a wrong call cannot silently look like success.
type fakeStockService struct {
	tenantSeen    domain.Tenant
	productSeen   string
	idsSeen       []string
	deltaSeen     int
	kindSeen      string
	noteSeen      string
	actorSeen     string
	referenceSeen string
	limitSeen     int

	stock        *domain.Stock
	movement     *domain.Movement
	stocks       []*domain.Stock
	movements    []*domain.Movement
	stockErr     error
	adjustErr    error
	stocksErr    error
	movementsErr error
}

func (f *fakeStockService) GetStock(_ context.Context, tenant domain.Tenant, productID string) (*domain.Stock, error) {
	f.tenantSeen = tenant
	f.productSeen = productID
	return f.stock, f.stockErr
}

func (f *fakeStockService) ListStocks(_ context.Context, tenant domain.Tenant, productIDs []string) ([]*domain.Stock, error) {
	f.tenantSeen = tenant
	f.idsSeen = productIDs
	return f.stocks, f.stocksErr
}

func (f *fakeStockService) AdjustStock(_ context.Context, tenant domain.Tenant, productID string, delta int, kind, note, actor, reference string) (*domain.Stock, *domain.Movement, error) {
	f.tenantSeen = tenant
	f.productSeen = productID
	f.deltaSeen = delta
	f.kindSeen = kind
	f.noteSeen = note
	f.actorSeen = actor
	f.referenceSeen = reference
	return f.stock, f.movement, f.adjustErr
}

func (f *fakeStockService) ListMovements(_ context.Context, tenant domain.Tenant, productID string, limit int) ([]*domain.Movement, error) {
	f.tenantSeen = tenant
	f.productSeen = productID
	f.limitSeen = limit
	return f.movements, f.movementsErr
}

func newTestService(svc ports.StockService) *service {
	return &service{svc: svc, logger: testLogger()}
}

// TestToStatus pins the error vocabulary exactly: each domain kind maps to one
// gRPC code and one stable client-facing message. A caller (the seller
// console) branches on these codes, and the messages are the contract that
// internal error detail never leaks.
func TestToStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		wantCode    codes.Code
		wantMessage string
	}{
		{
			// nil is not a success kind: Classify fails closed to Internal,
			// so an errorless call routed through here still signals failure.
			name:        "nil classifies as internal",
			err:         nil,
			wantCode:    codes.Internal,
			wantMessage: "something went wrong",
		},
		{
			name:        "missing stock is not found",
			err:         domain.ErrStockNotFound,
			wantCode:    codes.NotFound,
			wantMessage: "stock not found",
		},
		{
			name:        "insufficient stock is a failed precondition",
			err:         domain.ErrInsufficientStock,
			wantCode:    codes.FailedPrecondition,
			wantMessage: "insufficient stock",
		},
		{
			name:        "invalid adjustment is invalid",
			err:         domain.ErrInvalidAdjustment,
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid stock adjustment",
		},
		{
			name:        "invalid tenant is invalid",
			err:         domain.ErrInvalidTenant,
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid stock adjustment",
		},
		{
			name:        "invalid product is invalid",
			err:         domain.ErrInvalidProductID,
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid stock adjustment",
		},
		{
			name:        "an unknown error is internal",
			err:         errors.New("driver: deadlock detected"),
			wantCode:    codes.Internal,
			wantMessage: "something went wrong",
		},
	}

	svc := newTestService(&fakeStockService{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := svc.toStatus(tc.err)
			if got := status.Code(err); got != tc.wantCode {
				t.Errorf("status.Code = %v, want %v", got, tc.wantCode)
			}
			if got := status.Convert(err).Message(); got != tc.wantMessage {
				t.Errorf("message = %q, want %q", got, tc.wantMessage)
			}
		})
	}

	// The sentinels travel wrapped through the application and the store; the
	// classification must survive the wrap or the mapping disappears at
	// exactly the depth it is needed.
	t.Run("a wrapped sentinel keeps its code", func(t *testing.T) {
		t.Parallel()

		err := svc.toStatus(fmt.Errorf("adjust stock: %w", domain.ErrInsufficientStock))
		if got := status.Code(err); got != codes.FailedPrecondition {
			t.Errorf("status.Code = %v, want FailedPrecondition", got)
		}
	})
}

// TestHandlersClassifyServiceErrors pins that every unary handler routes a
// service error through toStatus rather than inventing its own code or
// returning the raw error. One case per classification, each through the
// handler whose port method returns that error in production.
func TestHandlersClassifyServiceErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		call        func(*service, context.Context) error
		wantCode    codes.Code
		wantMessage string
	}{
		{
			name: "GetStock maps a missing stock to NotFound",
			err:  domain.ErrStockNotFound,
			call: func(s *service, ctx context.Context) error {
				_, err := s.GetStock(ctx, &inventoryv1.GetStockRequest{ProductId: "p-1"})
				return err
			},
			wantCode:    codes.NotFound,
			wantMessage: "stock not found",
		},
		{
			name: "AdjustStock maps oversell to FailedPrecondition",
			err:  domain.ErrInsufficientStock,
			call: func(s *service, ctx context.Context) error {
				_, err := s.AdjustStock(ctx, &inventoryv1.AdjustStockRequest{ProductId: "p-1", Delta: -1})
				return err
			},
			wantCode:    codes.FailedPrecondition,
			wantMessage: "insufficient stock",
		},
		{
			name: "AdjustStock maps a bad request to InvalidArgument",
			err:  domain.ErrInvalidAdjustment,
			call: func(s *service, ctx context.Context) error {
				_, err := s.AdjustStock(ctx, &inventoryv1.AdjustStockRequest{})
				return err
			},
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid stock adjustment",
		},
		{
			name: "ListStockMovements maps an unknown error to Internal",
			err:  errors.New("driver: deadlock detected"),
			call: func(s *service, ctx context.Context) error {
				_, err := s.ListStockMovements(ctx, &inventoryv1.ListStockMovementsRequest{})
				return err
			},
			wantCode:    codes.Internal,
			wantMessage: "something went wrong",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Every port method answers with the same failure; the handler
			// under test is the only one called.
			svc := newTestService(&fakeStockService{
				stockErr:     tc.err,
				adjustErr:    tc.err,
				stocksErr:    tc.err,
				movementsErr: tc.err,
			})

			err := tc.call(svc, context.Background())
			if got := status.Code(err); got != tc.wantCode {
				t.Errorf("status.Code = %v, want %v", got, tc.wantCode)
			}
			message := status.Convert(err).Message()
			if message != tc.wantMessage {
				t.Errorf("message = %q, want %q", message, tc.wantMessage)
			}
			if tc.wantCode == codes.Internal && message == tc.err.Error() {
				t.Errorf("the client-facing message leaks the internal error text %q", message)
			}
		})
	}
}

// TestTenantReachesTheService pins tenantOf: a tenant carried on the context
// reaches the port verbatim, and a context with none resolves to the default
// tenant — the same fallback the HTTP gateway applies to an unmatched host.
func TestTenantReachesTheService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  context.Context
		want domain.Tenant
	}{
		{
			name: "a context tenant reaches the service",
			ctx:  tenant.WithID(context.Background(), "acme"),
			want: "acme",
		},
		{
			name: "an unscoped call falls back to the default tenant",
			ctx:  context.Background(),
			want: domain.Tenant(tenant.Default),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := &fakeStockService{stock: &domain.Stock{ProductID: "p-1", Quantity: 1}}
			svc := newTestService(fixture)

			if _, err := svc.GetStock(tc.ctx, &inventoryv1.GetStockRequest{ProductId: "p-1"}); err != nil {
				t.Fatalf("GetStock: %v", err)
			}
			if fixture.tenantSeen != tc.want {
				t.Errorf("tenant reached the service as %q, want %q", fixture.tenantSeen, tc.want)
			}
		})
	}
}

// TestTenantsAreIsolated pins the boundary through the adapter: two contexts
// naming different tenants are served by the same service over the same store,
// and neither sees the other's stock or ledger rows. This is the adapter half
// of tenant scoping — infrastructure/repository_test.go pins the store half.
func TestTenantsAreIsolated(t *testing.T) {
	t.Parallel()

	svc := newTestService(infrastructure.NewStockRepository())
	ctxA := tenant.WithID(context.Background(), "tenant-a")
	ctxB := tenant.WithID(context.Background(), "tenant-b")

	if _, err := svc.AdjustStock(ctxA, &inventoryv1.AdjustStockRequest{
		ProductId: "p-1", Delta: 9, Kind: "receive", Actor: "tester", Reference: "ref-a",
	}); err != nil {
		t.Fatalf("AdjustStock as tenant-a: %v", err)
	}

	if _, err := svc.GetStock(ctxB, &inventoryv1.GetStockRequest{ProductId: "p-1"}); status.Code(err) != codes.NotFound {
		t.Errorf("GetStock as tenant-b = %v, want NotFound", err)
	}
	if got, err := svc.GetStock(ctxA, &inventoryv1.GetStockRequest{ProductId: "p-1"}); err != nil {
		t.Fatalf("GetStock as tenant-a: %v", err)
	} else if got.GetStock().GetQuantity() != 9 {
		t.Errorf("tenant-a stock = %d, want 9", got.GetStock().GetQuantity())
	}

	list, err := svc.ListStocks(ctxB, &inventoryv1.ListStocksRequest{})
	if err != nil {
		t.Fatalf("ListStocks as tenant-b: %v", err)
	}
	if len(list.GetStocks()) != 0 {
		t.Errorf("tenant-b sees %d stocks, want 0", len(list.GetStocks()))
	}

	movements, err := svc.ListStockMovements(ctxB, &inventoryv1.ListStockMovementsRequest{})
	if err != nil {
		t.Fatalf("ListStockMovements as tenant-b: %v", err)
	}
	if len(movements.GetMovements()) != 0 {
		t.Errorf("tenant-b sees %d movements, want 0", len(movements.GetMovements()))
	}
}

// TestAdjustStockThenGetStockRoundtrip pins the field mapping in both
// directions: the domain values the store returns reach the wire with the
// right quantities, signed deltas, string fields and non-nil timestamps, and a
// subsequent read returns the same level.
func TestAdjustStockThenGetStockRoundtrip(t *testing.T) {
	t.Parallel()

	svc := newTestService(infrastructure.NewStockRepository())
	ctx := tenant.WithID(context.Background(), "acme")

	adjust, err := svc.AdjustStock(ctx, &inventoryv1.AdjustStockRequest{
		ProductId: "p-1", Delta: 7, Kind: "receive", Note: "restock", Actor: "kasir", Reference: "ref-1",
	})
	if err != nil {
		t.Fatalf("AdjustStock: %v", err)
	}

	stock := adjust.GetStock()
	if stock.GetProductId() != "p-1" {
		t.Errorf("stock product = %q, want p-1", stock.GetProductId())
	}
	if stock.GetQuantity() != 7 {
		t.Errorf("stock quantity = %d, want 7", stock.GetQuantity())
	}
	if ts := stock.GetUpdatedAt(); ts == nil || !ts.IsValid() {
		t.Errorf("stock updated_at = %v, want a valid timestamp", ts)
	}

	movement := adjust.GetMovement()
	if movement.GetId() == "" {
		t.Error("movement id is empty")
	}
	if movement.GetProductId() != "p-1" {
		t.Errorf("movement product = %q, want p-1", movement.GetProductId())
	}
	if movement.GetKind() != "receive" || movement.GetNote() != "restock" ||
		movement.GetActor() != "kasir" || movement.GetReference() != "ref-1" {
		t.Errorf("movement fields = %+v, want receive/restock/kasir/ref-1", movement)
	}
	if movement.GetQuantityDelta() != 7 || movement.GetQuantityBefore() != 0 || movement.GetQuantityAfter() != 7 {
		t.Errorf("movement quantities = delta %d before %d after %d, want 7/0/7",
			movement.GetQuantityDelta(), movement.GetQuantityBefore(), movement.GetQuantityAfter())
	}
	if ts := movement.GetCreatedAt(); ts == nil || !ts.IsValid() {
		t.Errorf("movement created_at = %v, want a valid timestamp", ts)
	}

	// A signed delta must survive int -> int32 and the ledger must carry the
	// before/after pair of the second application, not the first.
	decrement, err := svc.AdjustStock(ctx, &inventoryv1.AdjustStockRequest{
		ProductId: "p-1", Delta: -2, Kind: "sale", Actor: "kasir", Reference: "ref-2",
	})
	if err != nil {
		t.Fatalf("AdjustStock (sale): %v", err)
	}
	if got := decrement.GetStock().GetQuantity(); got != 5 {
		t.Errorf("quantity after sale = %d, want 5", got)
	}
	if delta, before, after := decrement.GetMovement().GetQuantityDelta(),
		decrement.GetMovement().GetQuantityBefore(), decrement.GetMovement().GetQuantityAfter(); delta != -2 || before != 7 || after != 5 {
		t.Errorf("sale movement = delta %d before %d after %d, want -2/7/5", delta, before, after)
	}

	got, err := svc.GetStock(ctx, &inventoryv1.GetStockRequest{ProductId: "p-1"})
	if err != nil {
		t.Fatalf("GetStock: %v", err)
	}
	if got.GetStock().GetQuantity() != 5 || got.GetStock().GetUpdatedAt() == nil {
		t.Errorf("read back = %+v, want quantity 5 with a timestamp", got.GetStock())
	}
}

// TestListStocksPassesExplicitIDs pins that the adapter forwards the request's
// product ids verbatim (the store owns selection semantics) and maps every
// returned stock. An empty request must pass no ids, leaving the store to
// return everything.
func TestListStocksPassesExplicitIDs(t *testing.T) {
	t.Parallel()

	now := time.Now()
	fixture := &fakeStockService{
		stocks: []*domain.Stock{
			{ProductID: "p-1", Quantity: 3, UpdatedAt: now},
			{ProductID: "p-2", Quantity: 4, UpdatedAt: now},
		},
	}
	svc := newTestService(fixture)
	ctx := context.Background()

	resp, err := svc.ListStocks(ctx, &inventoryv1.ListStocksRequest{ProductIds: []string{"p-2", "p-1"}})
	if err != nil {
		t.Fatalf("ListStocks: %v", err)
	}
	if len(fixture.idsSeen) != 2 || fixture.idsSeen[0] != "p-2" || fixture.idsSeen[1] != "p-1" {
		t.Errorf("ids reached the store as %v, want [p-2 p-1]", fixture.idsSeen)
	}
	if len(resp.GetStocks()) != 2 {
		t.Fatalf("response carried %d stocks, want 2", len(resp.GetStocks()))
	}
	for i, want := range []struct {
		id       string
		quantity int32
	}{{"p-1", 3}, {"p-2", 4}} {
		stock := resp.GetStocks()[i]
		if stock.GetProductId() != want.id || stock.GetQuantity() != want.quantity {
			t.Errorf("stocks[%d] = %s/%d, want %s/%d", i,
				stock.GetProductId(), stock.GetQuantity(), want.id, want.quantity)
		}
		if stock.GetUpdatedAt() == nil || !stock.GetUpdatedAt().AsTime().Equal(now) {
			t.Errorf("stocks[%d] updated_at = %v, want %v", i, stock.GetUpdatedAt(), now)
		}
	}

	if _, err := svc.ListStocks(ctx, &inventoryv1.ListStocksRequest{}); err != nil {
		t.Fatalf("ListStocks with no ids: %v", err)
	}
	if len(fixture.idsSeen) != 0 {
		t.Errorf("empty request passed ids %v to the store, want none", fixture.idsSeen)
	}
}

// TestListStockMovementsForwardsPaging pins the paging contract: the
// request's page size and product filter reach the store unchanged, and
// mapping preserves the store's ordering. Choosing the default limit is the
// store's job, not the adapter's.
func TestListStockMovementsForwardsPaging(t *testing.T) {
	t.Parallel()

	now := time.Now()
	fixture := &fakeStockService{
		movements: []*domain.Movement{
			{ID: "m-2", ProductID: "p-1", Kind: "sale", Delta: -1, Before: 2, After: 1, CreatedAt: now},
			{ID: "m-1", ProductID: "p-1", Kind: "receive", Delta: 2, Before: 0, After: 2, CreatedAt: now},
		},
	}
	svc := newTestService(fixture)
	ctx := context.Background()

	resp, err := svc.ListStockMovements(ctx, &inventoryv1.ListStockMovementsRequest{ProductId: "p-1", PageSize: 2})
	if err != nil {
		t.Fatalf("ListStockMovements: %v", err)
	}
	if fixture.productSeen != "p-1" {
		t.Errorf("product reached the store as %q, want p-1", fixture.productSeen)
	}
	if fixture.limitSeen != 2 {
		t.Errorf("page size reached the store as %d, want 2", fixture.limitSeen)
	}
	if len(resp.GetMovements()) != 2 {
		t.Fatalf("response carried %d movements, want 2", len(resp.GetMovements()))
	}
	first := resp.GetMovements()[0]
	if first.GetId() != "m-2" || first.GetQuantityDelta() != -1 ||
		first.GetQuantityBefore() != 2 || first.GetQuantityAfter() != 1 {
		t.Errorf("movements[0] = %+v, want the newest movement first", first)
	}
	if first.GetCreatedAt() == nil || !first.GetCreatedAt().AsTime().Equal(now) {
		t.Errorf("movements[0] created_at = %v, want %v", first.GetCreatedAt(), now)
	}

	if _, err := svc.ListStockMovements(ctx, &inventoryv1.ListStockMovementsRequest{}); err != nil {
		t.Fatalf("ListStockMovements with no request fields: %v", err)
	}
	if fixture.limitSeen != 0 || fixture.productSeen != "" {
		t.Errorf("empty request forwarded product %q limit %d; the store owns the defaults",
			fixture.productSeen, fixture.limitSeen)
	}
}

// TestNilDomainValuesMapToNilProtos pins the nil guard in both converters: a
// store may hand back a nil stock or movement, and the adapter must map that
// to nil rather than panic on a field access.
func TestNilDomainValuesMapToNilProtos(t *testing.T) {
	t.Parallel()

	if got := stockToProto(nil); got != nil {
		t.Errorf("stockToProto(nil) = %v, want nil", got)
	}
	if got := movementToProto(nil); got != nil {
		t.Errorf("movementToProto(nil) = %v, want nil", got)
	}
}

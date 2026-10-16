// Package inventoryclient_test covers the console's adapter over the inventory.v1
// gRPC contract. The stub below is a contract-only server: it knows the generated
// wire types and nothing of gogo's inventory module, so an assertion here proves
// the adapter maps the wire *and* that no page code needs to.
package inventoryclient_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	inventoryv1 "gogo/contracts/inventory/v1"
	"gogo/internal/pages/seller/infrastructure/inventoryclient"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/tenant"
)

// outgoingTenant mirrors grpcx's production dial interceptor: the tenant travels
// as call metadata, so a bufconn pairing must attach the same convention or an
// assertion about the stub seeing the tenant would only test the stub.
func outgoingTenant() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if id, ok := tenant.FromContext(ctx); ok {
			ctx = metadata.AppendToOutgoingContext(ctx, tenant.Header, string(id))
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// tenantIncoming mirrors grpcx's production server interceptor.
func tenantIncoming() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if values := md.Get(tenant.Header); len(values) > 0 && values[0] != "" {
				ctx = tenant.WithID(ctx, tenant.ID(values[0]))
			}
		}
		return handler(ctx, req)
	}
}

func dialStub(t *testing.T, register func(grpc.ServiceRegistrar)) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(tenantIncoming()))
	register(server)
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(outgoingTenant()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		server.Stop()
		_ = conn.Close()
	})
	return conn
}

// stubInventory implements just enough of the contract to drive the adapter,
// recording every request and the tenant the server convention put on the
// handler's context.
type stubInventory struct {
	inventoryv1.UnimplementedInventoryServiceServer

	mu sync.Mutex

	listStocksFn    func(context.Context, *inventoryv1.ListStocksRequest) (*inventoryv1.ListStocksResponse, error)
	adjustStockFn   func(context.Context, *inventoryv1.AdjustStockRequest) (*inventoryv1.AdjustStockResponse, error)
	listMovementsFn func(context.Context, *inventoryv1.ListStockMovementsRequest) (*inventoryv1.ListStockMovementsResponse, error)

	listCalls     int
	adjustCalls   int
	movementCalls int
	tenants       []string

	lastList      *inventoryv1.ListStocksRequest
	lastAdjust    *inventoryv1.AdjustStockRequest
	lastMovements *inventoryv1.ListStockMovementsRequest
}

func (s *stubInventory) record(ctx context.Context) {
	if id, ok := tenant.FromContext(ctx); ok {
		s.tenants = append(s.tenants, string(id))
	} else {
		s.tenants = append(s.tenants, "")
	}
}

func (s *stubInventory) ListStocks(ctx context.Context, req *inventoryv1.ListStocksRequest) (*inventoryv1.ListStocksResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	s.lastList = req
	s.record(ctx)
	if s.listStocksFn == nil {
		return &inventoryv1.ListStocksResponse{}, nil
	}
	return s.listStocksFn(ctx, req)
}

func (s *stubInventory) AdjustStock(ctx context.Context, req *inventoryv1.AdjustStockRequest) (*inventoryv1.AdjustStockResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adjustCalls++
	s.lastAdjust = req
	s.record(ctx)
	if s.adjustStockFn == nil {
		return &inventoryv1.AdjustStockResponse{}, nil
	}
	return s.adjustStockFn(ctx, req)
}

func (s *stubInventory) ListStockMovements(ctx context.Context, req *inventoryv1.ListStockMovementsRequest) (*inventoryv1.ListStockMovementsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.movementCalls++
	s.lastMovements = req
	s.record(ctx)
	if s.listMovementsFn == nil {
		return &inventoryv1.ListStockMovementsResponse{}, nil
	}
	return s.listMovementsFn(ctx, req)
}

func (s *stubInventory) counts() (list, adjust, movements int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls, s.adjustCalls, s.movementCalls
}

func (s *stubInventory) seenTenants() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tenants...)
}

func newClient(t *testing.T, stub *stubInventory, b *breaker.Breaker) *inventoryclient.Client {
	t.Helper()
	return inventoryclient.New(dialStub(t, func(s grpc.ServiceRegistrar) {
		inventoryv1.RegisterInventoryServiceServer(s, stub)
	}), b)
}

func TestListStocksMapsTheWireAndScopesTheRequest(t *testing.T) {
	t.Parallel()

	stub := &stubInventory{listStocksFn: func(_ context.Context, req *inventoryv1.ListStocksRequest) (*inventoryv1.ListStocksResponse, error) {
		return &inventoryv1.ListStocksResponse{Stocks: []*inventoryv1.Stock{
			{ProductId: "7", Quantity: 3},
			{ProductId: "9", Quantity: 0},
		}}, nil
	}}
	client := newClient(t, stub, breaker.NewDefault())

	stocks, err := client.ListStocks(context.Background(), []string{"7", "9"})
	if err != nil {
		t.Fatalf("ListStocks: %v", err)
	}
	if len(stocks) != 2 {
		t.Fatalf("len(stocks) = %d, want 2", len(stocks))
	}
	if stocks[0].ProductID != "7" || stocks[0].Quantity != 3 {
		t.Errorf("stocks[0] = %+v, want product 7 quantity 3", stocks[0])
	}
	if stocks[1].ProductID != "9" || stocks[1].Quantity != 0 {
		t.Errorf("stocks[1] = %+v, want product 9 quantity 0", stocks[1])
	}

	// The requested ids travel verbatim: the console's batched lookup is only
	// batched if the adapter forwards the whole slice in one request.
	if got := stub.lastList.GetProductIds(); len(got) != 2 || got[0] != "7" || got[1] != "9" {
		t.Errorf("wire request product_ids = %v, want [7 9]", got)
	}
	if list, _, _ := stub.counts(); list != 1 {
		t.Errorf("list calls = %d, want 1", list)
	}
}

// TestListStocksWithNoIDsAsksForEveryRow pins the contract's own reading: an
// empty product_ids lists every stock row for the tenant, which is what the
// stock page needs.
func TestListStocksWithNoIDsAsksForEveryRow(t *testing.T) {
	t.Parallel()

	stub := &stubInventory{}
	client := newClient(t, stub, breaker.NewDefault())

	if _, err := client.ListStocks(context.Background(), nil); err != nil {
		t.Fatalf("ListStocks: %v", err)
	}
	if got := stub.lastList.GetProductIds(); len(got) != 0 {
		t.Errorf("wire request product_ids = %v, want none", got)
	}
}

func TestAdjustStockMapsRequestAndResponse(t *testing.T) {
	t.Parallel()

	created := time.Unix(1750000000, 0).UTC()
	stub := &stubInventory{adjustStockFn: func(_ context.Context, req *inventoryv1.AdjustStockRequest) (*inventoryv1.AdjustStockResponse, error) {
		return &inventoryv1.AdjustStockResponse{
			Stock: &inventoryv1.Stock{ProductId: req.GetProductId(), Quantity: 12},
			Movement: &inventoryv1.StockMovement{
				ProductId:      req.GetProductId(),
				Kind:           req.GetKind(),
				QuantityDelta:  5,
				QuantityBefore: 7,
				QuantityAfter:  12,
				Note:           "kiriman pagi",
				Actor:          "Siti",
				CreatedAt:      timestamppb.New(created),
			},
		}, nil
	}}
	client := newClient(t, stub, breaker.NewDefault())

	stock, movement, err := client.AdjustStock(context.Background(), "7", 5, "restock", "kiriman pagi", "Siti", "INV-1")
	if err != nil {
		t.Fatalf("AdjustStock: %v", err)
	}

	req := stub.lastAdjust
	if req.GetProductId() != "7" || req.GetDelta() != 5 || req.GetKind() != "restock" ||
		req.GetNote() != "kiriman pagi" || req.GetActor() != "Siti" || req.GetReference() != "INV-1" {
		t.Errorf("wire request = %+v, want every form field forwarded", req)
	}

	if stock == nil || stock.ProductID != "7" || stock.Quantity != 12 {
		t.Errorf("stock = %+v, want product 7 quantity 12", stock)
	}
	if movement == nil {
		t.Fatal("movement = nil, want the ledger entry the contract returned")
	}
	if movement.ProductID != "7" || movement.Kind != "restock" ||
		movement.Delta != 5 || movement.Before != 7 || movement.After != 12 ||
		movement.Note != "kiriman pagi" || movement.Actor != "Siti" {
		t.Errorf("movement = %+v, want the wire fields mapped", movement)
	}
	if !movement.CreatedAt.Equal(created) {
		t.Errorf("movement.CreatedAt = %v, want %v", movement.CreatedAt, created)
	}
}

// TestAdjustStockWithoutAMovementReturnsNil pins the nil branch: the contract's
// movement is optional, and the adapter must not fabricate a zero-valued ledger
// entry the caller would render as a real one.
func TestAdjustStockWithoutAMovementReturnsNil(t *testing.T) {
	t.Parallel()

	stub := &stubInventory{adjustStockFn: func(context.Context, *inventoryv1.AdjustStockRequest) (*inventoryv1.AdjustStockResponse, error) {
		return &inventoryv1.AdjustStockResponse{Stock: &inventoryv1.Stock{ProductId: "7", Quantity: 12}}, nil
	}}
	client := newClient(t, stub, breaker.NewDefault())

	_, movement, err := client.AdjustStock(context.Background(), "7", 5, "restock", "", "", "")
	if err != nil {
		t.Fatalf("AdjustStock: %v", err)
	}
	if movement != nil {
		t.Errorf("movement = %+v, want nil when the contract carried none", movement)
	}
}

func TestListMovementsMapsEachEntry(t *testing.T) {
	t.Parallel()

	created := time.Unix(1750000100, 0).UTC()
	stub := &stubInventory{listMovementsFn: func(_ context.Context, req *inventoryv1.ListStockMovementsRequest) (*inventoryv1.ListStockMovementsResponse, error) {
		return &inventoryv1.ListStockMovementsResponse{Movements: []*inventoryv1.StockMovement{
			{ProductId: "7", Kind: "adjustment", QuantityDelta: -2, QuantityBefore: 5, QuantityAfter: 3, Note: "rusak", Actor: "Budi", CreatedAt: timestamppb.New(created)},
			{ProductId: "9", Kind: "opname"},
		}}, nil
	}}
	client := newClient(t, stub, breaker.NewDefault())

	movements, err := client.ListMovements(context.Background(), "7", 50)
	if err != nil {
		t.Fatalf("ListMovements: %v", err)
	}
	if len(movements) != 2 {
		t.Fatalf("len(movements) = %d, want 2", len(movements))
	}
	if movements[0].ProductID != "7" || movements[0].Delta != -2 || movements[0].Before != 5 ||
		movements[0].After != 3 || movements[0].Note != "rusak" || movements[0].Actor != "Budi" {
		t.Errorf("movements[0] = %+v, want the ledger entry mapped", movements[0])
	}
	if !movements[0].CreatedAt.Equal(created) {
		t.Errorf("movements[0].CreatedAt = %v, want %v", movements[0].CreatedAt, created)
	}
	// A contract that carries no timestamp is "no timestamp", not the epoch: the
	// console must be able to tell the two apart.
	if !movements[1].CreatedAt.IsZero() {
		t.Errorf("movements[1].CreatedAt = %v, want the zero time for an absent timestamp", movements[1].CreatedAt)
	}

	if got := stub.lastMovements.GetProductId(); got != "7" {
		t.Errorf("wire request product_id = %q, want 7", got)
	}
	if got := stub.lastMovements.GetPageSize(); got != 50 {
		t.Errorf("wire request page_size = %d, want 50", got)
	}
}

// TestInventoryAnswersAreDistinguishable pins the vocabulary: every answer the
// contract documents has its own sentinel, and a status the vocabulary does not
// name is an outage, not an invented refusal.
func TestInventoryAnswersAreDistinguishable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		code codes.Code
		want error
	}{
		{"unknown product", codes.NotFound, inventoryclient.ErrUnknownProduct},
		{"invalid adjustment", codes.InvalidArgument, inventoryclient.ErrInvalidAdjustment},
		{"insufficient stock", codes.FailedPrecondition, inventoryclient.ErrInsufficientStock},
		{"unreachable", codes.Unavailable, inventoryclient.ErrInventoryUnavailable},
		{"unnamed answer", codes.Internal, inventoryclient.ErrInventoryUnavailable},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubInventory{listStocksFn: func(context.Context, *inventoryv1.ListStocksRequest) (*inventoryv1.ListStocksResponse, error) {
				return nil, status.Error(tc.code, "inventory says no")
			}}
			client := newClient(t, stub, breaker.NewDefault())

			_, err := client.ListStocks(context.Background(), nil)
			if !errors.Is(err, tc.want) {
				t.Errorf("code %s mapped to %v, want errors.Is(_, %v)", tc.code, err, tc.want)
			}
		})
	}
}

// TestTheTenantIsStampedOnEveryInventoryCall pins the calling convention the
// adapter inherits: the tenant on the caller's context reaches the wire, and a
// caller with none sends the default tenant rather than an empty one.
func TestTheTenantIsStampedOnEveryInventoryCall(t *testing.T) {
	t.Parallel()

	stub := &stubInventory{}
	client := newClient(t, stub, breaker.NewDefault())

	if _, err := client.ListStocks(tenant.WithID(context.Background(), "acme"), nil); err != nil {
		t.Fatalf("ListStocks: %v", err)
	}
	if _, _, err := client.AdjustStock(context.Background(), "7", 1, "restock", "", "", ""); err != nil {
		t.Fatalf("AdjustStock: %v", err)
	}

	seen := stub.seenTenants()
	if len(seen) != 2 {
		t.Fatalf("server saw %d calls, want 2", len(seen))
	}
	if seen[0] != "acme" {
		t.Errorf("first call tenant = %q, want acme", seen[0])
	}
	if seen[1] != "default" {
		t.Errorf("second call tenant = %q, want the default tenant, never empty", seen[1])
	}
}

// TestTheBreakerGuardsInventoryCalls pins that the adapter's calls run under the
// breaker it is given: a transport failure opens the circuit and the next call
// is answered without dialing the stub, while a business refusal never trips it.
func TestTheBreakerGuardsInventoryCalls(t *testing.T) {
	t.Parallel()

	t.Run("a transport failure trips the circuit", func(t *testing.T) {
		t.Parallel()

		stub := &stubInventory{listStocksFn: func(context.Context, *inventoryv1.ListStocksRequest) (*inventoryv1.ListStocksResponse, error) {
			return nil, status.Error(codes.Unavailable, "inventory unreachable")
		}}
		b := breaker.New(1, time.Minute, 1)
		client := newClient(t, stub, b)

		if _, err := client.ListStocks(context.Background(), nil); !errors.Is(err, inventoryclient.ErrInventoryUnavailable) {
			t.Fatalf("first ListStocks = %v, want the unavailable sentinel", err)
		}
		if b.State() != breaker.StateOpen {
			t.Fatalf("breaker state after a transport failure = %v, want open", b.State())
		}
		if _, err := client.ListStocks(context.Background(), nil); !errors.Is(err, inventoryclient.ErrInventoryUnavailable) {
			t.Fatalf("second ListStocks = %v, want the unavailable sentinel", err)
		}
		if list, _, _ := stub.counts(); list != 1 {
			t.Errorf("stub calls = %d, want 1: the open circuit must fail fast without dialing", list)
		}
	})

	t.Run("a business refusal never trips the circuit", func(t *testing.T) {
		t.Parallel()

		stub := &stubInventory{listStocksFn: func(context.Context, *inventoryv1.ListStocksRequest) (*inventoryv1.ListStocksResponse, error) {
			return nil, status.Error(codes.NotFound, "no such product")
		}}
		b := breaker.New(1, time.Minute, 1)
		client := newClient(t, stub, b)

		for i := 0; i < 2; i++ {
			if _, err := client.ListStocks(context.Background(), nil); !errors.Is(err, inventoryclient.ErrUnknownProduct) {
				t.Fatalf("ListStocks #%d = %v, want the unknown-product sentinel", i+1, err)
			}
		}
		if b.State() != breaker.StateClosed {
			t.Errorf("breaker state after business refusals = %v, want closed", b.State())
		}
		if list, _, _ := stub.counts(); list != 2 {
			t.Errorf("stub calls = %d, want 2: a healthy refusal is not a failure", list)
		}
	})
}

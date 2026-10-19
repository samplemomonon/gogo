// Package salesclient_test covers the console's adapter over the sales.v1 gRPC
// contract. The stub is a contract-only server: it records wire requests and
// answers with wire responses, so every assertion here proves the adapter's
// translation in the contract's own terms.
package salesclient_test

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

	salesv1 "gogo/contracts/sales/v1"
	"gogo/internal/features/sales/ui/application/results"
	"gogo/internal/features/sales/ui/infrastructure/salesclient"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/tenant"
)

// The tenant interceptors mirror grpcx's production pair so the bufconn pairing
// carries the same calling convention as a deployed dial.
func outgoingTenant() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if id, ok := tenant.FromContext(ctx); ok {
			ctx = metadata.AppendToOutgoingContext(ctx, tenant.Header, string(id))
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

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

func dialStub(t *testing.T, stub *stubSales) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(tenantIncoming()))
	salesv1.RegisterSalesServiceServer(server, stub)
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

type stubSales struct {
	salesv1.UnimplementedSalesServiceServer

	mu sync.Mutex

	listFn     func(context.Context, *salesv1.ListTransactionsRequest) (*salesv1.ListTransactionsResponse, error)
	checkoutFn func(context.Context, *salesv1.CheckoutRequest) (*salesv1.CheckoutResponse, error)
	voidFn     func(context.Context, *salesv1.VoidTransactionRequest) (*salesv1.VoidTransactionResponse, error)

	listCalls     int
	checkoutCalls int
	voidCalls     int
	tenants       []string

	lastList     *salesv1.ListTransactionsRequest
	lastCheckout *salesv1.CheckoutRequest
	lastVoid     *salesv1.VoidTransactionRequest
}

func (s *stubSales) record(ctx context.Context) {
	if id, ok := tenant.FromContext(ctx); ok {
		s.tenants = append(s.tenants, string(id))
	} else {
		s.tenants = append(s.tenants, "")
	}
}

func (s *stubSales) ListTransactions(ctx context.Context, req *salesv1.ListTransactionsRequest) (*salesv1.ListTransactionsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	s.lastList = req
	s.record(ctx)
	if s.listFn == nil {
		return &salesv1.ListTransactionsResponse{}, nil
	}
	return s.listFn(ctx, req)
}

func (s *stubSales) Checkout(ctx context.Context, req *salesv1.CheckoutRequest) (*salesv1.CheckoutResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkoutCalls++
	s.lastCheckout = req
	s.record(ctx)
	if s.checkoutFn == nil {
		return &salesv1.CheckoutResponse{}, nil
	}
	return s.checkoutFn(ctx, req)
}

func (s *stubSales) VoidTransaction(ctx context.Context, req *salesv1.VoidTransactionRequest) (*salesv1.VoidTransactionResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.voidCalls++
	s.lastVoid = req
	s.record(ctx)
	if s.voidFn == nil {
		return &salesv1.VoidTransactionResponse{}, nil
	}
	return s.voidFn(ctx, req)
}

func (s *stubSales) counts() (list, checkout, void int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls, s.checkoutCalls, s.voidCalls
}

func (s *stubSales) seenTenants() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tenants...)
}

func newClient(t *testing.T, stub *stubSales, b *breaker.Breaker) *salesclient.Client {
	t.Helper()
	return salesclient.New(dialStub(t, stub), b)
}

func TestListAsksForOneHundredAndDecodesEachSale(t *testing.T) {
	t.Parallel()

	created := time.Unix(1750000000, 0).UTC()
	voided := time.Unix(1750000600, 0).UTC()
	stub := &stubSales{listFn: func(context.Context, *salesv1.ListTransactionsRequest) (*salesv1.ListTransactionsResponse, error) {
		return &salesv1.ListTransactionsResponse{Transactions: []*salesv1.Transaction{
			{
				Id: "11", Number: "INV-11", Total: 25000, Discount: 1000, Paid: 30000, Change: 4000,
				PaymentMethod: "cash", Cashier: "Siti", EmployeeId: "3", CreatedAt: timestamppb.New(created),
				Items: []*salesv1.TransactionItem{
					{Id: "1", ProductId: "7", Name: "Kopi", Quantity: 2, Price: 10000, Discount: 500, Subtotal: 19500},
					{Id: "2", ProductId: "9", Name: "Teh", Quantity: 1, Price: 6500, Subtotal: 6500},
				},
			},
			{
				Id: "12", Number: "INV-12", Total: 8000, Paid: 8000, PaymentMethod: "qris", Cashier: "Budi",
				CreatedAt: timestamppb.New(created), VoidedAt: timestamppb.New(voided), VoidedBy: "Siti", VoidReason: "salah input",
			},
		}}, nil
	}}
	client := newClient(t, stub, breaker.NewDefault())

	transactions, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(transactions) != 2 {
		t.Fatalf("len(transactions) = %d, want 2", len(transactions))
	}
	if got := stub.lastList.GetPageSize(); got != 100 {
		t.Errorf("wire page_size = %d, want 100 (one console page)", got)
	}

	first := transactions[0]
	if first.ID != "11" || first.Number != "INV-11" || first.Total != 25000 || first.Discount != 1000 ||
		first.Paid != 30000 || first.Change != 4000 || first.PaymentMethod != "cash" ||
		first.Cashier != "Siti" || first.EmployeeID != "3" {
		t.Errorf("transactions[0] = %+v, want the wire row decoded", first)
	}
	if !first.CreatedAt.Equal(created) {
		t.Errorf("transactions[0].CreatedAt = %v, want %v", first.CreatedAt, created)
	}
	if len(first.Items) != 2 {
		t.Fatalf("len(transactions[0].Items) = %d, want 2", len(first.Items))
	}
	if item := first.Items[0]; item.ProductID != "7" || item.Name != "Kopi" || item.Quantity != 2 ||
		item.Price != 10000 || item.Discount != 500 || item.Subtotal != 19500 {
		t.Errorf("transactions[0].Items[0] = %+v, want every wire field decoded", item)
	}
	// A sale that was never voided reports no void moment, so the console can
	// tell it apart from a void that happened at the epoch.
	if first.VoidedAt != nil {
		t.Errorf("transactions[0].VoidedAt = %v, want nil for a live sale", first.VoidedAt)
	}

	second := transactions[1]
	if second.VoidedAt == nil || !second.VoidedAt.Equal(voided) {
		t.Errorf("transactions[1].VoidedAt = %v, want %v", second.VoidedAt, voided)
	}
	if second.VoidedBy != "Siti" || second.VoidReason != "salah input" {
		t.Errorf("transactions[1] void fields = %q/%q, want Siti/salah input", second.VoidedBy, second.VoidReason)
	}
	if len(second.Items) != 0 {
		t.Errorf("transactions[1].Items = %v, want an empty slice for a sale with no lines", second.Items)
	}
}

func TestCheckoutSendsTheBasketAndDecodesTheCommittedSale(t *testing.T) {
	t.Parallel()

	stub := &stubSales{checkoutFn: func(_ context.Context, req *salesv1.CheckoutRequest) (*salesv1.CheckoutResponse, error) {
		// The committed sale answers: total and change are the sales service's
		// arithmetic, not an echo of what the console sent.
		return &salesv1.CheckoutResponse{Transaction: &salesv1.Transaction{
			Id: "21", Number: "INV-21", Total: int64(25000 - req.GetDiscount()), Discount: req.GetDiscount(),
			Paid: req.GetPaid(), Change: req.GetPaid() + req.GetDiscount() - 25000,
			PaymentMethod: req.GetPaymentMethod(), Cashier: req.GetCashier(), EmployeeId: req.GetEmployeeId(),
			Items: []*salesv1.TransactionItem{
				{Id: "31", ProductId: "7", Name: "Kopi", Quantity: 2, Price: 10000, Discount: 500, Subtotal: 19500},
			},
		}}, nil
	}}
	client := newClient(t, stub, breaker.NewDefault())

	committed, err := client.Checkout(context.Background(),
		[]*results.TransactionItem{
			{ProductID: "7", Name: "Kopi", Quantity: 2, Price: 10000, Discount: 500},
			{ProductID: "9", Name: "Teh", Quantity: 1, Price: 6500},
		},
		1000, 30000, "cash", "Siti", "3")
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	req := stub.lastCheckout
	if len(req.GetItems()) != 2 {
		t.Fatalf("wire items = %d, want 2", len(req.GetItems()))
	}
	if item := req.GetItems()[0]; item.GetProductId() != "7" || item.GetName() != "Kopi" ||
		item.GetQuantity() != 2 || item.GetPrice() != 10000 || item.GetDiscount() != 500 {
		t.Errorf("wire items[0] = %+v, want the basket line forwarded", item)
	}
	if item := req.GetItems()[1]; item.GetProductId() != "9" || item.GetQuantity() != 1 || item.GetPrice() != 6500 {
		t.Errorf("wire items[1] = %+v, want the basket line forwarded", item)
	}
	if req.GetDiscount() != 1000 || req.GetPaid() != 30000 {
		t.Errorf("wire discount/paid = %d/%d, want 1000/30000", req.GetDiscount(), req.GetPaid())
	}
	if req.GetPaymentMethod() != "cash" || req.GetCashier() != "Siti" || req.GetEmployeeId() != "3" {
		t.Errorf("wire method/cashier/employee = %q/%q/%q, want cash/Siti/3", req.GetPaymentMethod(), req.GetCashier(), req.GetEmployeeId())
	}

	if committed == nil {
		t.Fatal("committed = nil, want the committed sale")
	}
	if committed.ID != "21" || committed.Number != "INV-21" || committed.Total != 24000 || committed.Change != 6000 {
		t.Errorf("committed = %+v, want the sales service's own total and change", committed)
	}
	if len(committed.Items) != 1 || committed.Items[0].Subtotal != 19500 {
		t.Errorf("committed.Items = %+v, want the computed subtotal decoded", committed.Items)
	}
}

// TestVoidCarriesWhoAndWhy pins that a void is attributable: the id names the sale
// and the actor and reason travel so the voided sale can be audited later.
func TestVoidCarriesWhoAndWhy(t *testing.T) {
	t.Parallel()

	voided := time.Unix(1750000600, 0).UTC()
	stub := &stubSales{voidFn: func(_ context.Context, req *salesv1.VoidTransactionRequest) (*salesv1.VoidTransactionResponse, error) {
		return &salesv1.VoidTransactionResponse{Transaction: &salesv1.Transaction{
			Id: req.GetId(), Number: "INV-21", VoidedAt: timestamppb.New(voided),
			VoidedBy: req.GetVoidedBy(), VoidReason: req.GetReason(),
		}}, nil
	}}
	client := newClient(t, stub, breaker.NewDefault())

	transaction, err := client.Void(context.Background(), "21", "Siti", "salah input")
	if err != nil {
		t.Fatalf("Void: %v", err)
	}

	req := stub.lastVoid
	if req.GetId() != "21" || req.GetVoidedBy() != "Siti" || req.GetReason() != "salah input" {
		t.Errorf("wire request = %+v, want id/voided_by/reason forwarded", req)
	}
	if transaction.VoidedAt == nil || !transaction.VoidedAt.Equal(voided) {
		t.Errorf("VoidedAt = %v, want %v", transaction.VoidedAt, voided)
	}
	if transaction.VoidedBy != "Siti" || transaction.VoidReason != "salah input" {
		t.Errorf("void fields = %q/%q, want Siti/salah input", transaction.VoidedBy, transaction.VoidReason)
	}
	if _, _, void := stub.counts(); void != 1 {
		t.Errorf("void calls = %d, want 1", void)
	}
}

// TestCheckoutWithoutACommittedSaleReturnsNil pins the optional response branch: a
// contract that answers with no transaction must not become a fabricated empty
// sale a handler would then redirect to.
func TestCheckoutWithoutACommittedSaleReturnsNil(t *testing.T) {
	t.Parallel()

	stub := &stubSales{}
	client := newClient(t, stub, breaker.NewDefault())

	transaction, err := client.Checkout(context.Background(), nil, 0, 0, "", "", "")
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if transaction != nil {
		t.Errorf("transaction = %+v, want nil when the contract carried none", transaction)
	}
}

// TestSalesAnswersAreDistinguishable pins the vocabulary: each documented answer
// has its own sentinel and an unnamed status is an outage.
func TestSalesAnswersAreDistinguishable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		code codes.Code
		want error
	}{
		{"unknown transaction", codes.NotFound, salesclient.ErrTransactionNotFound},
		{"invalid basket", codes.InvalidArgument, salesclient.ErrInvalidCheckout},
		{"refused sale", codes.FailedPrecondition, salesclient.ErrSaleRefused},
		{"unreachable", codes.Unavailable, salesclient.ErrSalesUnavailable},
		{"unnamed answer", codes.Internal, salesclient.ErrSalesUnavailable},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubSales{listFn: func(context.Context, *salesv1.ListTransactionsRequest) (*salesv1.ListTransactionsResponse, error) {
				return nil, status.Error(tc.code, "sales says no")
			}}
			client := newClient(t, stub, breaker.NewDefault())

			_, err := client.List(context.Background())
			if !errors.Is(err, tc.want) {
				t.Errorf("code %s mapped to %v, want errors.Is(_, %v)", tc.code, err, tc.want)
			}
		})
	}
}

// TestTheBreakerGuardsSalesCalls pins that the adapter's calls run under the
// breaker it is given: a transport failure opens the circuit and the next call is
// answered without dialing, while a business refusal never trips it.
func TestTheBreakerGuardsSalesCalls(t *testing.T) {
	t.Parallel()

	t.Run("a transport failure trips the circuit", func(t *testing.T) {
		t.Parallel()

		stub := &stubSales{listFn: func(context.Context, *salesv1.ListTransactionsRequest) (*salesv1.ListTransactionsResponse, error) {
			return nil, status.Error(codes.Unavailable, "sales unreachable")
		}}
		client := newClient(t, stub, breaker.New(1, time.Minute, 1))

		if _, err := client.List(context.Background()); !errors.Is(err, salesclient.ErrSalesUnavailable) {
			t.Fatalf("first List = %v, want the unavailable sentinel", err)
		}
		if _, err := client.List(context.Background()); !errors.Is(err, salesclient.ErrSalesUnavailable) {
			t.Fatalf("second List = %v, want the unavailable sentinel", err)
		}
		if list, _, _ := stub.counts(); list != 1 {
			t.Errorf("stub calls = %d, want 1: the open circuit must fail fast", list)
		}
	})

	t.Run("a business refusal never trips the circuit", func(t *testing.T) {
		t.Parallel()

		stub := &stubSales{checkoutFn: func(context.Context, *salesv1.CheckoutRequest) (*salesv1.CheckoutResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "pembayaran kurang")
		}}
		client := newClient(t, stub, breaker.New(1, time.Minute, 1))

		for i := 0; i < 2; i++ {
			if _, err := client.Checkout(context.Background(), nil, 0, 0, "cash", "Siti", ""); !errors.Is(err, salesclient.ErrSaleRefused) {
				t.Fatalf("Checkout #%d = %v, want the refused-sale sentinel", i+1, err)
			}
		}
		if _, checkout, _ := stub.counts(); checkout != 2 {
			t.Errorf("stub calls = %d, want 2: a refusal is the service working", checkout)
		}
	})
}

// TestTheTenantIsStampedOnEverySalesCall pins the shared calling convention: the
// request tenant reaches the wire, and a caller with none sends the default
// tenant rather than an empty one.
func TestTheTenantIsStampedOnEverySalesCall(t *testing.T) {
	t.Parallel()

	stub := &stubSales{}
	client := newClient(t, stub, breaker.NewDefault())

	if _, err := client.List(tenant.WithID(context.Background(), "acme")); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := client.Checkout(context.Background(), nil, 0, 0, "", "", ""); err != nil {
		t.Fatalf("Checkout: %v", err)
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

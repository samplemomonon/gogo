package grpcservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	salesv1 "gogo/contracts/sales/v1"
	"gogo/internal/modules/sales/application"
	"gogo/internal/modules/sales/application/ports"
	"gogo/internal/modules/sales/domain"
	"gogo/internal/modules/sales/infrastructure"
	"gogo/internal/shared/tenant"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubStock records how many stock adjustments the application service made.
type stubStock struct{ calls int }

func (s *stubStock) Adjust(context.Context, domain.Tenant, string, int, string, string) error {
	s.calls++
	return nil
}

func newWiredService() (*service, *infrastructure.Store, *stubStock) {
	store := infrastructure.NewStore()
	stock := &stubStock{}
	return &service{svc: application.NewService(store, stock), logger: discardLogger()}, store, stock
}

// recordingSales is a ports.SalesService that records what each handler passed
// it and answers with a fixed result and error.
type recordingSales struct {
	tenant domain.Tenant

	items                       []domain.CheckoutItem
	discount, paid              int64
	method, cashier, employeeID string

	id, by, reason string
	limit          int

	result *domain.Transaction
	err    error
}

var _ ports.SalesService = (*recordingSales)(nil)

func (r *recordingSales) Checkout(_ context.Context, tenant domain.Tenant, items []domain.CheckoutItem, discount, paid int64, method, cashier, employeeID string) (*domain.Transaction, error) {
	r.tenant = tenant
	r.items = items
	r.discount, r.paid = discount, paid
	r.method, r.cashier, r.employeeID = method, cashier, employeeID
	return r.result, r.err
}

func (r *recordingSales) Get(_ context.Context, tenant domain.Tenant, id string) (*domain.Transaction, error) {
	r.tenant, r.id = tenant, id
	return r.result, r.err
}

func (r *recordingSales) List(_ context.Context, tenant domain.Tenant, limit int) ([]*domain.Transaction, error) {
	r.tenant, r.limit = tenant, limit
	if r.err != nil {
		return nil, r.err
	}
	if r.result == nil {
		return nil, nil
	}
	return []*domain.Transaction{r.result}, nil
}

func (r *recordingSales) Void(_ context.Context, tenant domain.Tenant, id, by, reason string) (*domain.Transaction, error) {
	r.tenant, r.id, r.by, r.reason = tenant, id, by, reason
	return r.result, r.err
}

// TestTenantOf pins the tenant resolution every handler runs: an explicit
// context tenant wins, and both an absent and a blank tenant fall back to the
// platform default.
func TestTenantOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  context.Context
		want domain.Tenant
	}{
		{"no tenant falls back to the default", context.Background(), domain.Tenant(tenant.Default)},
		{"an explicit tenant wins", tenant.WithID(context.Background(), "acme"), "acme"},
		{"a blank tenant falls back to the default", tenant.WithID(context.Background(), ""), domain.Tenant(tenant.Default)},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tenantOf(tt.ctx); got != tt.want {
				t.Errorf("tenantOf = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCheckoutMapsTheContractOntoTheDomainAndBack pins the checkout round trip
// at the wire boundary: request fields reach the domain, totals and change are
// computed server-side, every amount crosses in minor units, and the line
// items are mapped back with quantity, discount and subtotal intact.
func TestCheckoutMapsTheContractOntoTheDomainAndBack(t *testing.T) {
	t.Parallel()

	svc, store, stock := newWiredService()
	ctx := tenant.WithID(t.Context(), "acme")

	resp, err := svc.Checkout(ctx, &salesv1.CheckoutRequest{
		Items: []*salesv1.CheckoutItem{
			{ProductId: "p1", Name: "Kopi", Quantity: 2, Price: 10000},
			{ProductId: "p2", Name: "Teh", Quantity: 1, Price: 5000, Discount: 1000},
		},
		Discount:      1000,
		Paid:          24000,
		PaymentMethod: "Kartu",
		Cashier:       "Kasir A",
		EmployeeId:    "emp-1",
	})
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	got := resp.GetTransaction()
	if got.GetId() == "" {
		t.Error("Checkout returned a transaction with no id")
	}
	if !strings.HasPrefix(got.GetNumber(), "TRX-") {
		t.Errorf("Number = %q, want a TRX- prefix", got.GetNumber())
	}
	if got.GetTotal() != 23000 || got.GetDiscount() != 1000 || got.GetPaid() != 24000 || got.GetChange() != 1000 {
		t.Errorf("total/discount/paid/change = %d/%d/%d/%d, want 23000/1000/24000/1000",
			got.GetTotal(), got.GetDiscount(), got.GetPaid(), got.GetChange())
	}
	if got.GetPaymentMethod() != "Kartu" || got.GetCashier() != "Kasir A" || got.GetEmployeeId() != "emp-1" {
		t.Errorf("method/cashier/employee = %q/%q/%q, want Kartu/Kasir A/emp-1",
			got.GetPaymentMethod(), got.GetCashier(), got.GetEmployeeId())
	}
	if got.GetCreatedAt() == nil {
		t.Fatal("CreatedAt is nil on a stored checkout")
	}
	if got.GetVoidedAt() != nil {
		t.Error("a fresh checkout must not be voided")
	}
	if len(got.GetItems()) != 2 {
		t.Fatalf("items = %d, want 2", len(got.GetItems()))
	}
	first := got.GetItems()[0]
	if first.GetProductId() != "p1" || first.GetQuantity() != 2 || first.GetPrice() != 10000 || first.GetSubtotal() != 20000 {
		t.Errorf("items[0] = %+v, want (p1, 2, 10000, 20000)", first)
	}
	second := got.GetItems()[1]
	if second.GetProductId() != "p2" || second.GetQuantity() != 1 || second.GetDiscount() != 1000 || second.GetSubtotal() != 4000 {
		t.Errorf("items[1] = %+v, want (p2, 1, 1000, 4000)", second)
	}

	stored, err := store.Get(t.Context(), "acme", got.GetId())
	if err != nil {
		t.Fatalf("the checkout was not persisted for the context tenant: %v", err)
	}
	if stored.Number != got.GetNumber() {
		t.Errorf("stored Number = %q, response Number = %q", stored.Number, got.GetNumber())
	}
	if !got.GetCreatedAt().AsTime().Equal(stored.CreatedAt) {
		t.Errorf("CreatedAt = %v, want the stored %v", got.GetCreatedAt().AsTime(), stored.CreatedAt)
	}
	if stock.calls != 2 {
		t.Errorf("stock adjustments = %d, want 2 (one per line)", stock.calls)
	}
}

// TestGetAndListMapAStoredTransaction pins the read-side proto mapping: every
// persisted field, the void stamp, and each line's minor-unit amounts survive
// the conversion, and the handler scopes the read to the context tenant.
func TestGetAndListMapAStoredTransaction(t *testing.T) {
	t.Parallel()

	store := infrastructure.NewStore()
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	voidedAt := createdAt.Add(time.Hour)
	sale := &domain.Transaction{
		TenantID: "acme", ID: "tx-1", Number: "TRX-1",
		Total: 23000, Discount: 1000, Paid: 24000, Change: 1000,
		PaymentMethod: "Kartu", Cashier: "Kasir A", EmployeeID: "emp-1",
		CreatedAt: createdAt, VoidedAt: &voidedAt, VoidedBy: "Supervisor", VoidReason: "Barang rusak",
		Items: []domain.Item{
			{ID: "tx-1:1", ProductID: "p1", Name: "Kopi", Quantity: 2, Price: 10000, Subtotal: 20000},
			{ID: "tx-1:2", ProductID: "p2", Name: "Teh", Quantity: 1, Price: 5000, Discount: 1000, Subtotal: 4000},
		},
	}
	if err := store.Create(context.Background(), "acme", sale); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	svc := &service{svc: application.NewService(store, &stubStock{}), logger: discardLogger()}
	ctx := tenant.WithID(t.Context(), "acme")

	resp, err := svc.GetTransaction(ctx, &salesv1.GetTransactionRequest{Id: "tx-1"})
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	got := resp.GetTransaction()
	if got.GetId() != "tx-1" || got.GetNumber() != "TRX-1" {
		t.Errorf("id/number = %q/%q, want tx-1/TRX-1", got.GetId(), got.GetNumber())
	}
	if got.GetTotal() != 23000 || got.GetDiscount() != 1000 || got.GetPaid() != 24000 || got.GetChange() != 1000 {
		t.Errorf("total/discount/paid/change = %d/%d/%d/%d, want 23000/1000/24000/1000",
			got.GetTotal(), got.GetDiscount(), got.GetPaid(), got.GetChange())
	}
	if got.GetPaymentMethod() != "Kartu" || got.GetCashier() != "Kasir A" || got.GetEmployeeId() != "emp-1" {
		t.Errorf("method/cashier/employee = %q/%q/%q, want Kartu/Kasir A/emp-1",
			got.GetPaymentMethod(), got.GetCashier(), got.GetEmployeeId())
	}
	if got.GetCreatedAt() == nil || !got.GetCreatedAt().AsTime().Equal(createdAt) {
		t.Errorf("CreatedAt = %v, want %v", got.GetCreatedAt(), createdAt)
	}
	if got.GetVoidedAt() == nil || !got.GetVoidedAt().AsTime().Equal(voidedAt) {
		t.Errorf("VoidedAt = %v, want %v", got.GetVoidedAt(), voidedAt)
	}
	if got.GetVoidedBy() != "Supervisor" || got.GetVoidReason() != "Barang rusak" {
		t.Errorf("voided by/reason = %q/%q, want Supervisor/Barang rusak", got.GetVoidedBy(), got.GetVoidReason())
	}
	if len(got.GetItems()) != 2 {
		t.Fatalf("items = %d, want 2", len(got.GetItems()))
	}
	if item := got.GetItems()[1]; item.GetId() != "tx-1:2" || item.GetProductId() != "p2" ||
		item.GetQuantity() != 1 || item.GetPrice() != 5000 || item.GetDiscount() != 1000 || item.GetSubtotal() != 4000 {
		t.Errorf("items[1] = %+v, want (tx-1:2, p2, 1, 5000, 1000, 4000)", item)
	}

	list, err := svc.ListTransactions(ctx, &salesv1.ListTransactionsRequest{PageSize: 10})
	if err != nil {
		t.Fatalf("ListTransactions: %v", err)
	}
	if len(list.GetTransactions()) != 1 || list.GetTransactions()[0].GetId() != "tx-1" {
		t.Errorf("ListTransactions = %+v, want the one stored transaction", list.GetTransactions())
	}

	// Another tenant must not see the sale.
	other := tenant.WithID(t.Context(), "other")
	if _, err := svc.GetTransaction(other, &salesv1.GetTransactionRequest{Id: "tx-1"}); status.Code(err) != codes.NotFound {
		t.Errorf("cross-tenant GetTransaction = %v, want NotFound", err)
	}
}

// TestHandlersPassTheContextTenantAndRequestFieldsToTheService pins the
// service boundary of every handler: each one resolves the tenant from its
// context and forwards the request's own fields unchanged.
func TestHandlersPassTheContextTenantAndRequestFieldsToTheService(t *testing.T) {
	t.Parallel()

	t.Run("an explicit tenant", func(t *testing.T) {
		t.Parallel()

		rec := &recordingSales{result: &domain.Transaction{ID: "tx-1"}}
		svc := &service{svc: rec, logger: discardLogger()}
		ctx := tenant.WithID(context.Background(), "video")

		if _, err := svc.Checkout(ctx, &salesv1.CheckoutRequest{
			Items:         []*salesv1.CheckoutItem{{ProductId: "p1", Name: "Kopi", Quantity: 2, Price: 100, Discount: 10}},
			Discount:      5,
			Paid:          200,
			PaymentMethod: "Kartu",
			Cashier:       "Kasir A",
			EmployeeId:    "emp-1",
		}); err != nil {
			t.Fatalf("Checkout: %v", err)
		}
		if rec.tenant != "video" {
			t.Errorf("Checkout tenant = %q, want video", rec.tenant)
		}
		if len(rec.items) != 1 || rec.items[0] != (domain.CheckoutItem{ProductID: "p1", Name: "Kopi", Quantity: 2, Price: 100, Discount: 10}) {
			t.Errorf("Checkout items = %+v, want the request line", rec.items)
		}
		if rec.discount != 5 || rec.paid != 200 || rec.method != "Kartu" || rec.cashier != "Kasir A" || rec.employeeID != "emp-1" {
			t.Errorf("Checkout fields = %d/%d/%q/%q/%q, want 5/200/Kartu/Kasir A/emp-1",
				rec.discount, rec.paid, rec.method, rec.cashier, rec.employeeID)
		}

		if _, err := svc.GetTransaction(ctx, &salesv1.GetTransactionRequest{Id: "tx-9"}); err != nil {
			t.Fatalf("GetTransaction: %v", err)
		}
		if rec.tenant != "video" || rec.id != "tx-9" {
			t.Errorf("GetTransaction saw tenant/id = %q/%q, want video/tx-9", rec.tenant, rec.id)
		}

		if _, err := svc.ListTransactions(ctx, &salesv1.ListTransactionsRequest{PageSize: 7}); err != nil {
			t.Fatalf("ListTransactions: %v", err)
		}
		if rec.tenant != "video" || rec.limit != 7 {
			t.Errorf("ListTransactions saw tenant/limit = %q/%d, want video/7", rec.tenant, rec.limit)
		}

		if _, err := svc.VoidTransaction(ctx, &salesv1.VoidTransactionRequest{Id: "tx-9", VoidedBy: "Supervisor", Reason: "Barang rusak"}); err != nil {
			t.Fatalf("VoidTransaction: %v", err)
		}
		if rec.tenant != "video" || rec.id != "tx-9" || rec.by != "Supervisor" || rec.reason != "Barang rusak" {
			t.Errorf("VoidTransaction saw %q/%q/%q/%q, want video/tx-9/Supervisor/Barang rusak",
				rec.tenant, rec.id, rec.by, rec.reason)
		}
	})

	t.Run("no tenant resolves to the default", func(t *testing.T) {
		t.Parallel()

		rec := &recordingSales{}
		svc := &service{svc: rec, logger: discardLogger()}

		if _, err := svc.GetTransaction(context.Background(), &salesv1.GetTransactionRequest{Id: "tx-1"}); err != nil {
			t.Fatalf("GetTransaction: %v", err)
		}
		if rec.tenant != domain.Tenant(tenant.Default) {
			t.Errorf("tenant = %q, want %q", rec.tenant, tenant.Default)
		}
	})
}

// TestErrorsClassifyIntoStatus pins toStatus's mapping on every handler: the
// sales domain's error kinds become the documented gRPC codes with the stable
// client-facing messages, and an unknown failure is an Internal whose detail
// stays out of the message.
func TestErrorsClassifyIntoStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string
	}{
		{"not found", domain.ErrTransactionNotFound, codes.NotFound, "transaction not found"},
		{"already voided", domain.ErrAlreadyVoided, codes.FailedPrecondition, "transaction already voided"},
		{"insufficient stock", domain.ErrInsufficientStock, codes.FailedPrecondition, "insufficient stock for a sale line"},
		{"insufficient payment", domain.ErrInsufficientPayment, codes.FailedPrecondition, "paid is less than the total"},
		{"unknown product", domain.ErrProductNotFound, codes.InvalidArgument, "product not found for a sale line"},
		{"invalid transaction", domain.ErrNoItems, codes.InvalidArgument, "invalid transaction"},
		{"unknown failure is internal", errors.New("boom"), codes.Internal, "something went wrong"},
	}

	handlers := map[string]func(*service) error{
		"Checkout": func(s *service) error {
			_, err := s.Checkout(context.Background(), &salesv1.CheckoutRequest{})
			return err
		},
		"GetTransaction": func(s *service) error {
			_, err := s.GetTransaction(context.Background(), &salesv1.GetTransactionRequest{Id: "tx-1"})
			return err
		},
		"ListTransactions": func(s *service) error {
			_, err := s.ListTransactions(context.Background(), &salesv1.ListTransactionsRequest{})
			return err
		},
		"VoidTransaction": func(s *service) error {
			_, err := s.VoidTransaction(context.Background(), &salesv1.VoidTransactionRequest{Id: "tx-1"})
			return err
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for name, call := range handlers {
				svc := &service{svc: &recordingSales{err: tt.err}, logger: discardLogger()}
				err := call(svc)
				if got := status.Code(err); got != tt.wantCode {
					t.Errorf("%s status = %v, want %v", name, got, tt.wantCode)
				}
				if got := status.Convert(err).Message(); got != tt.wantMsg {
					t.Errorf("%s message = %q, want %q", name, got, tt.wantMsg)
				}
			}
		})
	}
}

// TestToProtoNilTransactionIsNil pins the nil guard: a service that returned no
// transaction must not produce a zero-valued proto that a client would read as
// a real sale.
func TestToProtoNilTransactionIsNil(t *testing.T) {
	t.Parallel()

	if got := toProto(nil); got != nil {
		t.Errorf("toProto(nil) = %+v, want nil", got)
	}
}

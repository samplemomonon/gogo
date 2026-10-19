package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gogo/internal/features/sales/domain"
	"gogo/internal/features/sales/infrastructure"
)

type stockCall struct {
	productID string
	delta     int
	kind      string
	reference string
}

type fakeStock struct {
	calls    []stockCall
	failAt   int
	failWith error
}

func (f *fakeStock) Adjust(_ context.Context, _ domain.Tenant, productID string, delta int, kind, reference string) error {
	f.calls = append(f.calls, stockCall{productID: productID, delta: delta, kind: kind, reference: reference})
	if f.failAt >= 0 && len(f.calls)-1 == f.failAt {
		return f.failWith
	}
	return nil
}

type unwritableStore struct {
	*infrastructure.Store
}

func (u *unwritableStore) Create(context.Context, domain.Tenant, *domain.Transaction) error {
	return errors.New("store unavailable")
}

func twoLines() []domain.CheckoutItem {
	return []domain.CheckoutItem{
		{ProductID: "p1", Name: "Kopi", Quantity: 2, Price: 10000},
		{ProductID: "p2", Name: "Teh", Quantity: 1, Price: 5000},
	}
}

func TestCheckout(t *testing.T) {
	t.Parallel()

	t.Run("reserves stock for every line and records the sale", func(t *testing.T) {
		t.Parallel()

		store, stock := infrastructure.NewStore(), &fakeStock{failAt: -1}
		service := NewService(store, stock)

		transaction, err := service.Checkout(t.Context(), "acme", twoLines(), 0, 30000, "Tunai", "Kasir A", "")
		if err != nil {
			t.Fatalf("Checkout returned error: %v", err)
		}

		if want := int64(25000); transaction.Total != want {
			t.Errorf("Total = %d, want %d", transaction.Total, want)
		}
		if len(stock.calls) != 2 {
			t.Fatalf("stock calls = %d, want 2", len(stock.calls))
		}
		if got, want := stock.calls[0], (stockCall{"p1", -2, "sale", transaction.Number + ":sale:p1"}); got != want {
			t.Errorf("stock.calls[0] = %+v, want %+v", got, want)
		}
		if got, want := stock.calls[1], (stockCall{"p2", -1, "sale", transaction.Number + ":sale:p2"}); got != want {
			t.Errorf("stock.calls[1] = %+v, want %+v", got, want)
		}

		saved, err := store.Get(t.Context(), "acme", transaction.ID)
		if err != nil {
			t.Fatalf("store.Get returned error: %v", err)
		}
		if saved.Number != transaction.Number {
			t.Errorf("saved Number = %q, want %q", saved.Number, transaction.Number)
		}
	})

	t.Run("rejects an underpayment before reserving any stock", func(t *testing.T) {
		t.Parallel()

		stock := &fakeStock{failAt: -1}
		service := NewService(infrastructure.NewStore(), stock)

		_, err := service.Checkout(t.Context(), "acme", twoLines(), 0, 100, "Tunai", "", "")
		if !errors.Is(err, domain.ErrInsufficientPayment) {
			t.Errorf("Checkout error = %v, want %v", err, domain.ErrInsufficientPayment)
		}
		if len(stock.calls) != 0 {
			t.Errorf("stock calls = %d, want 0", len(stock.calls))
		}
	})

	t.Run("compensates only the lines already reserved when a later line is refused", func(t *testing.T) {
		t.Parallel()

		stock := &fakeStock{failAt: 1, failWith: domain.ErrInsufficientStock}
		service := NewService(infrastructure.NewStore(), stock)

		_, err := service.Checkout(t.Context(), "acme", twoLines(), 0, 30000, "Tunai", "", "")
		if !errors.Is(err, domain.ErrInsufficientStock) {
			t.Errorf("Checkout error = %v, want %v", err, domain.ErrInsufficientStock)
		}
		if domain.Classify(err) != domain.KindFailedPrecondition {
			t.Errorf("Classify = %v, want %v", domain.Classify(err), domain.KindFailedPrecondition)
		}

		// The refused line was never reserved, so only the first line is given back.
		if len(stock.calls) != 3 {
			t.Fatalf("stock calls = %d, want 3: %+v", len(stock.calls), stock.calls)
		}
		giveBack := stock.calls[2]
		if giveBack.productID != "p1" || giveBack.delta != 2 || giveBack.kind != "void" {
			t.Errorf("stock.calls[2] = %+v, want a +2 void for p1", giveBack)
		}
		if !strings.HasSuffix(giveBack.reference, ":comp:p1") {
			t.Errorf("stock.calls[2].reference = %q, want the comp reference for p1", giveBack.reference)
		}
	})

	t.Run("compensates every line when the sale cannot be recorded", func(t *testing.T) {
		t.Parallel()

		stock := &fakeStock{failAt: -1}
		service := NewService(&unwritableStore{Store: infrastructure.NewStore()}, stock)

		_, err := service.Checkout(t.Context(), "acme", twoLines(), 0, 30000, "Tunai", "", "")
		if err == nil {
			t.Fatal("Checkout returned nil error, want a store failure")
		}

		if len(stock.calls) != 4 {
			t.Fatalf("stock calls = %d, want 4: %+v", len(stock.calls), stock.calls)
		}
		var net int
		for _, call := range stock.calls {
			net += call.delta
		}
		if net != 0 {
			t.Errorf("net stock delta = %d, want 0", net)
		}
	})
}

func TestVoid(t *testing.T) {
	t.Parallel()

	t.Run("restores stock and refuses a second void", func(t *testing.T) {
		t.Parallel()

		store, stock := infrastructure.NewStore(), &fakeStock{failAt: -1}
		service := NewService(store, stock)

		transaction, err := service.Checkout(t.Context(), "acme", []domain.CheckoutItem{
			{ProductID: "p1", Quantity: 3, Price: 1000},
		}, 0, 3000, "Tunai", "", "")
		if err != nil {
			t.Fatalf("Checkout returned error: %v", err)
		}

		voided, err := service.Void(t.Context(), "acme", transaction.ID, "Supervisor", "Barang rusak")
		if err != nil {
			t.Fatalf("Void returned error: %v", err)
		}
		if voided.VoidedAt == nil {
			t.Fatal("VoidedAt is nil after Void")
		}
		if len(stock.calls) != 2 {
			t.Fatalf("stock calls = %d, want 2", len(stock.calls))
		}
		if got, want := stock.calls[1], (stockCall{"p1", 3, "void", transaction.Number + ":void:p1"}); got != want {
			t.Errorf("stock.calls[1] = %+v, want %+v", got, want)
		}

		if _, err := service.Void(t.Context(), "acme", transaction.ID, "Supervisor", "lagi"); !errors.Is(err, domain.ErrAlreadyVoided) {
			t.Errorf("second Void error = %v, want %v", err, domain.ErrAlreadyVoided)
		}
		if len(stock.calls) != 2 {
			t.Errorf("stock calls after second void = %d, want 2 (no double restore)", len(stock.calls))
		}

		saved, err := store.Get(t.Context(), "acme", transaction.ID)
		if err != nil {
			t.Fatalf("store.Get returned error: %v", err)
		}
		if saved.VoidedAt == nil {
			t.Error("the void was not persisted")
		}
	})

	t.Run("refuses to void another tenant's sale", func(t *testing.T) {
		t.Parallel()

		stock := &fakeStock{failAt: -1}
		service := NewService(infrastructure.NewStore(), stock)

		transaction, err := service.Checkout(t.Context(), "acme", []domain.CheckoutItem{
			{ProductID: "p1", Quantity: 1, Price: 1000},
		}, 0, 1000, "Tunai", "", "")
		if err != nil {
			t.Fatalf("Checkout returned error: %v", err)
		}

		if _, err := service.Void(t.Context(), "other", transaction.ID, "Supervisor", ""); !errors.Is(err, domain.ErrTransactionNotFound) {
			t.Errorf("Void error = %v, want %v", err, domain.ErrTransactionNotFound)
		}
		if len(stock.calls) != 1 {
			t.Errorf("stock calls = %d, want 1 (no restore for a refused void)", len(stock.calls))
		}
	})
}

func TestList(t *testing.T) {
	t.Parallel()

	store, stock := infrastructure.NewStore(), &fakeStock{failAt: -1}
	service := NewService(store, stock)

	if _, err := service.Checkout(t.Context(), "acme", []domain.CheckoutItem{
		{ProductID: "p1", Quantity: 1, Price: 1000},
	}, 0, 1000, "Tunai", "", ""); err != nil {
		t.Fatalf("Checkout returned error: %v", err)
	}
	if _, err := service.Checkout(t.Context(), "other", []domain.CheckoutItem{
		{ProductID: "p2", Quantity: 1, Price: 1000},
	}, 0, 1000, "Tunai", "", ""); err != nil {
		t.Fatalf("Checkout returned error: %v", err)
	}

	found, err := service.List(t.Context(), "acme", 0)
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("List returned %d transactions, want 1 (tenant scoped)", len(found))
	}
	if found[0].TenantID != "acme" {
		t.Errorf("List returned tenant %q, want acme", found[0].TenantID)
	}
}

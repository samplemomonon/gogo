package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestNewTransaction(t *testing.T) {
	t.Parallel()

	t.Run("computes line subtotals, total, and change server-side", func(t *testing.T) {
		t.Parallel()

		transaction, err := NewTransaction("acme", "tx-1", "TRX-1", []CheckoutItem{
			{ProductID: "p1", Name: "Kopi", Quantity: 2, Price: 10000},
			{ProductID: "p2", Name: "Teh", Quantity: 1, Price: 5000, Discount: 1000},
		}, 0, 30000, "Tunai", "Kasir A", "")
		if err != nil {
			t.Fatalf("NewTransaction returned error: %v", err)
		}

		if want := int64(24000); transaction.Total != want {
			t.Errorf("Total = %d, want %d", transaction.Total, want)
		}
		if want := int64(20000); transaction.Items[0].Subtotal != want {
			t.Errorf("Items[0].Subtotal = %d, want %d", transaction.Items[0].Subtotal, want)
		}
		if want := int64(4000); transaction.Items[1].Subtotal != want {
			t.Errorf("Items[1].Subtotal = %d, want %d", transaction.Items[1].Subtotal, want)
		}
		if want := int64(6000); transaction.Change != want {
			t.Errorf("Change = %d, want %d", transaction.Change, want)
		}
	})

	t.Run("applies the transaction discount after line discounts", func(t *testing.T) {
		t.Parallel()

		transaction, err := NewTransaction("acme", "tx-1", "TRX-1", []CheckoutItem{
			{ProductID: "p1", Quantity: 1, Price: 10000},
		}, 1500, 8500, "", "", "")
		if err != nil {
			t.Fatalf("NewTransaction returned error: %v", err)
		}
		if want := int64(8500); transaction.Total != want {
			t.Errorf("Total = %d, want %d", transaction.Total, want)
		}
		if want := "Tunai"; transaction.PaymentMethod != want {
			t.Errorf("PaymentMethod = %q, want %q", transaction.PaymentMethod, want)
		}
	})

	t.Run("rejects an invalid checkout", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name     string
			tenant   Tenant
			id       string
			items    []CheckoutItem
			discount int64
			paid     int64
			wantErr  error
		}{
			{"blank tenant", "", "tx-1", []CheckoutItem{{ProductID: "p1", Quantity: 1, Price: 100}}, 0, 100, ErrInvalidTenant},
			{"blank id", "acme", " ", []CheckoutItem{{ProductID: "p1", Quantity: 1, Price: 100}}, 0, 100, ErrInvalidID},
			{"no items", "acme", "tx-1", nil, 0, 0, ErrNoItems},
			{"zero quantity", "acme", "tx-1", []CheckoutItem{{ProductID: "p1", Quantity: 0, Price: 100}}, 0, 0, ErrInvalidItem},
			{"negative price", "acme", "tx-1", []CheckoutItem{{ProductID: "p1", Quantity: 1, Price: -1}}, 0, 0, ErrInvalidItem},
			{"line discount above subtotal", "acme", "tx-1", []CheckoutItem{{ProductID: "p1", Quantity: 1, Price: 100, Discount: 200}}, 0, 0, ErrInvalidItem},
			{"negative transaction discount", "acme", "tx-1", []CheckoutItem{{ProductID: "p1", Quantity: 1, Price: 100}}, -1, 0, ErrInvalidItem},
			{"paid below total", "acme", "tx-1", []CheckoutItem{{ProductID: "p1", Quantity: 2, Price: 10000}}, 0, 100, ErrInsufficientPayment},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				_, err := NewTransaction(tt.tenant, tt.id, "TRX-1", tt.items, tt.discount, tt.paid, "Tunai", "", "")
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("NewTransaction error = %v, want %v", err, tt.wantErr)
				}
			})
		}
	})
}

func TestVoid(t *testing.T) {
	t.Parallel()

	transaction, err := NewTransaction("acme", "tx-1", "TRX-1", []CheckoutItem{
		{ProductID: "p1", Quantity: 1, Price: 10000},
	}, 0, 10000, "Tunai", "", "")
	if err != nil {
		t.Fatalf("NewTransaction returned error: %v", err)
	}

	if err := transaction.Void("Supervisor", "Barang rusak"); err != nil {
		t.Fatalf("Void returned error: %v", err)
	}
	if transaction.VoidedAt == nil {
		t.Fatal("VoidedAt is nil after Void")
	}
	if want := "Supervisor"; transaction.VoidedBy != want {
		t.Errorf("VoidedBy = %q, want %q", transaction.VoidedBy, want)
	}
	if want := "Barang rusak"; transaction.VoidReason != want {
		t.Errorf("VoidReason = %q, want %q", transaction.VoidReason, want)
	}

	if err := transaction.Void("Supervisor", "lagi"); !errors.Is(err, ErrAlreadyVoided) {
		t.Errorf("second Void error = %v, want %v", err, ErrAlreadyVoided)
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want Kind
	}{
		{"not found", ErrTransactionNotFound, KindNotFound},
		{"already voided", ErrAlreadyVoided, KindFailedPrecondition},
		{"insufficient stock", ErrInsufficientStock, KindFailedPrecondition},
		{"insufficient payment", ErrInsufficientPayment, KindFailedPrecondition},
		{"unknown product", ErrProductNotFound, KindInvalid},
		{"umbrella invalid", ErrInvalidTransaction, KindInvalid},
		{"wrapped not found", fmt.Errorf("repo: %w", ErrTransactionNotFound), KindNotFound},
		{"unknown error is internal", errors.New("boom"), KindInternal},
		{"nil is internal", nil, KindInternal},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"not found", ErrTransactionNotFound, "transaction not found"},
		{"already voided", ErrAlreadyVoided, "transaction already voided"},
		{"insufficient stock", ErrInsufficientStock, "insufficient stock for a sale line"},
		{"unknown product", ErrProductNotFound, "product not found for a sale line"},
		{"insufficient payment", ErrInsufficientPayment, "paid is less than the total"},
		{"invalid", ErrInvalidTransaction, "invalid transaction"},
		{"unknown", errors.New("boom"), "something went wrong"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := Message(tt.err); got != tt.want {
				t.Errorf("Message(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

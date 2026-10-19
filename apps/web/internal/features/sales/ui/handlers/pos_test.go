package handlers_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gogo/internal/features/sales/ui/application/results"
	"gogo/internal/features/sales/ui/infrastructure/salesclient"
)

// TestKasirLooksUpEveryQuantityInOneBatchedCall pins the till's data gathering:
// the page filters to sellable products for display, but the stock lookup is one
// batched call carrying every catalogue id, including non-sellable ones. A
// per-product lookup would make the till's latency scale with the catalogue; a
// lookup restricted to actives would make the filter observable in the wrong
// place.
func TestKasirLooksUpEveryQuantityInOneBatchedCall(t *testing.T) {
	t.Parallel()

	router, catalog, inventory, _ := newConsole(t)

	rec := do(t, router, getReq("/seller/kasir"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /seller/kasir = %d, want 200", rec.Code)
	}

	if catalog.listCalls != 1 {
		t.Errorf("catalog.List calls = %d, want 1", catalog.listCalls)
	}
	if inventory.listCalls != 1 {
		t.Errorf("ListStocks calls = %d, want 1 batched lookup", inventory.listCalls)
	}
	want := []string{"1", "2", "3"}
	if strings.Join(inventory.lastStockIDs, ",") != strings.Join(want, ",") {
		t.Errorf("ListStocks ids = %v, want every catalogue id %v", inventory.lastStockIDs, want)
	}
}

// TestCheckoutForwardsTheBasketAtCataloguePrices pins what a sale is made of:
// quantities come from the qty_<id> form fields, prices come from the catalogue,
// non-sellable and unreadable quantities are dropped, and the checkout is one call
// to the sales module with the payment fields exactly as posted. The inventory
// module is not called: the sale's own stock effect belongs to the sales service.
func TestCheckoutForwardsTheBasketAtCataloguePrices(t *testing.T) {
	t.Parallel()

	router, _, inventory, sales := newConsole(t)

	rec := do(t, router, postReq("/seller/kasir",
		"qty_1=2&qty_2=5&qty_3=abc&paid=50000&method=Tunai&cashier=Budi"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("checkout status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/seller/transaksi" {
		t.Errorf("Location = %q, want /seller/transaksi", got)
	}

	if sales.checkoutCalls != 1 {
		t.Fatalf("Checkout calls = %d, want 1", sales.checkoutCalls)
	}
	got := sales.lastCheckout
	if len(got.items) != 1 {
		t.Fatalf("basket = %+v, want one line (draft and malformed quantities dropped)", got.items)
	}
	want := results.TransactionItem{ProductID: "1", Name: "Kopi Susu", Quantity: 2, Price: 25000}
	item := got.items[0]
	if *item != want {
		t.Errorf("basket line = %+v, want %+v", *item, want)
	}
	if got.discount != 0 || got.paid != 50000 || got.method != "Tunai" || got.cashier != "Budi" || got.employeeID != "employee-1" {
		t.Errorf("payment fields = %+v, want discount 0, paid 50000, Tunai/Budi and validated employee id", got)
	}

	if inventory.adjustCalls != 0 {
		t.Errorf("inventory was adjusted %d times during checkout; the sales service owns the stock effect",
			inventory.adjustCalls)
	}
}

// TestCheckoutWithoutASelectionReRendersTheTill pins the no-op answer: an empty
// basket does not reach the sales module, and the cashier is asked to pick
// something rather than being redirected to an unchanged ledger.
func TestCheckoutWithoutASelectionReRendersTheTill(t *testing.T) {
	t.Parallel()

	router, _, _, sales := newConsole(t)

	rec := do(t, router, postReq("/seller/kasir", "paid=50000&method=Tunai"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the till re-renders)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Pilih minimal satu produk") {
		t.Error("an empty checkout did not ask for a selection")
	}
	if sales.checkoutCalls != 0 {
		t.Errorf("Checkout calls = %d, want 0", sales.checkoutCalls)
	}
}

// TestCheckoutIgnoresQuantitiesForNonSellableProducts pins that a qty posted for a
// draft product is not trusted: the field does not exist for it, so a posted one is
// stale or tampered with, and a basket made only of those produces no sale.
func TestCheckoutIgnoresQuantitiesForNonSellableProducts(t *testing.T) {
	t.Parallel()

	router, _, _, sales := newConsole(t)

	rec := do(t, router, postReq("/seller/kasir", "qty_2=1&paid=15000&method=Tunai"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Pilih minimal satu produk") {
		t.Error("a draft-only basket did not read as empty")
	}
	if sales.checkoutCalls != 0 {
		t.Errorf("Checkout calls = %d, want 0", sales.checkoutCalls)
	}
}

// TestCheckoutRefusesAnOversellByName pins the till's own guard. The sales module
// would also refuse the negative stock, but failing here keeps the message
// specific: the cashier is told which product cannot cover the quantity.
func TestCheckoutRefusesAnOversellByName(t *testing.T) {
	t.Parallel()

	router, _, _, sales := newConsole(t)

	// Product 1 has 4 recorded; ask for 5.
	rec := do(t, router, postReq("/seller/kasir", "qty_1=5&paid=125000&method=Tunai"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Stok Kopi Susu tidak cukup") {
		t.Error("an oversell attempt did not name the product that could not cover it")
	}
	if sales.checkoutCalls != 0 {
		t.Errorf("Checkout calls = %d, want 0: the basket never reached sales", sales.checkoutCalls)
	}
}

// TestCheckoutTreatsAnUnreadablePaidAsZero pins the deliberate leniency: a payment
// box the cashier left as junk is read as zero, which the sales service then
// refuses as an underpayment with its own message, rather than the console
// rejecting the basket before the service can explain what is wrong.
func TestCheckoutTreatsAnUnreadablePaidAsZero(t *testing.T) {
	t.Parallel()

	router, _, _, sales := newConsole(t)

	rec := do(t, router, postReq("/seller/kasir", "qty_1=1&paid=not-a-number&method=Tunai"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if sales.lastCheckout.paid != 0 {
		t.Errorf("paid = %d, want 0 for an unreadable value", sales.lastCheckout.paid)
	}
}

// TestCheckoutFailureReRendersTheTillAt200 pins the checkout failure posture: the
// sales service's refusal is shown on the till with the mapped message, at 200 so
// the quantities the cashier entered stay on screen, and never with the upstream's
// own error text.
func TestCheckoutFailureReRendersTheTillAt200(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		err     error
		message string
	}{
		{"a refused payment", salesclient.ErrSaleRefused, "Penjualan ditolak"},
		{"an invalid basket", salesclient.ErrInvalidCheckout, "Keranjang tidak valid"},
		{"an unreachable sales service", salesclient.ErrSalesUnavailable, "Layanan sedang tidak tersedia, coba lagi sebentar lagi"},
		{"an unrecognised console bug", errors.New("nil map write"), "Terjadi kesalahan pada konsol"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			router, _, _, sales := newConsole(t)
			sales.checkoutErr = fmt.Errorf("%w: internal detail nobody should see", tc.err)

			rec := do(t, router, postReq("/seller/kasir", "qty_1=1&paid=25000&method=Tunai"))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (the till re-renders)", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.message) {
				t.Errorf("body does not show %q", tc.message)
			}
			if strings.Contains(body, "internal detail nobody should see") {
				t.Error("the re-rendered till leaked the upstream's error text")
			}
		})
	}
}

// TestTransactionVoidAttributesTheSessionEmployee pins that a void is
// attributable: the id comes from the path, and the actor comes from the
// validated session — never from the form, which a browser can forge. The form's
// reason still travels to the sales module so the voided sale can be audited.
func TestTransactionVoidAttributesTheSessionEmployee(t *testing.T) {
	t.Parallel()

	router, _, _, sales := newConsole(t)

	// voidedBy is planted in the form to prove the handler ignores it in favour
	// of the session's employee.
	rec := do(t, router, postReq("/seller/transaksi/77/void", "voidedBy=forged&reason=salah+input"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("void status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/seller/transaksi" {
		t.Errorf("Location = %q, want /seller/transaksi", got)
	}
	got := sales.lastVoid
	if got.id != "77" || got.by != "employee-1" || got.reason != "salah input" {
		t.Errorf("void call = %+v, want 77/employee-1/salah input", got)
	}
}

// TestVoidFailureReRendersTheLedgerAt200 pins that a failed void does not lose the
// ledger: the sales list is re-fetched and rendered with the mapped message.
func TestVoidFailureReRendersTheLedgerAt200(t *testing.T) {
	t.Parallel()

	router, _, _, sales := newConsole(t)
	sales.voidErr = salesclient.ErrTransactionNotFound

	rec := do(t, router, postReq("/seller/transaksi/77/void", "voidedBy=Siti&reason=salah"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Transaksi tidak ditemukan") {
		t.Error("a failed void did not show the mapped reason")
	}
	if !strings.Contains(body, "TRX-1") {
		t.Error("a failed void did not re-render the ledger")
	}
	if sales.listCalls != 1 {
		t.Errorf("sales.List calls = %d, want 1", sales.listCalls)
	}
}

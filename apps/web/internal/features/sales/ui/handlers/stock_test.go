package handlers_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gogo/internal/features/sales/ui/infrastructure/inventoryclient"
)

// TestStockPageAsksForTheWholeLedgerAndTheRecentMovements pins the stock page's
// two questions: every recorded quantity (empty ids means "all rows" to the
// inventory contract, not "no rows") and the most recent movements with no product
// filter. Fifty is the page's window.
func TestStockPageAsksForTheWholeLedgerAndTheRecentMovements(t *testing.T) {
	t.Parallel()

	router, catalog, inventory, _ := newConsole(t)

	rec := do(t, router, getReq("/seller/stok"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /seller/stok = %d, want 200", rec.Code)
	}

	if catalog.listCalls != 1 {
		t.Errorf("catalog.List calls = %d, want 1", catalog.listCalls)
	}
	if inventory.listCalls != 1 {
		t.Errorf("ListStocks calls = %d, want 1", inventory.listCalls)
	}
	if inventory.lastStockIDs != nil {
		t.Errorf("ListStocks ids = %v, want nil (the whole ledger)", inventory.lastStockIDs)
	}
	got := inventory.lastMovements
	if got.productID != "" || got.limit != 50 {
		t.Errorf("ListMovements = %+v, want no product filter and limit 50", got)
	}
}

// TestStockAdjustForwardsEveryFieldAndRedirects pins the adjustment contract:
// product id, delta, kind and note travel from the form while the actor comes from
// the validated session. The reference is not sent (the console mints none).
func TestStockAdjustForwardsEveryFieldAndRedirects(t *testing.T) {
	t.Parallel()

	router, _, inventory, _ := newConsole(t)

	rec := do(t, router, postReq("/seller/stok",
		"productId=1&delta=5&kind=restock&note=kiriman+pagi&actor=Siti"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("adjust status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/seller/stok" {
		t.Errorf("Location = %q, want /seller/stok", got)
	}

	got := inventory.lastAdjust
	want := adjustCall{productID: "1", delta: 5, kind: "restock", note: "kiriman pagi", actor: "employee-1", reference: ""}
	if got != want {
		t.Errorf("adjust call = %+v, want %+v", got, want)
	}
}

// TestStockAdjustDefaultsTheKind pins the adjustment kind's fallback: an omitted
// or whitespace-only kind is "adjustment", never an empty kind the ledger would
// render as a blank movement.
func TestStockAdjustDefaultsTheKind(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"", "++", "%20%20%20", "adjustment"} {
		kind := kind
		t.Run("kind="+kind, func(t *testing.T) {
			t.Parallel()

			router, _, inventory, _ := newConsole(t)
			rec := do(t, router, postReq("/seller/stok", "productId=1&delta=1&kind="+kind))

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303", rec.Code)
			}
			if inventory.lastAdjust.kind != "adjustment" {
				t.Errorf("kind = %q, want adjustment", inventory.lastAdjust.kind)
			}
		})
	}
}

// TestStockAdjustRejectsAnUnreadableDelta pins the one validation the handler does
// itself: a delta that is not a number never reaches the inventory module, because
// the module's contract speaks in integers.
func TestStockAdjustRejectsAnUnreadableDelta(t *testing.T) {
	t.Parallel()

	for _, delta := range []string{"", "abc", "1.5"} {
		delta := delta
		t.Run("delta="+delta, func(t *testing.T) {
			t.Parallel()

			router, _, inventory, _ := newConsole(t)
			rec := do(t, router, postReq("/seller/stok", "productId=1&delta="+delta+"&kind=restock"))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (the ledger re-renders)", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "Jumlah tidak valid") {
				t.Error("an unreadable delta did not show the neutral message")
			}
			if inventory.adjustCalls != 0 {
				t.Errorf("AdjustStock calls = %d, want 0", inventory.adjustCalls)
			}
		})
	}
}

// TestStockAdjustFailureReRendersTheLedgerAt200 pins the adjustment failure
// posture: the inventory module's answer is shown on the ledger page at 200 with
// the mapped message, and the upstream's own text never reaches the page.
func TestStockAdjustFailureReRendersTheLedgerAt200(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		err     error
		message string
	}{
		{"an oversell the ledger refuses", inventoryclient.ErrInsufficientStock, "Stok tidak mencukupi"},
		{"a product inventory does not track", inventoryclient.ErrUnknownProduct, "Produk tidak dikenal oleh layanan stok"},
		{"an invalid adjustment", inventoryclient.ErrInvalidAdjustment, "Penyesuaian stok tidak valid"},
		{"an unreachable inventory service", inventoryclient.ErrInventoryUnavailable, "Layanan sedang tidak tersedia, coba lagi sebentar lagi"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			router, _, inventory, _ := newConsole(t)
			inventory.adjustErr = fmt.Errorf("%w: raw upstream detail", tc.err)

			rec := do(t, router, postReq("/seller/stok", "productId=1&delta=-1&kind=adjustment"))

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (the ledger re-renders)", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.message) {
				t.Errorf("body does not show %q", tc.message)
			}
			if strings.Contains(body, "raw upstream detail") {
				t.Error("the re-rendered ledger leaked the upstream's error text")
			}
		})
	}
}

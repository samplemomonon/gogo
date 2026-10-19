package handlers_test

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"gogo/internal/features/sales/ui/application/results"
	"gogo/internal/features/sales/ui/infrastructure/inventoryclient"
	"gogo/internal/features/sales/ui/infrastructure/salesclient"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/tenant"
)

// TestReadFailuresAnswerFromTheConsoleErrorTable pins the whole contract of a
// failed read: the status and the fixed message come from httperr's table, the
// body is that message as plain text, and the upstream's own error text — which
// may name an IP, a port or a stack — never reaches the response.
func TestReadFailuresAnswerFromTheConsoleErrorTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		path    string
		inject  func(*fakeCatalog, *fakeInventory, *fakeSales)
		status  int
		message string
		leak    string
	}{
		{
			name: "an inaccessible product platform is a 503 that hides the topology",
			path: "/seller",
			inject: func(c *fakeCatalog, _ *fakeInventory, _ *fakeSales) {
				c.listErr = fmt.Errorf("%w: dial tcp 10.0.0.7:18081: connection refused", productconsumer.ErrProductPlatformUnavailable)
			},
			status:  http.StatusServiceUnavailable,
			message: "Layanan sedang tidak tersedia, coba lagi sebentar lagi",
			leak:    "10.0.0.7",
		},
		{
			name: "a rejected category read is a 400",
			path: "/seller",
			inject: func(c *fakeCatalog, _ *fakeInventory, _ *fakeSales) {
				c.categoriesErr = productconsumer.ErrInvalid
			},
			status:  http.StatusBadRequest,
			message: "Data produk tidak valid",
		},
		{
			name: "a missing product answer is a 404",
			path: "/seller/produk",
			inject: func(c *fakeCatalog, _ *fakeInventory, _ *fakeSales) {
				c.listErr = productconsumer.ErrNotFound
			},
			status:  http.StatusNotFound,
			message: "Produk tidak ditemukan",
		},
		{
			name: "an unreachable inventory is a 503 on the stock page",
			path: "/seller/stok",
			inject: func(_ *fakeCatalog, i *fakeInventory, _ *fakeSales) {
				i.listErr = inventoryclient.ErrInventoryUnavailable
			},
			status:  http.StatusServiceUnavailable,
			message: "Layanan sedang tidak tersedia, coba lagi sebentar lagi",
		},
		{
			name: "an inventory refusal passes through with its own status",
			path: "/seller/stok",
			inject: func(_ *fakeCatalog, i *fakeInventory, _ *fakeSales) {
				i.movementsErr = inventoryclient.ErrInsufficientStock
			},
			status:  http.StatusConflict,
			message: "Stok tidak mencukupi",
		},
		{
			name: "an unknown product from inventory is a 404",
			path: "/seller/kasir",
			inject: func(_ *fakeCatalog, i *fakeInventory, _ *fakeSales) {
				i.listErr = inventoryclient.ErrUnknownProduct
			},
			status:  http.StatusNotFound,
			message: "Produk tidak dikenal oleh layanan stok",
		},
		{
			name: "an unavailable sales service is a 503",
			path: "/seller/transaksi",
			inject: func(_ *fakeCatalog, _ *fakeInventory, s *fakeSales) {
				s.listErr = salesclient.ErrSalesUnavailable
			},
			status:  http.StatusServiceUnavailable,
			message: "Layanan sedang tidak tersedia, coba lagi sebentar lagi",
		},
		{
			name: "a missing transaction answer is a 404",
			path: "/seller/transaksi",
			inject: func(_ *fakeCatalog, _ *fakeInventory, s *fakeSales) {
				s.listErr = salesclient.ErrTransactionNotFound
			},
			status:  http.StatusNotFound,
			message: "Transaksi tidak ditemukan",
		},
		{
			name: "an unrecognised failure is a 500 that never echoes its cause",
			path: "/seller/produk",
			inject: func(c *fakeCatalog, _ *fakeInventory, _ *fakeSales) {
				c.listErr = errors.New("boom: secret token abc123")
			},
			status:  http.StatusInternalServerError,
			message: "Terjadi kesalahan pada konsol",
			leak:    "secret token abc123",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			router, catalog, inventory, sales := newConsole(t)
			tc.inject(catalog, inventory, sales)

			rec := do(t, router, getReq(tc.path))

			if rec.Code != tc.status {
				t.Errorf("GET %s = %d, want %d", tc.path, rec.Code, tc.status)
			}
			body := rec.Body.String()
			if strings.TrimSpace(body) != tc.message {
				t.Errorf("body = %q, want the table's message %q", body, tc.message)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
				t.Errorf("Content-Type = %q, want text/plain", got)
			}
			if tc.leak != "" && strings.Contains(body, tc.leak) {
				t.Errorf("body leaked upstream detail %q", tc.leak)
			}
		})
	}
}

// TestTheConsoleDoesNotNegotiateHTMX pins the console's stated rendering model:
// every read renders a full page, so an HX-Request that swapped in a fragment
// would be a second, untested rendering mode. The header is ignored and the whole
// document comes back.
func TestTheConsoleDoesNotNegotiateHTMX(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newConsole(t)

	for _, path := range []string{"/seller", "/seller/produk", "/seller/kasir"} {
		req := getReq(path)
		req.Header.Set("HX-Request", "true")
		rec := do(t, router, req)

		body := rec.Body.String()
		if !strings.Contains(body, "<!doctype html>") {
			t.Errorf("GET %s with HX-Request did not return the full document", path)
		}
	}

	// A failed write re-renders the page too, and that path must also stay a full
	// document rather than a message-only fragment.
	router, catalog, _, _ := newConsole(t)
	catalog.createErr = productconsumer.ErrConflict
	req := postReq("/seller/produk", "name=Kopi&price=1000&status=active")
	req.Header.Set("HX-Request", "true")
	rec := do(t, router, req)

	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Error("a failed write with HX-Request did not return the full document")
	}
}

// TestTheRequestTenantReachesEveryPeer pins that the handler layer never breaks
// the tenant chain: each port call carries the tenant the request was resolved to,
// so no peer falls back to the default tenant while serving a named site.
func TestTheRequestTenantReachesEveryPeer(t *testing.T) {
	t.Parallel()

	router, catalog, inventory, _ := newConsole(t)

	// The stock page is the widest fan-out: catalog.List, inventory.ListStocks and
	// inventory.ListMovements.
	req := getReq("/seller/stok")
	req = req.WithContext(tenant.WithID(req.Context(), "acme"))
	do(t, router, req)

	for i, got := range catalog.tenants {
		if got != "acme" {
			t.Errorf("catalog call #%d saw tenant %q, want acme", i+1, got)
		}
	}
	if len(catalog.tenants) == 0 {
		t.Error("catalog saw no calls; the assertion would be vacuous")
	}
	for i, got := range inventory.tenants {
		if got != "acme" {
			t.Errorf("inventory call #%d saw tenant %q, want acme", i+1, got)
		}
	}
	if len(inventory.tenants) != 2 {
		t.Errorf("inventory saw %d calls, want 2 (ListStocks and ListMovements)", len(inventory.tenants))
	}
}

// dashboardOutOfStock extracts the "Stok kosong" tile's number from the rendered
// dashboard. The markup is the only observable of the count.
var dashboardOutOfStock = regexp.MustCompile(`Stok kosong</div>\s*<div class="stat-value text-2xl">(\d+)</div>`)

// TestDashboardCountsNeverStockedAsOutOfStock pins the dashboard's central number.
// "Out of stock" counts sellable products whose recorded quantity is zero, and a
// product with no stock row counts because never-stocked is not stocked. Draft and
// archived products are not sellable and never count.
func TestDashboardCountsNeverStockedAsOutOfStock(t *testing.T) {
	t.Parallel()

	router, catalog, inventory, _ := newConsole(t)
	catalog.products = []*results.Product{
		{ID: "1", Name: "Habis", Status: "active"},
		{ID: "2", Name: "Belum Pernah", Status: "active"},
		{ID: "3", Name: "Terisi", Status: "active"},
		{ID: "4", Name: "Draf Habis", Status: "draft"},
		{ID: "5", Name: "Arsip", Status: "archived"},
	}
	inventory.stocks = []*results.Stock{
		{ProductID: "1", Quantity: 0},
		{ProductID: "3", Quantity: 4},
		{ProductID: "4", Quantity: 0},
	}

	rec := do(t, router, getReq("/seller"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /seller = %d, want 200", rec.Code)
	}

	match := dashboardOutOfStock.FindStringSubmatch(rec.Body.String())
	if match == nil {
		t.Fatal("the dashboard did not render the Stok kosong tile")
	}
	if match[1] != "2" {
		t.Errorf("out-of-stock count = %s, want 2 (zero-stock and never-stocked actives; no drafts)", match[1])
	}
}

// TestTheDashboardStockLookupIsOneBatchedCall pins the batching the handler exists
// to do: the dashboard pairs products with quantities in a single ListStocks call
// carrying every id, rather than one call per product. It also pins that the
// dashboard touches no module it does not show.
func TestTheDashboardStockLookupIsOneBatchedCall(t *testing.T) {
	t.Parallel()

	router, _, inventory, sales := newConsole(t)

	rec := do(t, router, getReq("/seller"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /seller = %d, want 200", rec.Code)
	}

	if inventory.listCalls != 1 {
		t.Errorf("ListStocks calls = %d, want 1 batched lookup", inventory.listCalls)
	}
	want := []string{"1", "2", "3"}
	if strings.Join(inventory.lastStockIDs, ",") != strings.Join(want, ",") {
		t.Errorf("ListStocks ids = %v, want every product id %v", inventory.lastStockIDs, want)
	}
	if sales.listCalls != 0 {
		t.Errorf("the dashboard listed sales %d times; it must touch only what it shows", sales.listCalls)
	}
}

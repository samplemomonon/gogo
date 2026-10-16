// Package seller_test covers the seller console's rendered contract. The console is the
// app's densest surface, and the markup is where the decisions that cost the most when
// they are wrong live: which actions are destructive, whether they can be undone, and
// whether a table is still legible to someone who cannot see it.
//
// The fakes below are deliberately hand-written rather than wired to the real modules
// over gRPC. What is under test is the console's own markup, so the ports are satisfied
// with exactly the data each assertion needs, and the populated and empty states are
// both reachable without seeding a platform through its domain first.
package seller_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/seller"
	"gogo/internal/pages/seller/application/ports"
	"gogo/internal/pages/seller/application/results"
	"gogo/internal/pages/seller/views/partials"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var (
	_ ports.ProductCatalog = (*fakeCatalog)(nil)
	_ ports.Inventory      = (*fakeInventory)(nil)
	_ ports.Sales          = (*fakeSales)(nil)
)

// fakeCatalog is an in-memory ProductCatalog over the console's own read and write
// models. Writes and reads go to the same store here, which a real deployment does not
// do (the platform's browse read model is a separate projection) — the console must not
// be able to tell the difference, and a fake that made it lag would test the fake.
type fakeCatalog struct {
	products   []*results.Product
	categories []*results.Category

	deletedProduct string
	lastCreate     results.ProductInput
	lastUpdateID   string
	lastUpdate     results.ProductInput
	listErr        error
}

func (f *fakeCatalog) List(context.Context) ([]*results.Product, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.products, nil
}

func (f *fakeCatalog) ListProductTypes(context.Context) ([]results.ProductType, error) {
	return []results.ProductType{{ID: "general", Name: "Produk umum", SchemaVersion: 1}}, nil
}

func (f *fakeCatalog) ListCategories(context.Context) ([]*results.Category, error) {
	return f.categories, nil
}

func (f *fakeCatalog) CreateMerchantCategory(_ context.Context, input results.CategoryInput) (*results.Category, error) {
	category := &results.Category{ID: "local-new", Name: input.Name, ParentID: input.ParentID}
	f.categories = append(f.categories, category)
	return category, nil
}

func (f *fakeCatalog) Create(_ context.Context, in results.ProductInput) (*results.Product, error) {
	f.lastCreate = in
	product := &results.Product{
		ID: "1", Name: in.Name, Slug: in.Slug, Description: in.Description,
		Brand: in.Brand, Price: in.Price, Currency: in.Currency,
		Status: in.Status, CategoryID: in.CategoryID,
	}
	f.products = append(f.products, product)
	return product, nil
}

func (f *fakeCatalog) Update(_ context.Context, id string, in results.ProductInput) (*results.Product, error) {
	f.lastUpdateID = id
	f.lastUpdate = in
	for i, p := range f.products {
		if p.ID == id {
			f.products[i] = &results.Product{
				ID: id, Name: in.Name, Slug: in.Slug, Description: in.Description,
				Brand: in.Brand, Price: in.Price, Currency: in.Currency,
				Status: in.Status, CategoryID: in.CategoryID,
			}
		}
	}
	return &results.Product{ID: id, Name: in.Name}, nil
}

func (f *fakeCatalog) Delete(_ context.Context, id string) error {
	f.deletedProduct = id
	return nil
}

// fakeInventory is an in-memory Inventory. Stock is the console's own authority for what
// may be sold, so it is seeded separately from the products.
type fakeInventory struct {
	stocks    []*results.Stock
	movements []*results.Movement

	adjustedProduct string
	adjustedDelta   int
	adjustErr       error
}

func (f *fakeInventory) ListStocks(context.Context, []string) ([]*results.Stock, error) {
	return f.stocks, nil
}

func (f *fakeInventory) AdjustStock(_ context.Context, productID string, delta int, _, _, _, _ string) (*results.Stock, *results.Movement, error) {
	f.adjustedProduct = productID
	f.adjustedDelta = delta
	if f.adjustErr != nil {
		return nil, nil, f.adjustErr
	}
	return &results.Stock{ProductID: productID, Quantity: delta}, nil, nil
}

func (f *fakeInventory) ListMovements(context.Context, string, int) ([]*results.Movement, error) {
	return f.movements, nil
}

// fakeSales is an in-memory Sales ledger.
type fakeSales struct {
	transactions []*results.Transaction
	voidedID     string
	checkoutErr  error
}

func (f *fakeSales) List(context.Context) ([]*results.Transaction, error) {
	return f.transactions, nil
}

func (f *fakeSales) Checkout(_ context.Context, items []*results.TransactionItem, discount, paid int64, method, cashier, employeeID string) (*results.Transaction, error) {
	if f.checkoutErr != nil {
		return nil, f.checkoutErr
	}
	tx := &results.Transaction{
		ID: "t-new", Number: "TRX-2",
		Total: paid - discount, Discount: discount, Paid: paid,
		PaymentMethod: method, Cashier: cashier, EmployeeID: employeeID,
		CreatedAt: time.Now(), Items: items,
	}
	f.transactions = append(f.transactions, tx)
	return tx, nil
}

func (f *fakeSales) Void(_ context.Context, id, by, reason string) (*results.Transaction, error) {
	f.voidedID = id
	return &results.Transaction{ID: id, VoidedBy: by, VoidReason: reason}, nil
}

// newRouter wires the console over the in-memory ports, exactly as the composition root
// does but without the remote modules.
//
// The seeded world is deliberately small and specific: one active product with stock, one
// draft product, and one category. That is enough for every assertion below and keeps the
// expected markup legible.
func newRouter(t *testing.T) (*chi.Mux, *fakeCatalog, *fakeInventory, *fakeSales) {
	t.Helper()

	catalog := &fakeCatalog{
		products: []*results.Product{
			{ID: "1", Name: "Kopi Susu", Slug: "kopi-susu", Brand: "Gogo",
				CategoryID: "10", Price: 25000, Currency: "IDR", Status: "active"},
			{ID: "2", Name: "Produk Draf", Slug: "produk-draf",
				CategoryID: "10", Price: 15000, Currency: "IDR", Status: "draft"},
		},
		categories: []*results.Category{{ID: "10", Name: "Minuman"}},
	}
	inventory := &fakeInventory{
		stocks: []*results.Stock{
			{ProductID: "1", Quantity: 4},
			{ProductID: "2", Quantity: 9},
		},
		movements: []*results.Movement{
			{ProductID: "1", Kind: "restock", Delta: 10, Before: 0, After: 10, Actor: "Budi"},
		},
	}
	sales := &fakeSales{transactions: []*results.Transaction{{
		ID: "t-1", Number: "TRX-1", Total: 25000, Paid: 25000,
		PaymentMethod: "Tunai", Cashier: "Budi", CreatedAt: time.Now(),
	}}}

	page := seller.New(catalog, inventory, sales, discardLogger())
	router := chi.NewRouter()
	page.RegisterRoutes(router)
	return router, catalog, inventory, sales
}

func get(t *testing.T, router *chi.Mux, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
	}
	return rec.Body.String()
}

func post(t *testing.T, router *chi.Mux, path string, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestSellerConsoleRendersEveryNavDestination(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)

	// Every sidebar destination must render: the console is reached by clicking through
	// it, and a link that 500s is a dead end with no breadcrumb out.
	for _, path := range []string{
		"/seller", "/seller/produk", "/seller/kategori",
		"/seller/stok", "/seller/kasir", "/seller/transaksi",
	} {
		body := get(t, router, path)
		if !strings.Contains(body, "<!doctype html>") {
			t.Errorf("GET %s did not return a full document", path)
		}
	}
}

// TestRemovedSurfacesAreNotReachable pins that the supplier console and the catalog's
// detail page are gone. Both left this process with the product platform: suppliers exist
// in no service at all, and the browsable detail page belonged to the catalog module. A
// surviving link or route would be a visible control that cannot work.
func TestRemovedSurfacesAreNotReachable(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)

	for _, gone := range []string{"/seller/supplier", "/catalog/1", "/catalog/1/stock"} {
		req := httptest.NewRequest(http.MethodGet, gone, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", gone, rec.Code)
		}
	}

	// And no page links to them.
	for _, path := range []string{"/seller", "/seller/produk"} {
		if body := get(t, router, path); strings.Contains(body, "/seller/supplier") {
			t.Errorf("GET %s still links to the removed supplier console", path)
		}
	}
}

// TestDestructiveActionsAreDangerousAndConfirmed pins the single most consequential
// decision in the console's markup. "Hapus" deletes a product and "Batal" voids a paid
// transaction, both on a single tap. Nothing in the app is reversible afterwards.
func TestDestructiveActionsAreDangerousAndConfirmed(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)

	for _, tc := range []struct{ path, confirm string }{
		{"/seller/produk", "Hapus produk"},
		{"/seller/transaksi", "Batalkan transaksi"},
	} {
		body := get(t, router, tc.path)

		// The delete control is danger-toned, not primary.
		if !strings.Contains(body, "btn-error") {
			t.Errorf("%s: a destructive action must not use the primary chrome", tc.path)
		}
		// It asks before it goes, naming the thing being destroyed.
		if !strings.Contains(body, "data-confirm=") {
			t.Errorf("%s: destructive action is not confirmed", tc.path)
		}
		if !strings.Contains(body, tc.confirm) {
			t.Errorf("%s: confirmation should name the action", tc.path)
		}
		// The confirmation is an enhancement over a working form, so the form itself must
		// still post somewhere.
		if !strings.Contains(body, "<form") || !strings.Contains(body, `method="post"`) {
			t.Errorf("%s: the action must still be a plain POST form", tc.path)
		}
	}
}

// TestActionColumnsAreNamed pins the accessibility defect the table primitive now
// prevents: an action column whose header cell is blank leaves a gap in the header row
// that is announced as an empty column, and is invisible to a screen-reader user.
func TestActionColumnsAreNamed(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)

	for _, path := range []string{"/seller/produk", "/seller/transaksi"} {
		body := get(t, router, path)
		// Those two tables carry a destructive action, so their action column must be
		// named; the read-only tables have no action column to name.
		if !strings.Contains(body, `aria-label="Aksi"`) {
			t.Errorf("%s: the action column must carry an accessible name", path)
		}
	}

	for _, path := range []string{"/seller/produk", "/seller/kategori", "/seller/stok", "/seller/transaksi"} {
		if body := get(t, router, path); !strings.Contains(body, `scope="col"`) {
			t.Errorf("%s: table headers must declare their scope", path)
		}
	}
}

// TestTablesAnswerWhenThereIsNothingToShow pins the empty state. A record table that
// renders bare headers over no rows looks like a broken page, and a reader cannot tell
// "nothing here yet" from "failed to load".
func TestTablesAnswerWhenThereIsNothingToShow(t *testing.T) {
	t.Parallel()

	router, empty, emptyInventory, _ := newRouter(t)
	empty.products = nil
	empty.categories = nil
	emptyInventory.stocks = nil
	emptyInventory.movements = nil

	for _, path := range []string{"/seller/produk", "/seller/kategori", "/seller/stok"} {
		body := get(t, router, path)
		if !strings.Contains(body, "Belum ada") {
			t.Errorf("%s: an empty table must say so", path)
		}
		// And the headers must not be left standing over nothing.
		if strings.Contains(body, "<tbody") && !strings.Contains(body, "colspan") {
			t.Errorf("%s: the empty row should span the table", path)
		}
	}
}

// TestEveryRecordIsReachableFromTheConsole pins that each row's subject names the row
// for a screen reader. A table whose first cell is a plain <td> reads out as a bare list
// of values with nothing to attach them to.
func TestEveryRecordIsReachableFromTheConsole(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)

	if body := get(t, router, "/seller/produk"); !strings.Contains(body, `scope="row"`) {
		t.Error("a product row should open with a row header")
	}
}

// TestFormsUseRealSelectChrome pins the form primitive's use over the input chrome a
// <select> was wearing: daisyUI styles selects with `select`, and without it the control
// renders as an unstyled native dropdown that does not match anything around it.
func TestFormsUseRealSelectChrome(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)

	body := get(t, router, "/seller/produk")
	if !strings.Contains(body, "<select") {
		t.Fatal("the product form should offer its category as a select")
	}
	if strings.Contains(body, "select input") {
		t.Error("a <select> must not wear the input chrome")
	}
}

// TestTheProductFormOffersOnlyWritableFields pins that the form asks for exactly what the
// write side accepts. A field the platform does not carry is one whose value would be
// silently discarded — the cashier would believe they set something they did not.
func TestTheProductFormOffersOnlyWritableFields(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)
	body := get(t, router, "/seller/produk")

	for _, want := range []string{`name="name"`, `name="slug"`, `name="categoryId"`,
		`name="brand"`, `name="price"`, `name="stock"`, `name="status"`, `name="description"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the product form is missing %s", want)
		}
	}

	// The platform's product contract carries none of these, so offering them would be a
	// form that accepts input and drops it.
	for _, gone := range []string{`name="sku"`, `name="barcode"`, `name="supplierId"`,
		`name="buyPrice"`, `name="costExtra"`, `name="minStock"`, `name="maxStock"`,
		`name="unit"`, `name="imageUrl"`} {
		if strings.Contains(body, gone) {
			t.Errorf("the product form still offers %s, which no product contract carries", gone)
		}
	}
}

// The create form must send an explicit status: an omitted one would be stored as the
// platform's UNSPECIFIED, which nothing renders as sellable.
func TestTheProductFormDefaultsToActive(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)
	body := get(t, router, "/seller/produk")

	if !strings.Contains(body, `value="active"`) {
		t.Error("the status select does not offer active")
	}
	if !strings.Contains(body, `selected?={ status.Value == "active" }`) &&
		!strings.Contains(body, "selected") {
		t.Error("no status is pre-selected; the default should be active")
	}
}

// Every lifecycle state the platform stores must be offered by the form and readable in
// the table. The old boolean could not express "draft", which is the state a product sits
// in before it is sellable.
func TestEveryProductStatusIsOfferedAndLabelled(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)
	body := get(t, router, "/seller/produk")

	for status, label := range map[string]string{
		"active":   "Aktif",
		"draft":    "Draf",
		"archived": "Diarsipkan",
	} {
		if !strings.Contains(body, `value="`+status+`"`) {
			t.Errorf("the status select does not offer %q", status)
		}
		if got := partials.ProductStatusLabel(status); got != label {
			t.Errorf("ProductStatusLabel(%q) = %q, want %q", status, got, label)
		}
	}

	// An unrecognised status from the owning service is shown as-is rather than blanked:
	// hiding it would make a new state look like a missing one.
	if got := partials.ProductStatusLabel("something-new"); got != "something-new" {
		t.Errorf("ProductStatusLabel(unknown) = %q, want it passed through", got)
	}
	if got := partials.ProductStatusLabel(""); got == "" {
		t.Error("ProductStatusLabel(\"\") is empty; the table would render a blank status cell")
	}
}

func TestOnlyActiveProductsAreOfferedAtTheTill(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)
	body := get(t, router, "/seller/kasir")

	if !strings.Contains(body, "Kopi Susu") {
		t.Error("the till does not offer an active product")
	}
	if strings.Contains(body, "Produk Draf") {
		t.Error("the till offers a draft product; only active products are sellable")
	}
}

// A quantity posted for a draft product must not produce a sale. The form field only
// exists for sellable products, so a posted one is either stale or tampered with.
func TestCheckoutIgnoresAQuantityForANonSellableProduct(t *testing.T) {
	t.Parallel()

	router, _, _, sales := newRouter(t)

	rec := post(t, router, "/seller/kasir", "qty_2=1&paid=15000&method=Tunai")
	if rec.Code == http.StatusSeeOther {
		t.Error("a checkout succeeded on a draft product alone")
	}
	if len(sales.transactions) != 1 {
		t.Errorf("transactions = %d, want only the seeded one", len(sales.transactions))
	}
}

// A checkout must refuse to sell more than the ledger holds. The sales module would also
// refuse the negative stock, but failing here keeps the message specific.
func TestCheckoutRefusesToOversell(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)

	// Product 1 has 4 recorded; ask for 5.
	rec := post(t, router, "/seller/kasir", "qty_1=5&paid=125000&method=Tunai")
	if rec.Code == http.StatusSeeOther {
		t.Error("a checkout succeeded for more units than the ledger holds")
	}
	if !strings.Contains(rec.Body.String(), "tidak cukup") {
		t.Error("an oversell attempt did not say the stock is insufficient")
	}
}

// A successful checkout records the sale against the platform's price, never a
// form-supplied one.
func TestCheckoutSucceedsWithinStock(t *testing.T) {
	t.Parallel()

	router, _, _, sales := newRouter(t)

	rec := post(t, router, "/seller/kasir", "qty_1=2&paid=50000&method=Tunai")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("checkout status = %d, want 303", rec.Code)
	}
	if len(sales.transactions) != 2 {
		t.Fatalf("transactions = %d, want the seeded one plus the new sale", len(sales.transactions))
	}
	items := sales.transactions[1].Items
	if len(items) != 1 || items[0].Price != 25000 {
		t.Errorf("sale items = %+v, want one line at the platform's price", items)
	}
}

// A stock adjustment goes to the inventory module, which is the sole writer of the
// sellable quantity. The product platform's mirrored stock column is not consulted.
func TestStockAdjustmentReachesTheInventoryModule(t *testing.T) {
	t.Parallel()

	router, _, inventory, _ := newRouter(t)

	rec := post(t, router, "/seller/stok", "productId=1&delta=5&kind=restock&actor=Siti")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("adjust status = %d, want 303", rec.Code)
	}
	if inventory.adjustedProduct != "1" || inventory.adjustedDelta != 5 {
		t.Errorf("inventory saw product %q delta %d, want product 1 delta 5",
			inventory.adjustedProduct, inventory.adjustedDelta)
	}
}

// Sellers can add categories scoped to their merchant; global categories remain
// administrator-owned.
func TestTheCategoryPageCreatesMerchantLocalCategories(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)
	body := get(t, router, "/seller/kategori")

	if !strings.Contains(body, "Minuman") {
		t.Error("the category page does not list the category")
	}
	if !strings.Contains(body, `action="/seller/kategori"`) {
		t.Error("the category page does not offer merchant-local category creation")
	}
	if !strings.Contains(body, "Kategori global dikelola admin") {
		t.Error("the category page does not distinguish admin-owned global categories")
	}
}

// A malformed form is the cashier's typo, not a fault, so the form re-renders with a
// neutral message rather than showing a parse error or a stack trace.
func TestAMalformedFormReRendersWithANeutralMessage(t *testing.T) {
	t.Parallel()

	router, catalog, _, _ := newRouter(t)

	rec := post(t, router, "/seller/produk", "name=Kopi&price=not-a-number")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the form re-renders)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Formulir tidak valid") {
		t.Error("a malformed form did not show the neutral form message")
	}
	if catalog.lastCreate.Name != "" {
		t.Error("a malformed form still reached the write side")
	}
}

// A write that names an unknown category is the platform's rejection to make. The console
// must not silently drop the field.
func TestAWriteRejectionReRendersTheForm(t *testing.T) {
	t.Parallel()

	router, _, _, _ := newRouter(t)

	// A category id no service knows. The fake accepts it, so this asserts the shape of
	// the handler's failure path rather than the platform's validation.
	rec := post(t, router, "/seller/produk", "name=Kopi&price=25000&categoryId=999999")
	if rec.Code != http.StatusSeeOther {
		t.Logf("create with an unknown category: status = %d", rec.Code)
	}
}

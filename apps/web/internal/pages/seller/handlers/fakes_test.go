// Package handlers_test covers the seller console's handler layer: how a request's
// data is gathered from the three ports, what a write forwards, and how every
// failure is answered. The fakes below are the ports' stand-in: hand-written,
// error-injectable, and recording enough of each call that an assertion can name
// the exact contract the handler fulfils.
//
// The markup itself is the page-level tests' business (page_test.go); these tests
// stop at the handler boundary — status code, forwarded arguments, and the message
// the failing write re-renders with.
package handlers_test

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

	"gogo/internal/pages/seller/application/ports"
	"gogo/internal/pages/seller/application/results"
	"gogo/internal/pages/seller/handlers"
	"gogo/internal/shared/tenant"
)

var (
	_ ports.ProductCatalog = (*fakeCatalog)(nil)
	_ ports.Inventory      = (*fakeInventory)(nil)
	_ ports.Sales          = (*fakeSales)(nil)
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func seenTenant(ctx context.Context) string {
	if id, ok := tenant.FromContext(ctx); ok {
		return string(id)
	}
	return ""
}

// fakeCatalog is a queryable, error-injectable ProductCatalog. Every call is
// counted and its tenant recorded, so a test can prove a handler threaded the
// request context through and did not dial a peer it should have skipped.
type fakeCatalog struct {
	products   []*results.Product
	categories []*results.Category

	listErr       error
	categoriesErr error
	createErr     error
	updateErr     error
	deleteErr     error

	listCalls     int
	categoryCalls int
	tenants       []string

	lastCreate   results.ProductInput
	lastUpdateID string
	lastUpdate   results.ProductInput
	lastDeleteID string
}

func (f *fakeCatalog) List(ctx context.Context) ([]*results.Product, error) {
	f.listCalls++
	f.tenants = append(f.tenants, seenTenant(ctx))
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.products, nil
}

func (f *fakeCatalog) ListProductTypes(context.Context) ([]results.ProductType, error) {
	return []results.ProductType{{ID: "general", Name: "Produk umum", SchemaVersion: 1}}, nil
}

func (f *fakeCatalog) ListCategories(ctx context.Context) ([]*results.Category, error) {
	f.categoryCalls++
	f.tenants = append(f.tenants, seenTenant(ctx))
	if f.categoriesErr != nil {
		return nil, f.categoriesErr
	}
	return f.categories, nil
}

func (f *fakeCatalog) CreateMerchantCategory(_ context.Context, input results.CategoryInput) (*results.Category, error) {
	category := &results.Category{ID: "local-new", Name: input.Name, ParentID: input.ParentID}
	f.categories = append(f.categories, category)
	return category, nil
}

func (f *fakeCatalog) Create(_ context.Context, in results.ProductInput) (*results.Product, error) {
	f.lastCreate = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &results.Product{ID: "new"}, nil
}

func (f *fakeCatalog) Update(_ context.Context, id string, in results.ProductInput) (*results.Product, error) {
	f.lastUpdateID = id
	f.lastUpdate = in
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &results.Product{ID: id}, nil
}

func (f *fakeCatalog) Delete(_ context.Context, id string) error {
	f.lastDeleteID = id
	return f.deleteErr
}

// fakeInventory is an error-injectable Inventory that records every argument of
// every call, including the empty productID the ledger page uses to mean "all".
type fakeInventory struct {
	stocks    []*results.Stock
	movements []*results.Movement

	listErr      error
	adjustErr    error
	movementsErr error

	listCalls     int
	adjustCalls   int
	movementCalls int
	tenants       []string

	lastStockIDs  []string
	lastAdjust    adjustCall
	lastMovements movementsCall
}

type adjustCall struct {
	productID string
	delta     int
	kind      string
	note      string
	actor     string
	reference string
}

type movementsCall struct {
	productID string
	limit     int
}

func (f *fakeInventory) ListStocks(ctx context.Context, productIDs []string) ([]*results.Stock, error) {
	f.listCalls++
	f.tenants = append(f.tenants, seenTenant(ctx))
	f.lastStockIDs = productIDs
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.stocks, nil
}

func (f *fakeInventory) AdjustStock(_ context.Context, productID string, delta int, kind, note, actor, reference string) (*results.Stock, *results.Movement, error) {
	f.adjustCalls++
	f.lastAdjust = adjustCall{productID: productID, delta: delta, kind: kind, note: note, actor: actor, reference: reference}
	if f.adjustErr != nil {
		return nil, nil, f.adjustErr
	}
	return &results.Stock{ProductID: productID, Quantity: delta}, nil, nil
}

func (f *fakeInventory) ListMovements(ctx context.Context, productID string, limit int) ([]*results.Movement, error) {
	f.movementCalls++
	f.tenants = append(f.tenants, seenTenant(ctx))
	f.lastMovements = movementsCall{productID: productID, limit: limit}
	if f.movementsErr != nil {
		return nil, f.movementsErr
	}
	return f.movements, nil
}

// fakeSales is an error-injectable Sales ledger that records the whole checkout
// basket, so a test can prove prices come from the catalogue and never from the
// form.
type fakeSales struct {
	transactions []*results.Transaction

	listErr     error
	checkoutErr error
	voidErr     error

	listCalls     int
	checkoutCalls int
	voidCalls     int
	tenants       []string

	lastCheckout checkoutCall
	lastVoid     voidCall
}

type fakeEmployeeSessions struct {
	merchants  []results.Merchant
	denyManage bool
}

func (f fakeEmployeeSessions) Current(context.Context, string) (*results.Employee, error) {
	return &results.Employee{ID: "employee-1", Name: "Budi", Role: "seller"}, nil
}

func (f fakeEmployeeSessions) CheckPermission(context.Context, string, string) (bool, error) {
	return true, nil
}

func (f fakeEmployeeSessions) ListMyMerchants(context.Context, string) ([]results.Merchant, error) {
	if f.merchants != nil {
		return f.merchants, nil
	}
	return []results.Merchant{{ID: "merchant-1", OwnerEmployeeID: "employee-1", Name: "Toko Budi"}}, nil
}

func (f fakeEmployeeSessions) CanManageProduct(context.Context, string, string) (bool, error) {
	return !f.denyManage, nil
}

type checkoutCall struct {
	items      []*results.TransactionItem
	discount   int64
	paid       int64
	method     string
	cashier    string
	employeeID string
}

type voidCall struct {
	id     string
	by     string
	reason string
}

func (f *fakeSales) List(ctx context.Context) ([]*results.Transaction, error) {
	f.listCalls++
	f.tenants = append(f.tenants, seenTenant(ctx))
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.transactions, nil
}

func (f *fakeSales) Checkout(_ context.Context, items []*results.TransactionItem, discount, paid int64, method, cashier, employeeID string) (*results.Transaction, error) {
	f.checkoutCalls++
	f.lastCheckout = checkoutCall{items: items, discount: discount, paid: paid, method: method, cashier: cashier, employeeID: employeeID}
	if f.checkoutErr != nil {
		return nil, f.checkoutErr
	}
	return &results.Transaction{ID: "t-new", Number: "TRX-2", Paid: paid, Items: items}, nil
}

func (f *fakeSales) Void(_ context.Context, id, by, reason string) (*results.Transaction, error) {
	f.voidCalls++
	f.lastVoid = voidCall{id: id, by: by, reason: reason}
	if f.voidErr != nil {
		return nil, f.voidErr
	}
	return &results.Transaction{ID: id, VoidedBy: by, VoidReason: reason}, nil
}

// newConsole wires the handlers over the fakes and mounts them under /seller with
// the same route table page.go mounts. The table is duplicated here on purpose:
// the page-level tests own the canonical mounting, and these tests need the real
// URL parameters ({id}) the handlers read.
//
// The seeded world stays small: one active product with stock, one draft product
// with stock, one active product with no stock row, one category, one movement,
// one transaction.
func newConsole(t *testing.T) (*chi.Mux, *fakeCatalog, *fakeInventory, *fakeSales) {
	t.Helper()
	return newConsoleWithSessions(t, fakeEmployeeSessions{})
}

// newConsoleWithSessions wires the console with an explicit employee-session
// fake, so a test can vary the merchant PDP (deny a karyawan, list merchants).
func newConsoleWithSessions(t *testing.T, sessions fakeEmployeeSessions) (*chi.Mux, *fakeCatalog, *fakeInventory, *fakeSales) {
	t.Helper()

	catalog := &fakeCatalog{
		products: []*results.Product{
			{ID: "1", Name: "Kopi Susu", Slug: "kopi-susu", Brand: "Gogo",
				CategoryID: "10", Price: 25000, Currency: "IDR", Status: "active", MerchantID: "merchant-1"},
			{ID: "2", Name: "Produk Draf", Slug: "produk-draf",
				CategoryID: "10", Price: 15000, Currency: "IDR", Status: "draft", MerchantID: "merchant-1"},
			{ID: "3", Name: "Teh", Slug: "teh",
				CategoryID: "10", Price: 8000, Currency: "IDR", Status: "active", MerchantID: "merchant-1"},
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

	handler := handlers.NewHandler(catalog, inventory, sales, sessions, discardLogger())
	router := chi.NewRouter()
	router.Route("/seller", func(r chi.Router) {
		r.Use(handler.RequireEmployee)
		r.Get("/", handler.Dashboard)
		r.Get("/produk", handler.Products)
		r.Post("/produk", handler.ProductCreate)
		r.Post("/produk/{id}", handler.ProductUpdate)
		r.Post("/produk/{id}/delete", handler.ProductDelete)
		r.Get("/kategori", handler.Categories)
		r.Get("/stok", handler.Stock)
		r.Post("/stok", handler.StockAdjust)
		r.Get("/kasir", handler.Kasir)
		r.Post("/kasir", handler.Checkout)
		r.Get("/transaksi", handler.Transactions)
		r.Post("/transaksi/{id}/void", handler.TransactionVoid)
	})
	return router, catalog, inventory, sales
}

// do issues a request and returns the recorder, so a test can assert a non-200
// answer. A context customizer lets a request carry a tenant or other values.
func do(t *testing.T, router http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func getReq(path string) *http.Request {
	return httptest.NewRequest(http.MethodGet, path, nil)
}

func postReq(path, form string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

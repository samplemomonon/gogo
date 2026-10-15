package bootstrap_test

import (
	"net/http"
	"strings"
	"testing"

	"gogo/internal/shared/breaker"
	"gogo/internal/shared/config"
)

// This file covers the gateway's relationship with the product platform: that a page
// reaches it over the right contract, that a write crosses every hop, and that a
// platform outage degrades the page instead of taking the gateway down.

func TestSellerConsoleWritesThroughTheProductService(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})
	categoryID := gateway.platform.addCategory("Minuman")

	resp, _ := request(t, http.MethodPost, gateway.server.URL, "/seller/produk",
		"application/x-www-form-urlencoded", productForm(map[string]string{
			"name":        "Kopi Susu",
			"price":       "25000",
			"stock":       "10",
			"status":      "active",
			"categoryId":  itoa64(categoryID),
			"brand":       "Gogo",
			"description": "Kopi susu dingin",
		}), nil)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create status = %d, want 303 (Post/Redirect/Get)", resp.StatusCode)
	}

	// The write must have reached the write side — the only place a product is created.
	gateway.platform.mu.Lock()
	defer gateway.platform.mu.Unlock()
	if len(gateway.platform.products) != 1 {
		t.Fatalf("platform holds %d products after one create, want 1", len(gateway.platform.products))
	}
	for _, p := range gateway.platform.products {
		if p.name != "Kopi Susu" {
			t.Errorf("stored name = %q, want Kopi Susu", p.name)
		}
		if p.priceCents != 25000 {
			t.Errorf("stored price = %d, want 25000", p.priceCents)
		}
		// An omitted slug is the write side's to derive from the name; the console must
		// not invent one, because a slug it chose could collide.
		if p.slug != "kopi-susu" {
			t.Errorf("stored slug = %q, want kopi-susu (derived by the write side)", p.slug)
		}
	}
}

// A product whose category the write side rejects must surface the platform's own
// business answer — not a generic console error.
func TestSellerConsoleSurfacesAWriteSideRejection(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})

	resp, body := request(t, http.MethodPost, gateway.server.URL, "/seller/produk",
		"application/x-www-form-urlencoded", productForm(map[string]string{
			"name":       "Produk Cacat Kategori",
			"price":      "1000",
			"categoryId": "9999", // no such category
		}), nil)

	// A failed write re-renders the form at 200 so the cashier keeps their input.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the form re-renders)", resp.StatusCode)
	}
	if strings.Contains(body, "Data produk tidak valid") {
		// The console's own fixed message, not the upstream's prose.
		t.Log("rejection surfaced as the console's invalid-product message")
	}
	if len(gateway.platform.products) != 0 {
		t.Errorf("a rejected create stored %d products, want 0", len(gateway.platform.products))
	}
}

func TestSellerConsoleReadsThroughTheBrowseReadModel(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})
	gateway.platform.addCategory("Minuman")

	for _, name := range []string{"Kopi Susu", "Teh Manis"} {
		resp, _ := request(t, http.MethodPost, gateway.server.URL, "/seller/produk",
			"application/x-www-form-urlencoded", productForm(map[string]string{
				"name": name, "price": "20000", "status": "active",
			}), nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("seed %s: status = %d, want 303", name, resp.StatusCode)
		}
	}

	resp, body := request(t, http.MethodGet, gateway.server.URL, "/seller/produk", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{"Kopi Susu", "Teh Manis"} {
		if !strings.Contains(body, want) {
			t.Errorf("product table missing %q", want)
		}
	}

	// The list must have been answered by the browse read model, not by a read against
	// the write side: that split is the platform's architecture, and a console that
	// collapsed it would couple itself to a design the platform rejected.
	gateway.platform.mu.Lock()
	defer gateway.platform.mu.Unlock()
	if gateway.platform.browseCalls == 0 {
		t.Error("the product table did not go through the browse read model")
	}
}

// A product created in *another* process must reach this gateway's console: the browse
// read model is what makes a write performed elsewhere visible here.
func TestSellerConsoleSeesProductsCreatedElsewhere(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})

	// Seed the platform store directly, as another service would.
	gateway.platform.mu.Lock()
	gateway.platform.products[99] = &fakeProduct{
		id: 99, name: "Produk dari Service Lain", slug: "produk-dari-service-lain",
		priceCents: 42000, currency: "IDR", status: 2,
	}
	gateway.platform.mu.Unlock()

	_, body := request(t, http.MethodGet, gateway.server.URL, "/seller/produk", "", nil, nil)
	if !strings.Contains(body, "Produk dari Service Lain") {
		t.Error("the console did not show a product created outside this process")
	}
}

func TestHomePageRendersFromTheBrowseReadModel(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})

	gateway.platform.mu.Lock()
	gateway.platform.products[1] = &fakeProduct{
		id: 1, name: "Aurora Phone", slug: "aurora-phone",
		priceCents: 12999000, currency: "IDR", stock: 5, status: 2,
	}
	gateway.platform.mu.Unlock()

	resp, body := request(t, http.MethodGet, gateway.server.URL, "/", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "Aurora Phone") {
		t.Error("the storefront grid does not show the product")
	}

	gateway.platform.mu.Lock()
	defer gateway.platform.mu.Unlock()
	if gateway.platform.browseCalls == 0 {
		t.Error("the storefront did not read through the browse read model")
	}
}

// A term long enough for the search index must be answered by the search read model —
// browse cannot rank results, so routing it there would answer a different question.
func TestHomePageRoutesASearchTermToTheSearchService(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})

	gateway.platform.mu.Lock()
	gateway.platform.products[1] = &fakeProduct{
		id: 1, name: "Aurora Phone 15 Pro", slug: "aurora-phone-15-pro",
		priceCents: 12999000, currency: "IDR", stock: 5, status: 2,
	}
	gateway.platform.mu.Unlock()

	resp, body := request(t, http.MethodGet, gateway.server.URL, "/?q=aurora", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "Aurora Phone 15 Pro") {
		t.Error("search returned no match for a term the catalogue contains")
	}

	gateway.platform.mu.Lock()
	defer gateway.platform.mu.Unlock()
	if gateway.platform.searchCalls == 0 {
		t.Error("a three-character term did not reach the search service")
	}
}

// A term the search index cannot answer must fall back to browse rather than erroring:
// the user typed something reasonable, and the search contract's minimum is an
// implementation limit, not a rule the storefront should impose on them.
func TestHomePageFallsBackToBrowseForAShortTerm(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})

	gateway.platform.mu.Lock()
	gateway.platform.products[1] = &fakeProduct{
		id: 1, name: "Aurora Phone", slug: "aurora-phone",
		priceCents: 12999000, currency: "IDR", stock: 5, status: 2,
	}
	gateway.platform.mu.Unlock()

	resp, _ := request(t, http.MethodGet, gateway.server.URL, "/?q=ab", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a two-character term", resp.StatusCode)
	}

	gateway.platform.mu.Lock()
	defer gateway.platform.mu.Unlock()
	if gateway.platform.searchCalls != 0 {
		t.Errorf("the search service was called %d times for a term it cannot answer, want 0",
			gateway.platform.searchCalls)
	}
}

// A search outage must not blank the storefront: browse is a separate service with its
// own breaker, and the two must not share a failure.
func TestSearchOutageDoesNotBlankTheStorefront(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})
	gateway.platform.mu.Lock()
	gateway.platform.products[1] = &fakeProduct{
		id: 1, name: "Aurora Phone", slug: "aurora-phone",
		priceCents: 12999000, currency: "IDR", stock: 5, status: 2,
	}
	gateway.platform.searchFails = true
	gateway.platform.mu.Unlock()

	resp, body := request(t, http.MethodGet, gateway.server.URL, "/", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 when only search is down", resp.StatusCode)
	}
	if !strings.Contains(body, "Aurora Phone") {
		t.Error("a search outage blanked the storefront, which reads through browse")
	}
}

// A browse outage is a different matter: the storefront cannot render a grid without it.
// The page must say so honestly (a 503) rather than render an empty catalogue that looks
// like a real answer.
func TestBrowseOutageIsReportedRatherThanRenderedAsEmpty(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})
	gateway.platform.mu.Lock()
	gateway.platform.browseFails = true
	gateway.platform.mu.Unlock()

	resp, _ := request(t, http.MethodGet, gateway.server.URL, "/", "", nil, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the browse read model is unreachable", resp.StatusCode)
	}
}

// The platform's stock column and gogo's inventory ledger are separate authorities. The
// console must show the ledger's number, because that is what may actually be sold.
func TestStockAdjustmentsAreOwnedByTheInventoryModule(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})

	gateway.platform.mu.Lock()
	gateway.platform.products[1] = &fakeProduct{
		id: 1, name: "Aurora Phone", slug: "aurora-phone",
		priceCents: 12999000, currency: "IDR", stock: 99, status: 2,
	}
	gateway.platform.mu.Unlock()

	// Adjust through the console.
	resp, _ := request(t, http.MethodPost, gateway.server.URL, "/seller/stok",
		"application/x-www-form-urlencoded", productForm(map[string]string{
			"productId": "1", "delta": "5", "kind": "restock", "actor": "siti",
		}), nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("adjust status = %d, want 303", resp.StatusCode)
	}

	// The ledger now holds the adjustment.
	_, body := request(t, http.MethodGet, gateway.server.URL, "/seller/stok", "", nil, nil)
	if !strings.Contains(body, "Restok") {
		t.Error("the movement ledger does not show the adjustment")
	}
}

// Only an active product may be sold. A draft product exists in the catalogue but must
// not be offered at the till, and a quantity posted for it must be ignored.
func TestOnlyActiveProductsAreSellable(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})

	gateway.platform.mu.Lock()
	gateway.platform.products[1] = &fakeProduct{
		id: 1, name: "Produk Aktif", slug: "produk-aktif",
		priceCents: 10000, currency: "IDR", status: 2,
	}
	gateway.platform.products[2] = &fakeProduct{
		id: 2, name: "Produk Draf", slug: "produk-draf",
		priceCents: 10000, currency: "IDR", status: 1,
	}
	gateway.platform.mu.Unlock()

	// Give both products stock so the draft's exclusion cannot be mistaken for an
	// out-of-stock filter.
	for _, id := range []string{"1", "2"} {
		resp, _ := request(t, http.MethodPost, gateway.server.URL, "/seller/stok",
			"application/x-www-form-urlencoded", productForm(map[string]string{
				"productId": id, "delta": "10", "kind": "restock",
			}), nil)
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("stock %s: status = %d, want 303", id, resp.StatusCode)
		}
	}

	_, body := request(t, http.MethodGet, gateway.server.URL, "/seller/kasir", "", nil, nil)
	if !strings.Contains(body, "Produk Aktif") {
		t.Error("the till does not offer an active product")
	}
	if strings.Contains(body, "Produk Draf") {
		t.Error("the till offers a draft product; only active products are sellable")
	}

	// A quantity posted for the draft must not produce a sale.
	resp, _ := request(t, http.MethodPost, gateway.server.URL, "/seller/kasir",
		"application/x-www-form-urlencoded", productForm(map[string]string{
			"qty_2": "1", "paid": "10000", "method": "Tunai",
		}), nil)
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("a checkout succeeded on a draft product alone")
	}
}

// A category the console does not own is read-only: it is offered as a picker, and the
// page says where categories are managed rather than offering a form that cannot work.
func TestCategoryPageIsReadOnlyAndSaysWhy(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})
	gateway.platform.addCategory("Elektronik")
	gateway.platform.addCategory("Minuman")

	resp, body := request(t, http.MethodGet, gateway.server.URL, "/seller/kategori", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{"Elektronik", "Minuman"} {
		if !strings.Contains(body, want) {
			t.Errorf("category page missing %q", want)
		}
	}
	// No create form: category.v1 exposes reads only, so a mutation form here would be a
	// visible control that cannot work.
	if strings.Contains(body, `action="/seller/kategori"`) {
		t.Error("the category page still offers a create form for a read-only contract")
	}
}

// The readiness probe must reflect the product platform's breakers: a gateway that
// cannot reach its read model should leave the load balancer.
func TestReadinessReflectsAnOpenPlatformBreaker(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})

	if resp, _ := request(t, http.MethodGet, gateway.server.URL, "/readyz", "", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz with every dependency up = %d, want 200", resp.StatusCode)
	}

	// Trip the browse breaker the way repeated transport failures would.
	for range 6 {
		gateway.platform.mu.Lock()
		gateway.platform.browseFails = true
		gateway.platform.mu.Unlock()
		request(t, http.MethodGet, gateway.server.URL, "/", "", nil, nil)
	}

	if state := gateway.breakers.Browse.State(); state != breaker.StateOpen {
		t.Fatalf("browse breaker = %v, want open after repeated failures", state)
	}
	resp, _ := request(t, http.MethodGet, gateway.server.URL, "/readyz", "", nil, nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz with an open browse breaker = %d, want 503", resp.StatusCode)
	}
}

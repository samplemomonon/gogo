// Package partials_test covers the storefront fragments' rendered contract: the
// product card's name, availability and price presentation, the grid's two
// distinct empty states, the crawlable pagination links (page + search term),
// and the escaping of platform-supplied product names. The mechanics of paging
// (which pages exist, cache keys, fragment negotiation) are asserted in
// internal/shared/web/pagekit and internal/features/home/ui; this file pins what
// the templates themselves render.
package partials_test

import (
	"bytes"
	"context"
	"html"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"gogo/internal/features/home/ui/application/results"
	"gogo/internal/features/home/ui/views/partials"
	"gogo/internal/shared/web/pagekit"
)

func render(t *testing.T, component templ.Component) string {
	t.Helper()

	var buf bytes.Buffer
	if err := component.Render(context.Background(), &buf); err != nil {
		t.Fatalf("component.Render: %v", err)
	}
	return buf.String()
}

// TestProductCardRendersNameAvailabilityAndPrice pins the card's three facts:
// the name, the availability badge (a positive count in words), and the price
// pair — the "Rp" currency prefix beside the thousands-grouped figure. The card
// is the storefront's only product surface, so this is what a shopper reads.
func TestProductCardRendersNameAvailabilityAndPrice(t *testing.T) {
	t.Parallel()

	body := render(t, partials.ProductCard(&results.Product{
		ID:    "7",
		Name:  "Kopi Susu Gula Aren",
		Price: 12999000,
		Stock: 3,
	}))

	for _, want := range []string{
		"Kopi Susu Gula Aren",
		"3 tersedia",
		"Rp",
		"12.999.000",
		`href="/catalog/7"`,
		"Lihat detail",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("product card missing %q", want)
		}
	}
}

// TestProductCardReportsOutOfStock pins the zero-stock rule: a sold-out card
// says so in words, not as a "0 tersedia" count that reads like a quantity.
func TestProductCardReportsOutOfStock(t *testing.T) {
	t.Parallel()

	body := render(t, partials.ProductCard(&results.Product{ID: "7", Name: "Kopi", Stock: 0}))

	if !strings.Contains(body, "Stok habis") {
		t.Error("a sold-out product should say so")
	}
	if strings.Contains(body, "0 tersedia") {
		t.Error("a sold-out product rendered a zero count as if it were available")
	}
}

// TestProductCardEscapesHostileNames pins the escaping boundary: a product name
// is data from the platform, and the card interpolates it as text. Markup in a
// name must not become markup in the document.
func TestProductCardEscapesHostileNames(t *testing.T) {
	t.Parallel()

	body := render(t, partials.ProductCard(&results.Product{
		ID:    "1",
		Name:  `"><script>alert(1)</script>`,
		Price: 1000,
		Stock: 1,
	}))

	if strings.Contains(body, "<script") {
		t.Error("a hostile product name produced executable markup")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the escaped name should still be visible as text")
	}
}

// TestProductListEmptyStatesAreDistinct pins the grid's two empty states: an
// empty catalogue offers the first-product action, while an empty search
// result names the term and offers to clear the filter. The two situations
// must not share one message.
func TestProductListEmptyStatesAreDistinct(t *testing.T) {
	t.Parallel()

	t.Run("empty catalogue", func(t *testing.T) {
		t.Parallel()

		body := render(t, partials.ProductList(nil, ""))
		if !strings.Contains(body, "Belum ada produk") {
			t.Error("an empty catalogue should say it is empty")
		}
		if !strings.Contains(body, "Tambah produk pertama") || !strings.Contains(body, `href="/seller/produk"`) {
			t.Error("an empty catalogue should offer the first-product action")
		}
		if strings.Contains(body, "Tidak ada produk yang cocok") {
			t.Error("an empty catalogue rendered the empty-search message")
		}
	})

	t.Run("empty search result", func(t *testing.T) {
		t.Parallel()

		body := render(t, partials.ProductList(nil, "kopi"))
		if !strings.Contains(body, "Tidak ada produk yang cocok dengan “kopi”") {
			t.Error("an empty search should name the term it could not match")
		}
		if !strings.Contains(body, "Bersihkan pencarian") || !strings.Contains(body, `href="/"`) {
			t.Error("an empty search should offer to clear the filter")
		}
		if strings.Contains(body, "Belum ada produk") {
			t.Error("an empty search rendered the empty-catalogue message")
		}
	})
}

// TestProductListIsALiveStatusRegion pins the fragment's accessibility
// declaration: pagination swaps this grid in place, so it is a polite live
// region and carries the id the swap targets.
func TestProductListIsALiveStatusRegion(t *testing.T) {
	t.Parallel()

	body := render(t, partials.ProductList(nil, ""))
	for _, want := range []string{`id="product-list"`, `role="status"`, `aria-live="polite"`} {
		if !strings.Contains(body, want) {
			t.Errorf("product grid missing %q", want)
		}
	}
}

// TestProductListRendersCards pins the non-empty path: every product becomes a
// card, and the empty state disappears.
func TestProductListRendersCards(t *testing.T) {
	t.Parallel()

	body := render(t, partials.ProductList([]*results.Product{
		{ID: "1", Name: "Kopi Susu", Price: 1000, Stock: 2},
		{ID: "2", Name: "Teh Manis", Price: 2000, Stock: 1},
	}, ""))

	for _, want := range []string{"Kopi Susu", "Teh Manis", `href="/catalog/1"`, `href="/catalog/2"`} {
		if !strings.Contains(body, want) {
			t.Errorf("product grid missing %q", want)
		}
	}
	if strings.Contains(body, "Belum ada produk") {
		t.Error("a non-empty grid rendered the empty state")
	}
}

// TestPaginationNavCarriesPageAndTerm pins the crawlable paging contract: every
// numbered and step control is a real href carrying ?page=N, and a search keeps
// its term in the link so paging stays inside the filter. The window is bounded
// around the current page.
func TestPaginationNavCarriesPageAndTerm(t *testing.T) {
	t.Parallel()

	body := html.UnescapeString(render(t, partials.PaginationNav(pagekit.Pagination{
		Page: 2, Count: 5, Total: 100, HasPrev: true, HasNext: true, Term: "kopi susu",
	})))

	for _, want := range []string{
		`aria-label="Halaman produk"`,
		`rel="prev"`,
		`rel="next"`,
		`aria-current="page"`,
		"Sebelumnya",
		"Berikutnya",
		`href="/?page=1&q=kopi+susu"`,
		`href="/?page=3&q=kopi+susu"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pagination missing %q", want)
		}
	}
	// The current page is a span, not a link: ui.Pagination deliberately keeps
	// the active page from linking to itself, so page 2 appears numbered but
	// without an href.
	if !strings.Contains(body, `>2</span>`) {
		t.Error("the current page should render as a non-navigating element")
	}
	if strings.Contains(body, `href="/?page=2`) {
		t.Error("the current page should not link to itself")
	}
	if strings.Contains(body, "page=5") {
		t.Error("the page window should be bounded around the current page")
	}
}

// TestPaginationNavOmitsTheTermWhenBrowsing pins the unfiltered listing's links:
// no empty q parameter is invented, so the URL a crawler follows is the clean
// /?page=N form.
func TestPaginationNavOmitsTheTermWhenBrowsing(t *testing.T) {
	t.Parallel()

	body := html.UnescapeString(render(t, partials.PaginationNav(pagekit.Pagination{
		Page: 2, Count: 3, Total: 60, HasPrev: true, HasNext: true,
	})))

	if !strings.Contains(body, `href="/?page=1"`) || !strings.Contains(body, `href="/?page=3"`) {
		t.Error("browsing should link plain ?page=N URLs")
	}
	if strings.Contains(body, "q=") {
		t.Error("an unfiltered listing invented a search parameter")
	}
}

// TestProductListingRendersPaginationOnlyWhenPaged pins the composition rule:
// the listing region shows controls only when the metadata says there is
// somewhere to go, so a single-page listing has no dead controls.
func TestProductListingRendersPaginationOnlyWhenPaged(t *testing.T) {
	t.Parallel()

	single := render(t, partials.ProductListing([]*results.Product{
		{ID: "1", Name: "Kopi", Price: 1000, Stock: 1},
	}, pagekit.Pagination{Page: 1, Count: 1}))
	if !strings.Contains(single, `id="product-list"`) {
		t.Error("the listing region should always render the grid")
	}
	if strings.Contains(single, `aria-label="Halaman produk"`) {
		t.Error("a single-page listing rendered pagination controls")
	}

	paged := render(t, partials.ProductListing([]*results.Product{
		{ID: "1", Name: "Kopi", Price: 1000, Stock: 1},
	}, pagekit.Pagination{Page: 1, Count: 2, HasNext: true}))
	if !strings.Contains(paged, `aria-label="Halaman produk"`) || !strings.Contains(paged, `href="/?page=2"`) {
		t.Error("a multi-page listing should render its navigation")
	}
}

// TestProductListingPassesTheTermToTheEmptyState pins the wiring from the
// pagination metadata to the grid: an empty filtered page must show the
// search-specific empty state, which is only possible if the region forwards
// nav.Term to the grid.
func TestProductListingPassesTheTermToTheEmptyState(t *testing.T) {
	t.Parallel()

	body := render(t, partials.ProductListing(nil, pagekit.Pagination{Page: 1, Term: "kopi"}))

	if !strings.Contains(body, "Tidak ada produk yang cocok dengan “kopi”") {
		t.Error("the listing region dropped the active term before rendering the empty state")
	}
}

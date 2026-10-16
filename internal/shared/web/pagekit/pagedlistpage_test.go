package pagekit

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"

	"gogo/internal/shared/web/render"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPaginationFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		total, page, limit int
		wantCount          int
		wantPrev           bool
		wantNext           bool
	}{
		{"empty listing", 0, 1, 20, 0, false, false},
		{"single page", 5, 1, 20, 1, false, false},
		{"exact boundary", 20, 2, 20, 1, true, false},
		{"one row over", 21, 2, 20, 2, true, false},
		{"middle page", 100, 3, 20, 5, true, true},
		{"first of many", 100, 1, 20, 5, false, true},
		{"last of many", 100, 5, 20, 5, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paginationFrom(tt.total, tt.page, tt.limit, "")
			if got.Count != tt.wantCount {
				t.Errorf("Count = %d, want %d", got.Count, tt.wantCount)
			}
			if got.HasPrev != tt.wantPrev {
				t.Errorf("HasPrev = %v, want %v", got.HasPrev, tt.wantPrev)
			}
			if got.HasNext != tt.wantNext {
				t.Errorf("HasNext = %v, want %v", got.HasNext, tt.wantNext)
			}
			if got.Total != tt.total || got.Page != tt.page {
				t.Errorf("Total/Page = %d/%d, want %d/%d", got.Total, got.Page, tt.total, tt.page)
			}
		})
	}
}

func TestPagesWindow(t *testing.T) {
	t.Parallel()

	// Current page in the middle: a bounded window around it.
	nav := Pagination{Page: 3, Count: 10}
	if got := nav.Pages(5, 2); !equalInts(got, []int{1, 2, 3, 4, 5}) {
		t.Errorf("Pages(3 of 10) = %v, want [1 2 3 4 5]", got)
	}

	// Near the end: clamped so the window does not exceed Count.
	nav = Pagination{Page: 9, Count: 10}
	if got := nav.Pages(5, 2); !equalInts(got, []int{7, 8, 9, 10}) {
		t.Errorf("Pages(9 of 10) = %v, want [7 8 9 10]", got)
	}

	// Single page: no window to render.
	if got := (Pagination{Page: 1, Count: 1}).Pages(5, 2); len(got) != 0 {
		t.Errorf("Pages(1 of 1) = %v, want empty", got)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPageFromRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want int
	}{
		{"", 1},
		{"?page=1", 1},
		{"?page=3", 3},
		{"?page=0", 1},
		{"?page=-2", 1},
		{"?page=abc", 1},
		{"?page=12&other=1", 12},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/"+tt.raw, nil)
		if got := pageFromRequest(req); got != tt.want {
			t.Errorf("pageFromRequest(%q) = %d, want %d", tt.raw, got, tt.want)
		}
	}
}

func TestPagedKeyIsPerPageAndTerm(t *testing.T) {
	t.Parallel()

	page := &PagedListPage[int]{IslandName: "home.product-grid"}
	if a, b := page.pagedKey(PagedQuery{Page: 1}), page.pagedKey(PagedQuery{Page: 2}); a == b {
		t.Errorf("page 1 and page 2 share the cache key %q", a)
	}
	if want := "home.product-grid:p3"; page.pagedKey(PagedQuery{Page: 3}) != want {
		t.Errorf("pagedKey(page 3) = %q, want %q", page.pagedKey(PagedQuery{Page: 3}), want)
	}
	// The same page under different search terms must not collide, so a
	// filtered result can never be served to an unfiltered request or vice
	// versa.
	k1 := page.pagedKey(PagedQuery{Page: 1, Term: "kopi"})
	k2 := page.pagedKey(PagedQuery{Page: 1, Term: "teh"})
	k0 := page.pagedKey(PagedQuery{Page: 1})
	if k1 == k0 || k2 == k0 || k1 == k2 {
		t.Errorf("term-scoped keys must differ: %q / %q / %q", k0, k1, k2)
	}
}

// TestNewPagedListPageRejectsUnownedIslandName pins the eager half of the
// island-ownership rule (docs/DECISION_UI_OWNERSHIP.md §5): the constructor
// fails a bad key at wiring, so an unowned name never reaches a request.
func TestNewPagedListPageRejectsUnownedIslandName(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewPagedListPage with an unowned island name must panic")
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, "pagekit: island name") {
			t.Errorf("panic = %q, want the pagekit: island name error", msg)
		}
	}()

	NewPagedListPage(
		"product-grid",
		render.RenderingContract{Public: true},
		"q",
		func(context.Context, PagedQuery) ([]string, int, string, error) { return nil, 0, "", nil },
		func([]string, Pagination) templ.Component { return nil },
		func(inner templ.Component, _ []string, _ Pagination) templ.Component { return inner },
		"failed",
		20,
		render.NewComposer(nil),
		discardLogger(),
	)
}

// rawBody returns a body renderer that counts its renders: the returned
// component increments renders each time it is rendered. Because the ISR
// composer caches rendered HTML (not the data fetch), a cache hit is
// observable as "the render counter did not advance".
func rawBody(renders *int) func(items []string, nav Pagination) templ.Component {
	return func(items []string, _ Pagination) templ.Component {
		var b strings.Builder
		for _, it := range items {
			b.WriteString(it)
			b.WriteString(",")
		}
		body := b.String()
		return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			*renders++
			_, err := io.WriteString(w, body)
			return err
		})
	}
}

func TestPagedListPageCachesPerPage(t *testing.T) {
	composer := render.NewComposer(render.NewTTLCache())
	renders := 0

	page := NewPagedListPage(
		"home.product-grid",
		render.RenderingContract{Public: true, CacheTTL: time.Minute},
		"q",
		func(ctx context.Context, query PagedQuery) ([]string, int, string, error) {
			all := make([]string, 0, 30)
			for i := 0; i < 30; i++ {
				all = append(all, string(rune('a'+i%26)))
			}
			start := (query.Page - 1) * query.Limit
			end := start + query.Limit
			if end > len(all) {
				end = len(all)
			}
			return all[start:end], len(all), "", nil
		},
		rawBody(&renders),
		func(inner templ.Component, _ []string, _ Pagination) templ.Component { return inner },
		"failed",
		20,
		composer,
		discardLogger(),
	)

	serve := func(n int) {
		req := httptest.NewRequest(http.MethodGet, "/?page="+itoa(n), nil)
		rec := httptest.NewRecorder()
		page.Handle(rec, req)
	}

	// Distinct pages render fresh; re-serving the same page must hit its own
	// per-page cache entry and skip the render.
	serve(1)
	serve(2)
	if renders != 2 {
		t.Fatalf("renders = %d after distinct pages, want 2", renders)
	}

	serve(1)
	if renders != 2 {
		t.Errorf("re-serving page 1 re-rendered (renders = %d, want 2): per-page cache missed", renders)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

func TestInvalidatePrefixOnEventsClearsEveryPage(t *testing.T) {
	composer := render.NewComposer(render.NewTTLCache())
	renders := 0

	page := NewPagedListPage(
		"home.product-grid",
		render.RenderingContract{Public: true, CacheTTL: time.Minute},
		"q",
		func(ctx context.Context, query PagedQuery) ([]string, int, string, error) {
			return []string{"x"}, 30, "", nil
		},
		rawBody(&renders),
		func(inner templ.Component, _ []string, _ Pagination) templ.Component { return inner },
		"failed",
		20,
		composer,
		discardLogger(),
	)

	serve := func(n int) {
		req := httptest.NewRequest(http.MethodGet, "/?page="+itoa(n), nil)
		rec := httptest.NewRecorder()
		page.Handle(rec, req)
	}

	serve(1)
	serve(2)
	renders = 0

	// Re-serving both pages must hit their per-page cache entries (no render).
	serve(1)
	serve(2)
	if renders != 0 {
		t.Fatalf("before invalidation: re-serves re-rendered (renders = %d, want 0)", renders)
	}

	// Invalidate the whole grid prefix; both cached pages must be dropped, so
	// the next serve of each page re-renders.
	composer.InvalidatePrefix("home.product-grid")
	serve(1)
	serve(2)
	if renders != 2 {
		t.Errorf("after InvalidatePrefix, renders = %d, want 2 (every page re-rendered)", renders)
	}
}

// TestPagedListPageHTMXCacheHitSkipsFetch pins the fragment fast path: an
// HTMX request whose island is already cached is answered from the cache
// without a data fetch on the backing service — the heavy-read win — while a
// miss (or a full-page request, which renders the shell from live data) still
// fetches.
func TestPagedListPageHTMXCacheHitSkipsFetch(t *testing.T) {
	composer := render.NewComposer(render.NewTTLCache())
	renders := 0
	var fetches int

	page := NewPagedListPage(
		"home.product-grid",
		render.RenderingContract{Public: true, CacheTTL: time.Minute},
		"q",
		func(ctx context.Context, query PagedQuery) ([]string, int, string, error) {
			fetches++
			return []string{"x"}, 30, "", nil
		},
		rawBody(&renders),
		func(inner templ.Component, _ []string, _ Pagination) templ.Component { return inner },
		"failed",
		20,
		composer,
		discardLogger(),
	)

	// A full-page request populates the cache for page 1.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	page.Handle(rec, req)
	if fetches != 1 {
		t.Fatalf("full-page prime fetches = %d, want 1", fetches)
	}

	// An HTMX fragment request now hits the cache and skips the fetch.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	page.Handle(rec, req)
	if fetches != 1 {
		t.Errorf("cached HTMX fragment re-fetched (fetches = %d, want 1): fast path missed", fetches)
	}
	if !strings.Contains(rec.Body.String(), "x,") {
		t.Error("cached fragment should still contain the island HTML")
	}

	// After prefix invalidation the fragment must fetch again.
	composer.InvalidatePrefix("home.product-grid")
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	page.Handle(rec, req)
	if fetches != 2 {
		t.Errorf("after invalidation, fragment fetches = %d, want 2", fetches)
	}
}

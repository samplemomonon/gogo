package pagekit

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/a-h/templ"

	"gogo/internal/shared/web/render"
)

// Pagination is the metadata a paged listing renders navigation from. It is
// computed from one paged fetch so a page renders controls without a second
// round trip.
type Pagination struct {
	// Page is the 1-based current page.
	Page int
	// Count is the total number of pages (0 when there is nothing to page).
	Count int
	// Total is the number of matching items before paging.
	Total int
	// HasPrev reports whether a previous page exists.
	HasPrev bool
	// HasNext reports whether a following page exists.
	HasNext bool

	// Term is the active search term this page was rendered for. The page's
	// chrome and nav use it to reflect and carry the current filter.
	Term string
}

// Pages returns the window of page numbers to render around the current
// page: a bounded neighbourhood so a very long listing never renders a long
// unbroken page-number bar. The window is clamped to [1, Count]; when there
// is nothing to page it is empty.
func (p Pagination) Pages(around, perSide int) []int {
	if p.Count < 2 {
		return nil
	}
	lo := p.Page - perSide
	hi := p.Page + perSide
	if hi > p.Count {
		hi = p.Count
	}
	if lo < 1 {
		lo = 1
	}
	pages := make([]int, 0, hi-lo+1)
	for n := lo; n <= hi; n++ {
		pages = append(pages, n)
	}
	return pages
}

// PagedQuery is the concrete listing request a paged handler passes to the
// page's Fetch: the 1-based page number, the page limit and the search term
// ("" for the unfiltered listing). It pairs URL state (page, search) with the
// page's fixed limit.
type PagedQuery struct {
	Page  int
	Limit int
	// Term is the search/filter term parsed from the request's SearchParam.
	Term string
}

// PagedListPage is the reusable handler for a page whose body is a paginated,
// crawlable island (one cache entry per page + search term) — the storefront
// shape. It owns the mechanics every such page repeats: parsing ?page= and a
// search term, fetching one page, computing pagination metadata, keying and
// caching each page's island under a page-scoped cache key, and negotiating
// full page vs HTMX fragment. Each page supplies only its capability, read
// model, views and navigation markup.
type PagedListPage[T any] struct {
	// IslandName is the prefix of this island's cache keys; each page N (and
	// search term) is cached under IslandName + ":" + term + ":p" + N. It is
	// also the prefix callers pass to InvalidatePrefixOnEvents to clear every
	// page on a mutation.
	IslandName string

	// Contract declares the island's rendering strategy.
	Contract render.RenderingContract

	// SearchParam is the name of the query parameter carrying the search term
	// (e.g. "q"). When empty, no term is read and Fetch always receives "".
	SearchParam string

	// Fetch loads one page for a PagedQuery (page, limit, optional search
	// term) and returns the items, the total matching count (before paging)
	// and the keyset cursor keying the next page (empty on the last page).
	Fetch func(ctx context.Context, query PagedQuery) (items []T, total int, nextCursor string, err error)

	// Body renders the loaded page: the collection plus its navigation.
	// It is the island's HTML, used for both the full page and the HTMX
	// fragment.
	Body func(items []T, nav Pagination) templ.Component

	// Layout wraps the island in the page shell for full-page responses.
	Layout func(inner templ.Component, items []T, nav Pagination) templ.Component

	// ErrorMessage is the client-facing message when Fetch fails.
	ErrorMessage string

	// ClassifyError maps a Fetch error to the HTTP status the page answers with.
	// Nil means "always 500".
	//
	// It exists because a paged listing whose backing service is unreachable is not a
	// server fault: the request was fine and the answer is "try again". Reporting 500
	// there tells a monitoring system the gateway is broken, tells a client the page
	// will never work, and — worst — makes an empty catalogue rendered by an outage
	// indistinguishable from a real empty one. A page that can tell the two apart
	// supplies this; one that cannot gets the 500 default.
	ClassifyError func(err error) int

	// PageLimit is the number of items per page. Non-positive values fall
	// back to DefaultPageLimit.
	PageLimit int

	// Streaming delivers full-page responses progressively (see
	// render.Page.Streaming). HTMX fragment responses stay buffered: their
	// swap is atomic by contract. Off by default; a page opts in.
	Streaming bool

	Composer *render.Composer
	Logger   *slog.Logger
}

// DefaultPageLimit is the items-per-page used when PagedListPage.PageLimit is
// non-positive.
const DefaultPageLimit = 20

// NewPagedListPage builds a PagedListPage. It panics on a nil Composer or
// Logger, and on an island name without an owner namespace — all of them
// composition-root wiring mistakes, failed at wiring rather than silently at
// the first request.
func NewPagedListPage[T any](
	islandName string,
	contract render.RenderingContract,
	searchParam string,
	fetch func(ctx context.Context, query PagedQuery) (items []T, total int, nextCursor string, err error),
	body func(items []T, nav Pagination) templ.Component,
	layout func(inner templ.Component, items []T, nav Pagination) templ.Component,
	errorMessage string,
	pageLimit int,
	composer *render.Composer,
	logger *slog.Logger,
) *PagedListPage[T] {
	if err := render.ValidateIslandName(islandName); err != nil {
		panic("pagekit: " + err.Error())
	}
	if composer == nil {
		panic("pagekit: PagedListPage requires a composer")
	}
	if logger == nil {
		panic("pagekit: PagedListPage requires a logger")
	}
	if pageLimit <= 0 {
		pageLimit = DefaultPageLimit
	}
	return &PagedListPage[T]{
		IslandName:   islandName,
		Contract:     contract,
		SearchParam:  searchParam,
		Fetch:        fetch,
		Body:         body,
		Layout:       layout,
		ErrorMessage: errorMessage,
		PageLimit:    pageLimit,
		Composer:     composer,
		Logger:       logger,
		// WithErrorStatus is applied after construction so the long positional
		// constructor above stays as it was; NewPagedListPageWithStatus is the readable
		// entry point for a page that can tell an outage from a fault.
	}
}

// WithErrorStatus installs the error classifier and returns the page, so a caller can
// chain it onto the constructor:
//
//	pagekit.NewPagedListPage(...).WithErrorStatus(homelist.classifyFetchError)
func (h *PagedListPage[T]) WithErrorStatus(classify func(err error) int) *PagedListPage[T] {
	h.ClassifyError = classify
	return h
}

// WithStreaming opts full-page responses into progressive delivery (see
// render.Page.Streaming), so the page shell reaches the client before the
// listing's fetch completes on a cache miss. HTMX fragment responses remain
// buffered regardless: their swap is atomic by htmx contract.
func (h *PagedListPage[T]) WithStreaming() *PagedListPage[T] {
	h.Streaming = true
	return h
}

// statusFor answers the HTTP status for a failed fetch: whatever the page's classifier
// says, defaulting to 500.
func (h *PagedListPage[T]) statusFor(err error) int {
	if h.ClassifyError != nil {
		return h.ClassifyError(err)
	}
	return http.StatusInternalServerError
}

// Handle serves one page of the listing: a full document for plain requests
// and the bare island fragment for HTMX requests. Each page and search term
// is its own crawlable URL (/?page=N&q=term), cacheable independently of the
// others.
//
// An HTMX fragment request whose island is already cached is answered
// straight from the cache — no data fetch on the backing service — because
// the fragment needs nothing else. Full-page requests still fetch (the page
// shell renders the count and pagination from live data).
func (h *PagedListPage[T]) Handle(w http.ResponseWriter, r *http.Request) {
	query := PagedQuery{
		Page:  pageFromRequest(r),
		Limit: h.PageLimit,
		Term:  termFromRequest(r, h.SearchParam),
	}

	rctx := render.ContextFromRequest(r)
	island := render.Island(h.pagedKey(query), nil, h.Contract)

	// Fragment fast path: a cached HTMX swap needs no data, so skip the fetch
	// entirely. It bypasses the composer, so it sets the same negotiation
	// headers the composer would: the same URL answers a document or a
	// fragment, and a shared cache must know that (render.SetVary).
	if rctx.IsHTMX {
		if data, ok := h.Composer.CachedIsland(r.Context(), island); ok {
			render.SetVary(w)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
	}

	items, total, _, err := h.Fetch(r.Context(), query)
	if err != nil {
		h.Logger.Error("paged list page fetch failed",
			"island", h.IslandName, "page", query.Page, "term", query.Term, "error", err)
		render.Error(w, h.statusFor(err), h.ErrorMessage)
		return
	}

	nav := paginationFrom(total, query.Page, query.Limit, query.Term)

	island.Body = h.Body(items, nav)
	pageDoc := &render.Page{
		Layout:     func(inner templ.Component) templ.Component { return h.Layout(inner, items, nav) },
		Components: []*render.Component{island},
		Streaming:  h.Streaming,
	}

	if err := h.Composer.RenderNavigation(r.Context(), w, pageDoc, rctx); err != nil {
		h.Logger.Error("render paged list page",
			"island", h.IslandName, "page", query.Page, "term", query.Term, "error", err)
	}
}

// pagedKey returns the cache key of this island: one entry per page and
// search term, so cached HTML never leaks across filters.
func (h *PagedListPage[T]) pagedKey(query PagedQuery) string {
	term := ""
	if query.Term != "" {
		term = "t" + fmt.Sprintf("%x", hashString(query.Term)) + ":"
	}
	return fmt.Sprintf("%s:%sp%d", h.IslandName, term, query.Page)
}

// pageFromRequest parses ?page=, defaulting to 1 and clamping bad values.
func pageFromRequest(r *http.Request) int {
	raw := r.URL.Query().Get("page")
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// termFromRequest parses the search term from param. An empty param name
// yields no term.
func termFromRequest(r *http.Request, param string) string {
	if param == "" {
		return ""
	}
	return r.URL.Query().Get(param)
}

// hashString returns a stable FNV-1a hash used to namespace cache keys, so the
// term is not embedded verbatim in a cache key.
func hashString(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// paginationFrom computes the navigation metadata for one page of a listing
// whose total is known, carrying the active search term so the page chrome
// and nav can reflect it.
func paginationFrom(total, page, limit int, term string) Pagination {
	count := 0
	if limit > 0 {
		count = (total + limit - 1) / limit
	}
	if count < 1 {
		count = 0
	}
	return Pagination{
		Page:    page,
		Count:   count,
		Total:   total,
		HasPrev: page > 1,
		HasNext: page < count,
		Term:    term,
	}
}

// Package homelist serves the home page's one page: the public product listing.
// It mirrors the seller console's handler layout (a handler package per page/handler
// under handlers/...) so both top-level pages follow the same shape and the same
// ownership rule — only the handler knows the read model and the remote port.
//
// The listing mechanics are the shared pagekit.PagedListPage (a crawlable,
// paginated island with one cache entry per page); this handler only supplies what
// is decisively home's: the remote port, the read model, the island key prefix and
// the views.
package homelist

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"

	"gogo/internal/pages/home/application/ports"
	"gogo/internal/pages/home/application/results"
	"gogo/internal/pages/home/infrastructure/listingclient"
	"gogo/internal/pages/home/islandkeys"
	"gogo/internal/pages/home/views/pages"
	"gogo/internal/pages/home/views/partials"
	"gogo/internal/shared/tenant"
	"gogo/internal/shared/web/pagekit"
	"gogo/internal/shared/web/render"
)

// Handler serves the home page's public, paginated product listing.
type Handler struct {
	page *pagekit.PagedListPage[*results.Product]
}

// NewHandler wires the home listing handler around the remote listing port.
func NewHandler(listing ports.Listing, composer *render.Composer, logger *slog.Logger) *Handler {
	return &Handler{
		page: pagekit.NewPagedListPage(
			islandkeys.HomeGrid,
			render.RenderingContract{
				Public:      true,
				RequiresSEO: true,
				CacheTTL:    30 * time.Second,
			},
			"q",
			func(ctx context.Context, query pagekit.PagedQuery) ([]*results.Product, int, string, error) {
				page, err := listing.ListPage(ctx, ports.Query{
					Term:   query.Term,
					Offset: (query.Page - 1) * query.Limit,
					Limit:  query.Limit,
				})
				if err != nil {
					logger.Error("home listing failed",
						"error", err,
						"tenant", tenant.FromContextOrDefault(ctx),
						"term", query.Term,
					)
					return nil, 0, "", err
				}
				return page.Products, page.Total, page.NextCursor, nil
			},
			partials.ProductListing,
			func(inner templ.Component, _ []*results.Product, nav pagekit.Pagination) templ.Component {
				return pages.Home(inner, nav.Total, nav.Term)
			},
			"Gagal memuat produk.",
			20,
			composer,
			logger,
		).WithErrorStatus(classifyFetchError),
	}
}

// classifyFetchError maps a listing failure to the status the storefront answers with.
//
// A platform outage is a 503, not a 500: the request was well-formed and the honest
// answer is "the catalogue is unreachable, try again". Reporting 500 there would tell a
// client the page is permanently broken and would make an outage indistinguishable from
// a genuinely empty catalogue in the logs. Everything else is a 500, because anything
// the page cannot classify is a bug in it.
func classifyFetchError(err error) int {
	if listingclient.IsUnavailable(err) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// Handle serves the home page. The grid is a paginated revalidated island (ISR)
// owned by this page — one cache entry per page — and products are fetched through
// the remote listing port. The renderer serves the full page for plain requests and
// the bare listing fragment for HTMX requests, which htmx swaps into the listing's
// swap target.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	h.page.Handle(w, r)
}

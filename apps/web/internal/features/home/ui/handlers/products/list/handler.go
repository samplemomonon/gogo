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
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"

	"gogo/internal/features/home/ui/application/ports"
	"gogo/internal/features/home/ui/application/results"
	"gogo/internal/features/home/ui/infrastructure/listingclient"
	"gogo/internal/features/home/ui/islandkeys"
	"gogo/internal/features/home/ui/views/pages"
	"gogo/internal/features/home/ui/views/partials"
	"gogo/internal/shared/tenant"
	"gogo/internal/shared/web/pagekit"
	"gogo/internal/shared/web/render"
)

// BannerSlot is the editorial content slug the storefront's home banner reads
// from the CMS. It is the page's own vocabulary: the slot is declared here, and
// the composition root maps the name onto the editorial reader.
const BannerSlot = "home-banner"

// Handler serves the home page's public, paginated product listing.
type Handler struct {
	page    *pagekit.PagedListPage[*results.Product]
	banners ports.BannerSlot
	logger  *slog.Logger
}

// NewHandler wires the home listing handler around the remote listing port and
// the page's editorial banner slot.
func NewHandler(listing ports.Listing, banners ports.BannerSlot, composer *render.Composer, logger *slog.Logger) *Handler {
	return &Handler{
		banners: banners,
		logger:  logger,
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
		).WithErrorStatus(classifyFetchError).WithStreaming(),
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
//
// The editorial banner is resolved first and rides the request context into the
// document (pages.WithBanner). An empty slot or a failed editorial read renders
// no banner — logged, never fatal — because the storefront must not stop
// serving products just because its editorial slot is unavailable
// (docs/DECISION_CONTENT_SURFACES.md §2).
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if banner, ok := h.readBanner(r.Context()); ok {
		r = r.WithContext(pages.WithBanner(r.Context(), banner))
	}
	h.page.Handle(w, r)
}

// readBanner resolves the page's editorial banner slot; ok reports whether
// there is a banner to render. ErrNoBanner is the expected empty state and is
// not logged; any other failure is logged and degraded to no banner.
func (h *Handler) readBanner(ctx context.Context) (results.Banner, bool) {
	banner, err := h.banners.ReadBanner(ctx, BannerSlot)
	switch {
	case err == nil:
		return banner, true
	case errors.Is(err, ports.ErrNoBanner):
		return results.Banner{}, false
	default:
		h.logger.Warn("home banner unavailable", "slot", BannerSlot, "error", err)
		return results.Banner{}, false
	}
}

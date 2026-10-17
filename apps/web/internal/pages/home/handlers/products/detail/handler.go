// Package detail serves the storefront's server-rendered product detail page.
package detail

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/home/application/ports"
	"gogo/internal/pages/home/islandkeys"
	"gogo/internal/pages/home/views/pages"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/tenant"
	"gogo/internal/shared/web/render"
)

// Handler serves one product's detail page.
type Handler struct {
	listing  ports.Listing
	composer *render.Composer
	logger   *slog.Logger
}

// NewHandler wires the detail handler around the remote listing port and the
// page's composer.
func NewHandler(listing ports.Listing, composer *render.Composer, logger *slog.Logger) *Handler {
	return &Handler{listing: listing, composer: composer, logger: logger}
}

// Handle serves /catalog/{id}. The page is composed like every other page:
// the product region is a public, crawlable island (one cache entry per
// product id, revalidated under a short TTL) and the shell wraps it.
//
// The handler still fetches the product on every request — the shell's title
// comes from live data — but composition runs through the composer, so the
// response is buffered atomically (a render failure leaves no half-written
// page), carries the negotiation and timing headers, and the island is
// reported per island like any other.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	numericID, parseErr := strconv.ParseInt(id, 10, 64)
	if parseErr != nil || numericID <= 0 {
		http.NotFound(w, r)
		return
	}
	product, err := h.listing.GetProduct(r.Context(), id)
	if errors.Is(err, productconsumer.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("home product detail failed", "error", err, "tenant", tenant.FromContextOrDefault(r.Context()), "product_id", id)
		if errors.Is(err, productconsumer.ErrProductPlatformUnavailable) {
			render.Error(w, http.StatusServiceUnavailable, "Katalog sedang tidak tersedia. Coba lagi sebentar.")
			return
		}
		render.Error(w, http.StatusInternalServerError, "Gagal memuat produk.")
		return
	}

	island := render.Island(
		islandkeys.ProductDetail+":"+id,
		pages.ProductDetailContent(product),
		render.RenderingContract{
			Public:      true,
			RequiresSEO: true,
			CacheTTL:    30 * time.Second,
		},
	)

	page := &render.Page{
		Layout: func(content templ.Component) templ.Component {
			return pages.ProductDetail(product, content)
		},
		Components: []*render.Component{island},
	}

	if err := h.composer.RenderPage(r.Context(), w, page, render.ContextFromRequest(r)); err != nil {
		h.logger.Error("render home product detail", "error", err, "product_id", id)
		// Composition failed before anything was written (a buffered
		// response is atomic), so the client still gets an honest error
		// instead of a truncated page.
		render.Error(w, http.StatusInternalServerError, "Gagal memuat produk.")
	}
}

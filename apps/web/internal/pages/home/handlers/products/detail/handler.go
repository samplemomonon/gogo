// Package detail serves the storefront's server-rendered product detail page.
package detail

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/home/application/ports"
	"gogo/internal/pages/home/views/pages"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/tenant"
)

type Handler struct {
	listing ports.Listing
	logger  *slog.Logger
}

func NewHandler(listing ports.Listing, logger *slog.Logger) *Handler {
	return &Handler{listing: listing, logger: logger}
}

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
			http.Error(w, "Katalog sedang tidak tersedia. Coba lagi sebentar.", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "Gagal memuat produk.", http.StatusInternalServerError)
		return
	}
	var body bytes.Buffer
	if err := pages.ProductDetail(product).Render(r.Context(), &body); err != nil {
		h.logger.Error("render home product detail", "error", err, "product_id", id)
		http.Error(w, "Gagal memuat produk.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = body.WriteTo(w)
}

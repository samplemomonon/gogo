// Package handlers serves the seller console: the product, category, supplier and
// stock surfaces of the unified gateway. Every read renders a full page; every write
// posts and redirects (Post/Redirect/Get), so the console works without JavaScript.
package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"gogo/internal/pages/seller/application/ports"
	"gogo/internal/pages/seller/application/results"
	"gogo/internal/pages/seller/httperr"
	"gogo/internal/pages/seller/views/pages"
	"gogo/internal/pages/seller/views/partials"
	"gogo/internal/shared/web/render"
)

// Handler serves the seller console's routes.
type Handler struct {
	catalog   ports.ProductCatalog
	inventory ports.Inventory
	sales     ports.Sales
	logger    *slog.Logger
}

// stockOf returns the recorded quantity for one product and whether a stock row
// exists at all. A product with no stock row is not zero stock — it has never been
// stocked — and the console must be able to say so rather than render a confident 0.
//
// The join is the console's own: products come from the product platform, quantities
// from gogo's inventory module, which is the sole writer of the sellable quantity.
// Neither side knows about the other, so the console pairs them by id here. The
// quantity shown is the inventory module's — not the platform's mirrored stock
// column — because that module is the authority for what may be sold.
func stockOf(stocks []*results.Stock, productID string) (int, bool) {
	for _, s := range stocks {
		if s.ProductID == productID {
			return s.Quantity, true
		}
	}
	return 0, false
}

// listStocks fetches the recorded quantity for each of the given products, as one
// batched lookup rather than one call per product.
func (h *Handler) listStocks(ctx context.Context, products []*results.Product) ([]*results.Stock, error) {
	ids := make([]string, 0, len(products))
	for _, p := range products {
		ids = append(ids, p.ID)
	}
	return h.inventory.ListStocks(ctx, ids)
}

func NewHandler(catalog ports.ProductCatalog, inventory ports.Inventory, sales ports.Sales, logger *slog.Logger) *Handler {
	return &Handler{catalog: catalog, inventory: inventory, sales: sales, logger: logger}
}

// Dashboard renders the console overview.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	products, err := h.catalog.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	categories, err := h.catalog.ListCategories(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	stocks, err := h.listStocks(r.Context(), products)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// "Out of stock" counts sellable products whose recorded quantity is zero — a
	// product with no stock row counts, because never-stocked is not stocked.
	outOfStock := 0
	for _, product := range products {
		if product.Status != "active" {
			continue
		}
		quantity, _ := stockOf(stocks, product.ID)
		if quantity <= 0 {
			outOfStock++
		}
	}

	h.page(w, r, partials.DashboardSection(products, categories, outOfStock))
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request, content templ.Component) {
	if err := render.HTML(w, r, pages.Seller(content)); err != nil {
		h.logger.Error("render seller page", "error", err)
	}
}

// fail answers a request whose data could not be loaded. The status comes from
// the console's own error table, so an unreachable dependency is reported as the
// 503 it is instead of a generic 500, and the response never carries the
// upstream's error text.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, message := httperr.Map(err)
	h.logger.Error("seller console error", "error", err, "path", r.URL.Path, "status", status)
	render.Error(w, status, message)
}

// notice returns the message a failed write shows in its re-rendered form, and
// records an outage as an error. A failed write re-renders at 200 so the
// cashier's input stays on screen, so only the fixed message travels with it.
func (h *Handler) notice(r *http.Request, err error) string {
	if httperr.Unavailable(err) {
		h.logger.Error("seller console dependency unavailable", "error", err, "path", r.URL.Path)
	}
	return httperr.Message(err)
}

func redirect(w http.ResponseWriter, r *http.Request, path string) {
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func defaultKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "adjustment"
	}
	return kind
}

func parseInt(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	return strconv.Atoi(strings.TrimSpace(value))
}

func parseInt64(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}

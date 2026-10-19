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

	"gogo/internal/features/sales/ui/application/ports"
	"gogo/internal/features/sales/ui/application/results"
	"gogo/internal/features/sales/ui/httperr"
	"gogo/internal/features/sales/ui/views/pages"
	"gogo/internal/features/sales/ui/views/partials"
	"gogo/internal/shared/authn"
	"gogo/internal/shared/web/render"
	"gogo/internal/shared/web/session"
)

// Handler serves the seller console's routes.
type Handler struct {
	catalog   ports.ProductCatalog
	inventory ports.Inventory
	sales     ports.Sales
	employees ports.EmployeeSessions
	logger    *slog.Logger
	created   *createdProductFlashes
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

func NewHandler(catalog ports.ProductCatalog, inventory ports.Inventory, sales ports.Sales, employees ports.EmployeeSessions, logger *slog.Logger) *Handler {
	return &Handler{
		catalog: catalog, inventory: inventory, sales: sales, employees: employees, logger: logger,
		created: newCreatedProductFlashes(),
	}
}

type employeeContextKey struct{}

func employeeFromContext(r *http.Request) *results.Employee {
	employee, _ := r.Context().Value(employeeContextKey{}).(*results.Employee)
	return employee
}

// RequireEmployee protects the seller console with the auth service's session
// validation. The browser-supplied cookie is only a session reference; identity
// and actor fields always come from the validated response.
func (h *Handler) RequireEmployee(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.employees == nil {
			h.logger.Error("seller employee session resolver is not configured")
			http.Error(w, "Authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		employee, err := h.employees.Current(r.Context(), session.Read(r))
		if err != nil {
			h.logger.Error("seller session validation failed", "error", err)
			http.Error(w, "Authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		if employee == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), employeeContextKey{}, employee)
		ctx = authn.WithSession(ctx, session.Read(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequirePermission is a route PEP. It asks auth's PDP using the same opaque
// session cookie as the identity check; an outage fails closed and is distinct
// from an explicit deny.
func (h *Handler) RequirePermission(permissionKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, err := h.employees.CheckPermission(r.Context(), session.Read(r), permissionKey)
			if err != nil {
				h.logger.Error("seller permission check failed", "permission", permissionKey, "error", err)
				http.Error(w, "Authorization unavailable", http.StatusServiceUnavailable)
				return
			}
			if !allowed {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// merchantForCreate returns the merchant in which the session employee may
// create products: their own merchant (a penjual) or a merchant whose owner
// granted them product management (a karyawan). It returns nil when the
// employee may create in no merchant.
func (h *Handler) merchantForCreate(r *http.Request) (*results.Merchant, error) {
	merchants, err := h.employees.ListMyMerchants(r.Context(), session.Read(r))
	if err != nil {
		return nil, err
	}
	for i := range merchants {
		allowed, err := h.employees.CanManageProduct(r.Context(), session.Read(r), merchants[i].ID)
		if err != nil {
			return nil, err
		}
		if allowed {
			return &merchants[i], nil
		}
	}
	return nil, nil
}

// productMerchant returns the merchant that owns the product with id, from the
// console's product page. An empty result means the product is unknown or
// unowned, which the caller treats as a forbidden write.
func (h *Handler) productMerchant(ctx context.Context, id string) (string, error) {
	products, err := h.catalog.List(ctx)
	if err != nil {
		return "", err
	}
	for _, product := range products {
		if product.ID == id {
			return product.MerchantID, nil
		}
	}
	return "", nil
}

// requireProductAccess gates a product mutation on the merchant-scoped PDP: the
// session employee must be a member of the merchant that owns the product and,
// unless they own it, hold an explicit merchant-scoped grant. An outage fails
// closed as 503, distinct from an explicit deny (403).
func (h *Handler) requireProductAccess(w http.ResponseWriter, r *http.Request, merchantID string) bool {
	if merchantID == "" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	allowed, err := h.employees.CanManageProduct(r.Context(), session.Read(r), merchantID)
	if err != nil {
		h.logger.Error("merchant product access check failed", "merchant", merchantID, "error", err)
		http.Error(w, "Authorization unavailable", http.StatusServiceUnavailable)
		return false
	}
	if !allowed {
		h.logger.Warn("seller product mutation forbidden", "merchant", merchantID, "path", r.URL.Path)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}
	return true
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

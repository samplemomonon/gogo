package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"gogo/internal/features/sales/ui/views/partials"
)

// Stock renders the stock ledger page.
func (h *Handler) Stock(w http.ResponseWriter, r *http.Request) {
	h.renderStock(w, r, "")
}

// StockAdjust applies an adjustment to one product's stock.
func (h *Handler) StockAdjust(w http.ResponseWriter, r *http.Request) {
	delta, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("delta")))
	if err != nil {
		h.renderStock(w, r, "Jumlah tidak valid")
		return
	}
	employee := employeeFromContext(r)
	if employee == nil {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	_, _, err = h.inventory.AdjustStock(
		r.Context(),
		r.PostFormValue("productId"),
		delta,
		defaultKind(r.PostFormValue("kind")),
		r.PostFormValue("note"),
		employee.ID,
		"",
	)
	if err != nil {
		h.renderStock(w, r, h.notice(r, err))
		return
	}
	redirect(w, r, "/seller/stok")
}

func (h *Handler) renderStock(w http.ResponseWriter, r *http.Request, errMsg string) {
	products, err := h.catalog.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	stocks, err := h.inventory.ListStocks(r.Context(), nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	movements, err := h.inventory.ListMovements(r.Context(), "", 50)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.page(w, r, partials.StockSection(products, stocks, movements, errMsg))
}

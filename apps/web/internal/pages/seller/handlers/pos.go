package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/seller/application/results"
	"gogo/internal/pages/seller/views/partials"
)

// Kasir renders the point-of-sale page.
func (h *Handler) Kasir(w http.ResponseWriter, r *http.Request) {
	products, _, err := h.sellableWithStock(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.page(w, r, partials.KasirSection(products, ""))
}

// Checkout records a sale from the POS form.
//
// Quantities come from the per-product qty_<id> fields; prices come from the product
// platform, never from the form. A form-sent price would let a tampered request
// decide what a sale is worth.
//
// Only active products are sold. The port already returns the whole catalogue, so
// the filter happens here, and a qty posted for a non-active product is ignored
// rather than trusted.
func (h *Handler) Checkout(w http.ResponseWriter, r *http.Request) {
	all, stocks, err := h.sellableWithStock(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	items := make([]*results.TransactionItem, 0, len(all))
	for _, product := range all {
		qty, qtyErr := parseInt(r.PostFormValue("qty_" + product.ID))
		if qtyErr != nil || qty <= 0 {
			continue
		}
		// Guard against overselling what the ledger actually holds. The sales module
		// would also refuse the resulting negative stock via inventory.v1, but failing
		// here keeps the cashier's message specific ("stok tidak mencukupi") instead of
		// surfacing an inventory precondition failure mid-checkout.
		if quantity, _ := stockOf(stocks, product.ID); quantity < qty {
			h.page(w, r, partials.KasirSection(all, "Stok "+product.Name+" tidak cukup"))
			return
		}
		items = append(items, &results.TransactionItem{
			ProductID: product.ID,
			Name:      product.Name,
			Quantity:  qty,
			Price:     product.Price,
		})
	}
	if len(items) == 0 {
		h.page(w, r, partials.KasirSection(all, "Pilih minimal satu produk"))
		return
	}

	paid, _ := parseInt64(r.PostFormValue("paid"))
	_, err = h.sales.Checkout(r.Context(), items, 0, paid, r.PostFormValue("method"), r.PostFormValue("cashier"), "")
	if err != nil {
		h.page(w, r, partials.KasirSection(all, h.notice(r, err)))
		return
	}
	redirect(w, r, "/seller/transaksi")
}

// sellableWithStock returns the active products together with their recorded
// quantities. Both come from remote peers, so it returns the first failure rather
// than rendering a partial till.
func (h *Handler) sellableWithStock(r *http.Request) ([]*results.Product, []*results.Stock, error) {
	products, err := h.catalog.List(r.Context())
	if err != nil {
		return nil, nil, err
	}
	stocks, err := h.listStocks(r.Context(), products)
	if err != nil {
		return nil, nil, err
	}
	return sellable(products), stocks, nil
}

// sellable keeps only the products that may be sold. A draft or archived product
// exists in the catalogue but is not offered at the till.
func sellable(products []*results.Product) []*results.Product {
	out := make([]*results.Product, 0, len(products))
	for _, product := range products {
		if product.Status == "active" {
			out = append(out, product)
		}
	}
	return out
}

// Transactions renders the sales history.
func (h *Handler) Transactions(w http.ResponseWriter, r *http.Request) {
	h.renderTransactions(w, r, "")
}

// TransactionVoid voids a completed sale.
func (h *Handler) TransactionVoid(w http.ResponseWriter, r *http.Request) {
	_, err := h.sales.Void(r.Context(), chi.URLParam(r, "id"), r.PostFormValue("voidedBy"), r.PostFormValue("reason"))
	if err != nil {
		h.renderTransactions(w, r, h.notice(r, err))
		return
	}
	redirect(w, r, "/seller/transaksi")
}

func (h *Handler) renderTransactions(w http.ResponseWriter, r *http.Request, errMsg string) {
	transactions, err := h.sales.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.page(w, r, partials.TransactionsSection(transactions, errMsg))
}

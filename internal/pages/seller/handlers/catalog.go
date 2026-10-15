package handlers

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/seller/views/partials"
)

// Products renders the product management page.
func (h *Handler) Products(w http.ResponseWriter, r *http.Request) {
	h.renderProducts(w, r, "")
}

func (h *Handler) ProductCreate(w http.ResponseWriter, r *http.Request) {
	input, ok := parseProduct(r)
	if !ok {
		h.renderProducts(w, r, msgInvalidForm)
		return
	}
	if _, err := h.catalog.Create(r.Context(), input); err != nil {
		h.renderProducts(w, r, h.notice(r, err))
		return
	}
	redirect(w, r, "/seller/produk")
}

func (h *Handler) ProductUpdate(w http.ResponseWriter, r *http.Request) {
	input, ok := parseProduct(r)
	if !ok {
		h.renderProducts(w, r, msgInvalidForm)
		return
	}
	if _, err := h.catalog.Update(r.Context(), chi.URLParam(r, "id"), input); err != nil {
		h.renderProducts(w, r, h.notice(r, err))
		return
	}
	redirect(w, r, "/seller/produk")
}

func (h *Handler) ProductDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.catalog.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		h.renderProducts(w, r, h.notice(r, err))
		return
	}
	redirect(w, r, "/seller/produk")
}

// Categories renders the category reference the product form picks from.
//
// There are no category mutations here, and that is the platform's decision rather
// than an omission: category.v1 exposes reads only so that only category-service
// mutates the hierarchy. A console that wrote categories would mean a second writer
// for an aggregate with a single owner.
func (h *Handler) Categories(w http.ResponseWriter, r *http.Request) {
	categories, err := h.catalog.ListCategories(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.page(w, r, partials.CategoriesSection(categories))
}

func (h *Handler) renderProducts(w http.ResponseWriter, r *http.Request, errMsg string) {
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
	h.page(w, r, partials.ProductsSection(products, categories, errMsg))
}

// trimmed reads a form field and normalizes surrounding whitespace, so a value that
// was only ever spaces reads as absent rather than as a one-character name.
func trimmed(r *http.Request, field string) string {
	return strings.TrimSpace(r.PostFormValue(field))
}

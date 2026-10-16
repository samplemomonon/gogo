package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"gogo/internal/pages/seller/application/results"
	"gogo/internal/pages/seller/views/partials"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/tenant"
)

// Products renders the seller-owned product page as a full document. A one-time
// create result may supplement the listing projection after PRG while its event
// propagates; the product platform remains the authority for writes and the
// listing service remains the authority for the catalogue read model.
func (h *Handler) Products(w http.ResponseWriter, r *http.Request) {
	created := h.created.take(tenant.FromContextOrDefault(r.Context()), r.URL.Query().Get("created"))
	h.renderProducts(w, r, "", created)
}

func (h *Handler) ProductCreate(w http.ResponseWriter, r *http.Request) {
	types, err := h.catalog.ListProductTypes(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	input, ok := parseProduct(r, types)
	if !ok {
		h.renderProducts(w, r, msgInvalidForm, nil)
		return
	}
	created, err := h.catalog.Create(r.Context(), input)
	if err != nil {
		h.renderProducts(w, r, h.notice(r, err), nil)
		return
	}
	// Seller routes deliberately use plain Post/Redirect/Get (see the page's
	// full-page-only rendering contract). Preserve the committed write result for
	// the redirected GET so the seller sees the new row immediately even while the
	// independently-owned listing projection is catching up.
	flashToken, err := h.created.put(tenant.FromContextOrDefault(r.Context()), created)
	if err != nil {
		h.logger.Error("save created product for seller redirect", "error", err)
		redirect(w, r, "/seller/produk")
		return
	}
	redirect(w, r, "/seller/produk?created="+flashToken)
}

func (h *Handler) ProductAttributeFields(w http.ResponseWriter, r *http.Request) {
	types, err := h.catalog.ListProductTypes(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	selected := r.URL.Query().Get("productTypeId")
	for i := range types {
		if types[i].ID == selected {
			_ = partials.ProductAttributeFields(types[i]).Render(r.Context(), w)
			return
		}
	}
	http.Error(w, "unknown product type", http.StatusBadRequest)
}

func (h *Handler) ProductUpdate(w http.ResponseWriter, r *http.Request) {
	input, ok := parseProduct(r)
	if !ok {
		h.renderProducts(w, r, msgInvalidForm, nil)
		return
	}
	if _, err := h.catalog.Update(r.Context(), chi.URLParam(r, "id"), input); err != nil {
		h.renderProducts(w, r, h.notice(r, err), nil)
		return
	}
	redirect(w, r, "/seller/produk")
}

func (h *Handler) ProductDelete(w http.ResponseWriter, r *http.Request) {
	if err := h.catalog.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		h.renderProducts(w, r, h.notice(r, err), nil)
		return
	}
	redirect(w, r, "/seller/produk")
}

// Categories renders admin-managed global categories alongside categories owned
// by this merchant. Only merchant-local categories can be created from Seller.
func (h *Handler) Categories(w http.ResponseWriter, r *http.Request) {
	categories, err := h.catalog.ListCategories(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	successMsg := ""
	createdID := r.URL.Query().Get("created")
	for _, category := range categories {
		if category.ID == createdID && !category.IsGlobal {
			successMsg = "Kategori toko berhasil dibuat."
			break
		}
	}
	h.page(w, r, partials.CategoriesSection(categories, "", successMsg))
}

// CategoryCreate creates a merchant-local category. Global categories are
// administrator-owned and are never writable from this page.
func (h *Handler) CategoryCreate(w http.ResponseWriter, r *http.Request) {
	input, ok := parseCategory(r)
	if !ok {
		h.renderCategories(w, r, "Form kategori tidak valid", "")
		return
	}
	created, err := h.catalog.CreateMerchantCategory(r.Context(), input)
	if err != nil {
		message := h.notice(r, err)
		if errors.Is(err, productconsumer.ErrInvalid) {
			message = "Kategori tidak valid"
			if detail := productconsumer.Detail(err); detail != "" {
				message += ": " + detail
			}
		}
		h.renderCategories(w, r, message, "")
		return
	}
	redirect(w, r, "/seller/kategori?created="+created.ID)
}

func (h *Handler) renderCategories(w http.ResponseWriter, r *http.Request, errMsg, successMsg string) {
	categories, err := h.catalog.ListCategories(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.page(w, r, partials.CategoriesSection(categories, errMsg, successMsg))
}

func (h *Handler) renderProducts(w http.ResponseWriter, r *http.Request, errMsg string, created *results.Product) {
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
	var successMsg string
	if created != nil {
		products = includeCreatedProduct(products, created)
		successMsg = "Produk berhasil ditambahkan."
	}
	types, err := h.catalog.ListProductTypes(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.page(w, r, partials.ProductsSection(products, categories, types, errMsg, successMsg))
}

func includeCreatedProduct(products []*results.Product, created *results.Product) []*results.Product {
	for _, product := range products {
		if product != nil && product.ID == created.ID {
			return products
		}
	}
	return append([]*results.Product{created}, products...)
}

// trimmed reads a form field and normalizes surrounding whitespace, so a value that
// was only ever spaces reads as absent rather than as a one-character name.
func trimmed(r *http.Request, field string) string {
	return strings.TrimSpace(r.PostFormValue(field))
}

// Package http provides HTTP handlers for the product API.
package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	productdomain "product-service/internal/domain/product"
	productusecase "product-service/internal/usecase/product"
)

// ProductHandler handles HTTP requests for product operations.
type ProductHandler struct {
	usecase productusecase.ProductUseCases
	log     *slog.Logger
}

// NewProductHandler creates a new product handler.
func NewProductHandler(uc productusecase.ProductUseCases, log *slog.Logger) *ProductHandler {
	return &ProductHandler{usecase: uc, log: log}
}

// RegisterRoutes mounts the product routes on the mux.
func (h *ProductHandler) RegisterRoutes(mux *http.ServeMux) {
	const base = "/api/v1/products"
	mux.HandleFunc("POST "+base, h.CreateProduct)
	mux.HandleFunc("PUT "+base+"/{id}", h.UpdateProduct)
	mux.HandleFunc("DELETE "+base+"/{id}", h.DeleteProduct)
	mux.HandleFunc("POST "+base+"/{id}/stock", h.AdjustStock)
	mux.HandleFunc("POST "+base+"/{id}/variants", h.CreateVariant)
	mux.HandleFunc("PUT "+base+"/{id}/variants/{variant_id}", h.UpdateVariant)
	mux.HandleFunc("DELETE "+base+"/{id}/variants/{variant_id}", h.DeleteVariant)
}

// CreateProduct handles POST /api/v1/products.
func (h *ProductHandler) CreateProduct(w http.ResponseWriter, r *http.Request) {
	var req productdomain.CreateProductRequest
	if err := decodeJSON(w, r, &req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}

	p, err := h.usecase.CreateProduct(r.Context(), &req)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/products/"+strconv.FormatInt(p.ID, 10))
	jsonResponse(w, http.StatusCreated, p)
}

// UpdateProduct handles PUT /api/v1/products/{id}.
func (h *ProductHandler) UpdateProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	var req productdomain.UpdateProductRequest
	if err := decodeJSON(w, r, &req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	if req.IsEmpty() {
		badRequest(w, "request body must set at least one field")
		return
	}

	p, err := h.usecase.UpdateProduct(r.Context(), id, &req)
	if err != nil {
		h.fail(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, p)
}

// DeleteProduct handles DELETE /api/v1/products/{id}.
func (h *ProductHandler) DeleteProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	if err := h.usecase.DeleteProduct(r.Context(), id); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AdjustStock handles POST /api/v1/products/{id}/stock.
func (h *ProductHandler) AdjustStock(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	var req struct {
		Delta int64 `json:"delta"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	if req.Delta == 0 {
		badRequest(w, "delta must not be zero")
		return
	}

	p, err := h.usecase.AdjustStock(r.Context(), id, req.Delta)
	if err != nil {
		h.fail(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, p)
}

// CreateVariant handles POST /api/v1/products/{id}/variants.
func (h *ProductHandler) CreateVariant(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	var req productdomain.CreateVariantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}

	v, err := h.usecase.CreateVariant(r.Context(), id, &req)
	if err != nil {
		h.fail(w, err)
		return
	}
	w.Header().Set("Location",
		"/api/v1/products/"+strconv.FormatInt(id, 10)+
			"/variants/"+strconv.FormatInt(v.ID, 10),
	)
	jsonResponse(w, http.StatusCreated, v)
}

// UpdateVariant handles PUT /api/v1/products/{id}/variants/{variant_id}.
func (h *ProductHandler) UpdateVariant(w http.ResponseWriter, r *http.Request) {
	id, variantID, ok := h.pathIDs(w, r)
	if !ok {
		return
	}

	var req productdomain.UpdateVariantRequest
	if err := decodeJSON(w, r, &req); err != nil {
		badRequest(w, "invalid request body: "+err.Error())
		return
	}
	if req.IsEmpty() {
		badRequest(w, "request body must set at least one field")
		return
	}

	v, err := h.usecase.UpdateVariant(r.Context(), id, variantID, &req)
	if err != nil {
		h.fail(w, err)
		return
	}
	jsonResponse(w, http.StatusOK, v)
}

// DeleteVariant handles DELETE /api/v1/products/{id}/variants/{variant_id}.
func (h *ProductHandler) DeleteVariant(w http.ResponseWriter, r *http.Request) {
	id, variantID, ok := h.pathIDs(w, r)
	if !ok {
		return
	}

	if err := h.usecase.DeleteVariant(r.Context(), id, variantID); err != nil {
		h.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProductHandler) pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	return pathInt(w, r, "id", "product")
}

func (h *ProductHandler) pathIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	productID, ok := h.pathID(w, r)
	if !ok {
		return 0, 0, false
	}
	variantID, ok := pathInt(w, r, "variant_id", "variant")
	if !ok {
		return 0, 0, false
	}
	return productID, variantID, true
}

func pathInt(w http.ResponseWriter, r *http.Request, name, label string) (int64, bool) {
	raw := r.PathValue(name)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		badRequest(w, "invalid "+label+" id: "+raw)
		return 0, false
	}
	return value, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	const maxRequestBody = 1 << 20 // 1 MiB
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func jsonResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("write response", "error", err)
	}
}

func badRequest(w http.ResponseWriter, message string) {
	jsonResponse(w, http.StatusBadRequest, errorResponse{Error: message})
}

func (h *ProductHandler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, productdomain.ErrProductNotFound), errors.Is(err, productdomain.ErrVariantNotFound):
		jsonResponse(w, http.StatusNotFound, errorResponse{Error: "not found"})
	case errors.Is(err, productdomain.ErrInvalidInput),
		errors.Is(err, productdomain.ErrEmptyName),
		errors.Is(err, productdomain.ErrEmptySKU),
		errors.Is(err, productdomain.ErrInvalidSlug),
		errors.Is(err, productdomain.ErrInvalidSKU),
		errors.Is(err, productdomain.ErrInvalidCurrency),
		errors.Is(err, productdomain.ErrDescriptionTooLong),
		errors.Is(err, productdomain.ErrNameTooLong),
		errors.Is(err, productdomain.ErrSKUTooLong),
		errors.Is(err, productdomain.ErrSlugTooLong),
		errors.Is(err, productdomain.ErrNegativePrice),
		errors.Is(err, productdomain.ErrNegativeStock),
		errors.Is(err, productdomain.ErrMaxVariantsExceeded),
		errors.Is(err, productdomain.ErrDuplicateSKU),
		errors.Is(err, productdomain.ErrVariantNotOwnedByProduct):
		jsonResponse(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, productdomain.ErrConflict):
		jsonResponse(w, http.StatusConflict, errorResponse{Error: err.Error()})
	case errors.Is(err, productdomain.ErrOutboxBlocked):
		h.log.Error("change rejected because its event could not be recorded", "error", err)
		jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "the change could not be recorded, retry"})
	case errors.Is(err, productdomain.ErrUnavailable):
		h.log.Warn("dependency unavailable", "error", err)
		jsonResponse(w, http.StatusServiceUnavailable, errorResponse{Error: "a required service is unavailable"})
	default:
		h.log.Error("unhandled request error", "error", err)
		jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}

type errorResponse struct {
	Error string `json:"error"`
}
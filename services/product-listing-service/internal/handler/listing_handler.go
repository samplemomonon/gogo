// Package handler exposes the product read API over HTTP. Handlers translate
// between the wire format and the service layer and contain no business rules.
//
// Every route here is a GET. Writes go to product-service, and the projection
// that backs these reads is built from the events it publishes. The split is
// visible in the paths: this service answers /api/v1/products, and creating a
// product is a POST to the same path on a different port.
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/httpx"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

// ProductListingService is the behaviour the handler needs from the service
// layer. Declaring it as an interface lets tests supply a stub.
type ProductListingService interface {
	GetByID(ctx context.Context, id int64, includeVariants bool) (*model.Product, error)
	List(ctx context.Context, q *model.ListProductsQuery) (*model.ListProductsResult, error)
	ListVariants(ctx context.Context, productID int64) ([]*model.Variant, error)
}

// searchProductsPath is where free-text search now lives, quoted back to a caller
// that still sends q here.
const searchProductsPath = "/api/v1/search/products"

// ListProductsResponse is the body of GET /api/v1/products.
type ListProductsResponse struct {
	Products []*model.Product `json:"products"`
	Total    int64            `json:"total"`
	Limit    int              `json:"limit"`
	Offset   int              `json:"offset"`
}

// ProductListingHandler serves the product read endpoints.
type ProductListingHandler struct {
	service ProductListingService
	log     *slog.Logger
}

// NewProductListingHandler returns a handler backed by svc.
func NewProductListingHandler(svc ProductListingService, log *slog.Logger) *ProductListingHandler {
	return &ProductListingHandler{service: svc, log: log}
}

// RegisterRoutes mounts the product read routes on mux.
func (h *ProductListingHandler) RegisterRoutes(mux *http.ServeMux) {
	const base = "/api/v1/products"
	mux.HandleFunc("GET "+base, h.List)
	mux.HandleFunc("GET "+base+"/{id}", h.GetByID)
	mux.HandleFunc("GET "+base+"/{id}/variants", h.ListVariants)
}

// List handles GET /api/v1/products.
func (h *ProductListingHandler) List(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// A search term is answered by product-search-service, not here. Rejecting it
	// is deliberate: quietly ignoring it would hand back the whole unfiltered
	// catalogue to a client that asked for a search, and the result would look
	// like a correct empty or a correct everything. A 400 that names the new
	// service turns a silent wrong answer into a fixable error. Presence is what
	// is checked, not the value, so an empty "q=" is rejected too: it would
	// otherwise match every product.
	if _, ok := query["q"]; ok {
		httpx.BadRequest(w, "q is not supported here: free-text search moved to "+
			"product-search-service, use GET "+searchProductsPath+"?q=...")
		return
	}

	// The raw parameter is checked before parsing. model.ParseStatus treats an
	// empty string as "draft", which is the right default for a create body but
	// the wrong one for a filter: applying it to an absent status would silently
	// hide every active product, returning only drafts for a request that asked
	// for everything.
	var statusFilter *model.Status
	if raw := strings.TrimSpace(query.Get("status")); raw != "" {
		parsed, err := model.ParseStatus(raw)
		if err != nil {
			httpx.BadRequest(w, err.Error())
			return
		}
		statusFilter = &parsed
	}
	// category_id stays a plain int64: an id of 0 is not a category, so zero
	// already means unset without losing an expressible query.
	categoryID, err := parseOptionalNonNegativeInt64Value(query.Get("category_id"), "category_id")
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	minPrice, err := parseOptionalNonNegativeInt64(query.Get("min_price_cents"), "min_price_cents")
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	maxPrice, err := parseOptionalNonNegativeInt64(query.Get("max_price_cents"), "max_price_cents")
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	limit, err := parseOptionalNonNegativeInt(query.Get("limit"), "limit")
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	offset, err := parseOptionalNonNegativeInt(query.Get("offset"), "offset")
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}

	req := &model.ListProductsQuery{
		CategoryID:      categoryID,
		Status:          statusFilter,
		MinPriceCents:   minPrice,
		MaxPriceCents:   maxPrice,
		Limit:           limit,
		Offset:          offset,
		IncludeVariants: parseBool(query.Get("include_variants")),
		// An absent or empty merchant_id means unfiltered, matching the
		// contract: the browse page sends none, while the seller console
		// sends the merchant it is scoped to.
		MerchantID: strings.TrimSpace(query.Get("merchant_id")),
	}

	result, err := h.service.List(r.Context(), req)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ListProductsResponse{
		Products: result.Products,
		Total:    result.Total,
		Limit:    req.Limit,
		Offset:   req.Offset,
	})
}

// GetByID handles GET /api/v1/products/{id}.
func (h *ProductListingHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id", "product")
	if !ok {
		return
	}

	includeVariants := parseBool(r.URL.Query().Get("include_variants"))
	product, err := h.service.GetByID(r.Context(), id, includeVariants)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, product)
}

// ListVariants handles GET /api/v1/products/{id}/variants.
func (h *ProductListingHandler) ListVariants(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id", "product")
	if !ok {
		return
	}

	variants, err := h.service.ListVariants(r.Context(), id)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, variants)
}

// pathInt reads a named path value as a positive integer, answering 400 and
// returning false when it is not.
func pathInt(w http.ResponseWriter, r *http.Request, name, label string) (int64, bool) {
	raw := r.PathValue(name)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		httpx.BadRequest(w, "invalid "+label+" id: "+raw)
		return 0, false
	}
	return value, true
}

// parseBool treats anything other than the explicit truthy spellings as false,
// so a stray value never silently enables an expensive option.
func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// parseOptionalNonNegativeInt64 reads an optional non-negative integer filter.
//
// It returns a pointer because absent and zero are different answers. A caller
// sending max_price_cents=0 is asking for products that cost nothing, and
// collapsing that to "no filter" returns the whole catalogue instead of the empty
// set: a larger result than was asked for, with no error to explain it.
func parseOptionalNonNegativeInt64(raw, name string) (*int64, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return nil, &queryError{message: name + " must be a non-negative integer, got " + strconv.Quote(raw)}
	}
	return &value, nil
}

// parseOptionalNonNegativeInt64Value is parseOptionalNonNegativeInt64 for the
// filters where zero can safely mean unset, collapsing the pointer back to a
// plain value.
func parseOptionalNonNegativeInt64Value(raw, name string) (int64, error) {
	value, err := parseOptionalNonNegativeInt64(raw, name)
	if err != nil {
		return 0, err
	}
	if value == nil {
		return 0, nil
	}
	return *value, nil
}

// parseOptionalNonNegativeInt reads an optional non-negative integer parameter.
func parseOptionalNonNegativeInt(raw, name string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, &queryError{message: name + " must be a non-negative integer, got " + strconv.Quote(raw)}
	}
	return value, nil
}

// queryError carries a 400 message out of a helper that cannot write to w.
type queryError struct{ message string }

func (e *queryError) Error() string { return e.message }

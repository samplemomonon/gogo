// Package handler exposes the product search API over HTTP. Handlers translate
// between the wire format and the service layer and contain no business rules.
//
// Every route here is a GET. Writes go to product-service, and the index that
// backs these reads is built from the events it publishes. The split is visible in
// the paths: this service answers /api/v1/search, browsing is answered by
// product-listing-service, and creating a product is a POST on that service's own
// path.
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"product-search-service/internal/httpx"
	"product-search-service/internal/model"
)

// ProductSearchService is the behaviour the handler needs from the service layer.
// Declaring it as an interface lets tests supply a stub.
type ProductSearchService interface {
	Search(ctx context.Context, q *model.SearchQuery) (*model.SearchResult, error)
}

// SearchResponse is the body of GET /api/v1/search/products.
type SearchResponse struct {
	Products []*model.SearchProduct `json:"products"`
	Total    int64                  `json:"total"`
	Limit    int                    `json:"limit"`
	Offset   int                    `json:"offset"`
	// TookMS is how long the query took server-side. It is the signal to watch as
	// the catalogue grows: the results stay correct, so a slowdown shows up here
	// before it shows up as a timeout, and it is the condition for moving to a
	// dedicated search engine.
	TookMS int64 `json:"took_ms"`
}

// SearchHandler serves the product search endpoints.
type SearchHandler struct {
	service ProductSearchService
	log     *slog.Logger
}

// NewSearchHandler returns a handler backed by svc.
func NewSearchHandler(svc ProductSearchService, log *slog.Logger) *SearchHandler {
	return &SearchHandler{service: svc, log: log}
}

// RegisterRoutes mounts the search routes on mux.
func (h *SearchHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+searchProductsPath, h.SearchProducts)
}

// searchProductsPath is the search route, and the one product-listing-service
// quotes back to a caller that still sends it a search term.
const searchProductsPath = "/api/v1/search/products"

// SearchProducts handles GET /api/v1/search/products.
func (h *SearchHandler) SearchProducts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

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

	req := &model.SearchQuery{
		Query:         query.Get("q"),
		CategoryID:    categoryID,
		Status:        statusFilter,
		MinPriceCents: minPrice,
		MaxPriceCents: maxPrice,
		Limit:         limit,
		Offset:        offset,
	}

	result, err := h.service.Search(r.Context(), req)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}

	httpx.JSON(w, http.StatusOK, SearchResponse{
		Products: result.Products,
		Total:    result.Total,
		// The applied page, not the requested one, so a client can page without
		// re-deriving the default and the cap.
		Limit:  result.Limit,
		Offset: result.Offset,
		TookMS: result.TookMS,
	})
}

// parseOptionalNonNegativeInt64 reads an optional non-negative integer filter.
//
// It returns a pointer because absent and zero are different answers. A caller
// sending max_price_cents=0 is asking for products that cost nothing, and
// collapsing that to "no filter" returns the whole index instead of the empty set:
// a larger result than was asked for, with no error to explain it.
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
// filters where zero can safely mean unset, collapsing the pointer back to a plain
// value.
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

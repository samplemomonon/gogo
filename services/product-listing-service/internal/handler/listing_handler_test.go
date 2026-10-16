package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/apperror"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

// stubService records the query the handler built and returns a fixed page.
type stubService struct {
	gotQuery   *model.ListProductsQuery
	gotID      int64
	gotInclude bool

	products []*model.Product
	total    int64
	err      error
}

func (s *stubService) List(_ context.Context, q *model.ListProductsQuery) (*model.ListProductsResult, error) {
	s.gotQuery = q
	if s.err != nil {
		return nil, s.err
	}
	// An empty slice rather than nil, which is what the repository returns and
	// what the JSON contract depends on. A stub returning nil here would be
	// testing a state the real code never produces.
	products := s.products
	if products == nil {
		products = []*model.Product{}
	}
	return &model.ListProductsResult{Products: products, Total: s.total}, nil
}

func (s *stubService) GetByID(
	_ context.Context,
	id int64,
	includeVariants bool,
) (*model.Product, error) {
	s.gotID, s.gotInclude = id, includeVariants
	if s.err != nil {
		return nil, s.err
	}
	return &model.Product{ID: id, Name: "Aurora Phone", Variants: []*model.Variant{}}, nil
}

func (s *stubService) ListVariants(_ context.Context, _ int64) ([]*model.Variant, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []*model.Variant{}, nil
}

func testHandler(svc *stubService) http.Handler {
	log := testLogger()
	mux := http.NewServeMux()
	NewProductListingHandler(svc, log).RegisterRoutes(mux)
	return mux
}

func doGet(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	testHandler(&stubService{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

// An absent status must not become a draft filter. model.ParseStatus treats an
// empty string as draft, which is the right default for a create body and exactly
// wrong for a filter: applying it would hide every active product and answer a
// request for the whole catalogue with only drafts.
func TestListWithoutAStatusFilterReturnsEverything(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/products", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if svc.gotQuery == nil {
		t.Fatal("the service was not called")
	}
	if svc.gotQuery.Status != nil {
		t.Errorf("status filter = %q, want nil so no filter is applied", *svc.gotQuery.Status)
	}
}

func TestListAppliesAnExplicitStatusFilter(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/products?status=active", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if svc.gotQuery.Status == nil {
		t.Fatal("status filter = nil, want the requested active filter")
	}
	if *svc.gotQuery.Status != model.StatusActive {
		t.Errorf("status filter = %q, want active", *svc.gotQuery.Status)
	}
}

func TestListRejectsAnUnknownStatus(t *testing.T) {
	t.Parallel()

	recorder := doGet(t, "/api/v1/products?status=live")
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown status", recorder.Code)
	}
}

func TestListRejectsNonNumericFilters(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/api/v1/products?limit=abc",
		"/api/v1/products?offset=-1",
		"/api/v1/products?category_id=-2",
		"/api/v1/products?min_price_cents=x",
	} {
		recorder := doGet(t, target)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, recorder.Code)
		}
	}
}

func TestListParsesTheFilters(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/api/v1/products?category_id=3&status=archived"+
			"&min_price_cents=1000&max_price_cents=9999&limit=10&offset=20&include_variants=true", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	q := svc.gotQuery
	switch {
	case q.CategoryID != 3:
		t.Errorf("category = %d, want 3", q.CategoryID)
	case !priceIs(q.MinPriceCents, 1000) || !priceIs(q.MaxPriceCents, 9999):
		t.Errorf("price band = %v-%v, want 1000-9999", q.MinPriceCents, q.MaxPriceCents)
	case q.Limit != 10 || q.Offset != 20:
		t.Errorf("page = %d+%d, want 10+20", q.Limit, q.Offset)
	case !q.IncludeVariants:
		t.Error("include_variants = false, want true")
	case q.Status == nil || *q.Status != model.StatusArchived:
		t.Error("status = archived, want the requested filter")
	}
}

// A search term is no longer answered here, and it has to be refused rather than
// ignored. Dropping it would hand the entire catalogue to a caller that asked for
// a search, and that answer is indistinguishable from a legitimate one. The error
// names the replacement so the caller can fix itself in one step.
func TestListRefusesASearchTermWithAPointerToTheSearchService(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/api/v1/products?q=aurora",
		// An empty value is refused too: it would otherwise match every product,
		// which is the same silent wrong answer in a different costume.
		"/api/v1/products?q=",
		"/api/v1/products?q=aurora&limit=5",
	} {
		recorder := doGet(t, target)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, recorder.Code)
			continue
		}
		if body := recorder.Body.String(); !strings.Contains(body, "product-search-service") {
			t.Errorf("%s: body = %s, want it to name the service that now owns search", target, body)
		}
	}
}

// include_variants is expensive, so only the explicit truthy spellings may turn
// it on. A stray value must not silently enable it.
func TestIncludeVariantsNeedsAnExplicitTrue(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]bool{
		"1": true, "true": true, "yes": true, "on": true,
		"": false, "0": false, "maybe": false, "y": false,
	} {
		svc := &stubService{}
		target := "/api/v1/products"
		if raw != "" {
			target += "?include_variants=" + raw
		}
		testHandler(svc).ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, target, nil))

		if svc.gotQuery.IncludeVariants != want {
			t.Errorf("include_variants=%q: got %v, want %v", raw, svc.gotQuery.IncludeVariants, want)
		}
	}
}

func TestGetByID(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/products/42?include_variants=true", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if svc.gotID != 42 {
		t.Errorf("id = %d, want 42", svc.gotID)
	}
	if !svc.gotInclude {
		t.Error("includeVariants = false, want true")
	}

	var product model.Product
	if err := json.Unmarshal(recorder.Body.Bytes(), &product); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if product.ID != 42 {
		t.Errorf("body id = %d, want 42", product.ID)
	}
}

func TestGetByIDRejectsANonPositiveId(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/api/v1/products/abc", "/api/v1/products/0", "/api/v1/products/-1",
	} {
		recorder := doGet(t, target)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, recorder.Code)
		}
	}
}

// A miss is a 404 with a stable message. The body must not distinguish "no such
// product" from "not projected yet", because a caller cannot act on the
// difference and the projection catches up on its own.
func TestGetByIDReportsAMissAsNotFound(t *testing.T) {
	t.Parallel()

	svc := &stubService{err: apperror.ErrNotFound}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/products/99", nil))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestListVariants(t *testing.T) {
	t.Parallel()

	recorder := doGet(t, "/api/v1/products/42/variants")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
}

// The response renders an empty list as [], never null, so a client does not have
// to special-case a missing field.
func TestEmptyResultsRenderAsAnArray(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/products", nil))

	var body struct {
		Products []*model.Product `json:"products"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Products == nil {
		t.Error("products is null, want an empty array")
	}
}

func TestListingHasNoWriteRoutes(t *testing.T) {
	t.Parallel()

	// The write side belongs to product-service. A route appearing here would mean
	// a second writer for a projection, which is the one thing that would break the
	// guarantee the read model is built on.
	handler := testHandler(&stubService{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder,
			httptest.NewRequest(method, "/api/v1/products", nil))

		if recorder.Code != http.StatusMethodNotAllowed && recorder.Code != http.StatusNotFound {
			t.Errorf("%s /api/v1/products: status = %d, want the route to be absent",
				method, recorder.Code)
		}
	}
}

// priceIs reports whether a price bound is present and equal to want.
func priceIs(got *int64, want int64) bool { return got != nil && *got == want }

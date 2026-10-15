package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"product-search-service/internal/apperror"
	"product-search-service/internal/model"
	"product-search-service/internal/search"
)

// stubEngine is the minimum search.Engine the real service needs, so these tests
// can drive validation without a database.
type stubEngine struct{}

func (e *stubEngine) Search(_ context.Context, q *model.SearchQuery) (*model.SearchResult, error) {
	return &model.SearchResult{Products: []*model.SearchProduct{}, Limit: q.Limit, Offset: q.Offset}, nil
}

// Index and Remove are not on search.Engine: indexing happens inside the
// projection's transaction, on repository.Indexer.
func (e *stubEngine) Count(context.Context) (int64, error) { return 0, nil }

// stubService records the query the handler built and returns a fixed page.
type stubService struct {
	gotQuery *model.SearchQuery

	products []*model.SearchProduct
	total    int64
	err      error
}

func (s *stubService) Search(_ context.Context, q *model.SearchQuery) (*model.SearchResult, error) {
	s.gotQuery = q
	if s.err != nil {
		return nil, s.err
	}
	// An empty slice rather than nil, which is what the engine returns and what
	// the JSON contract depends on. A stub returning nil would be testing a state
	// the real code never produces.
	products := s.products
	if products == nil {
		products = []*model.SearchProduct{}
	}
	return &model.SearchResult{
		Products: products,
		Total:    s.total,
		Limit:    q.Limit,
		Offset:   q.Offset,
		TookMS:   7,
	}, nil
}

func testHandler(svc *stubService) http.Handler {
	mux := http.NewServeMux()
	NewSearchHandler(svc, testLogger()).RegisterRoutes(mux)
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
func TestSearchWithoutAStatusFilterReturnsEverything(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/search/products?q=aurora", nil))

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

func TestSearchAppliesAnExplicitStatusFilter(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/search/products?q=aurora&status=active", nil))

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

func TestSearchRejectsAnUnknownStatus(t *testing.T) {
	t.Parallel()

	recorder := doGet(t, "/api/v1/search/products?q=aurora&status=live")
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown status", recorder.Code)
	}
}

func TestSearchRejectsNonNumericFilters(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/api/v1/search/products?q=aurora&limit=abc",
		"/api/v1/search/products?q=aurora&offset=-1",
		"/api/v1/search/products?q=aurora&category_id=-2",
		"/api/v1/search/products?q=aurora&min_price_cents=x",
	} {
		recorder := doGet(t, target)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", target, recorder.Code)
		}
	}
}

func TestSearchParsesTheFilters(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet,
			"/api/v1/search/products?q=aurora&category_id=3&status=archived"+
				"&min_price_cents=1000&max_price_cents=9999&limit=10&offset=20", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	q := svc.gotQuery
	switch {
	case q.Query != "aurora":
		t.Errorf("q = %q, want aurora", q.Query)
	case q.CategoryID != 3:
		t.Errorf("category = %d, want 3", q.CategoryID)
	case !priceIs(q.MinPriceCents, 1000) || !priceIs(q.MaxPriceCents, 9999):
		t.Errorf("price band = %v-%v, want 1000-9999", q.MinPriceCents, q.MaxPriceCents)
	case q.Limit != 10 || q.Offset != 20:
		t.Errorf("page = %d+%d, want 10+20", q.Limit, q.Offset)
	case q.Status == nil || *q.Status != model.StatusArchived:
		t.Error("status = archived, want the requested filter")
	}
}

// The response has to carry the page that was actually applied, not the one that
// was requested, so a client can page without re-deriving the default and the cap.
func TestSearchEchoesTheAppliedPage(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	recorder := httptest.NewRecorder()
	// limit=0 asks for the default; the response must report the real one.
	testHandler(svc).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/search/products?q=aurora&offset=5", nil))

	var body struct {
		Limit    int   `json:"limit"`
		Offset   int   `json:"offset"`
		Total    int64 `json:"total"`
		TookMS   int64 `json:"took_ms"`
		Products []struct {
			ID    int64   `json:"id"`
			Score float64 `json:"score"`
		} `json:"products"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Limit != svc.gotQuery.Limit || body.Offset != 5 {
		t.Errorf("page echoed as %d+%d, want %d+5", body.Limit, body.Offset, svc.gotQuery.Limit)
	}
	// The duration is the signal to watch as the catalogue grows, so it has to be
	// in the response rather than only in the logs.
	if body.TookMS < 0 {
		t.Errorf("took_ms = %d, want a non-negative duration", body.TookMS)
	}
}

// The score is the ranking, so it has to survive the wire.
func TestSearchResponseCarriesTheScore(t *testing.T) {
	t.Parallel()

	svc := &stubService{products: []*model.SearchProduct{{ID: 1, Name: "Aurora", Score: 0.87}}}
	recorder := httptest.NewRecorder()
	testHandler(svc).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/search/products?q=aurora", nil))

	var body struct {
		Products []struct {
			ID    int64   `json:"id"`
			Score float64 `json:"score"`
		} `json:"products"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Products) != 1 || body.Products[0].Score != 0.87 {
		t.Errorf("products = %+v, want the score preserved", body.Products)
	}
}

// The response renders an empty list as [], never null, so a client does not have
// to special-case a missing field.
func TestEmptyResultsRenderAsAnArray(t *testing.T) {
	t.Parallel()

	recorder := doGet(t, "/api/v1/search/products?q=aurora")

	var body struct {
		Products []*model.SearchProduct `json:"products"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Products == nil {
		t.Error("products is null, want an empty array")
	}
}

// A domain failure has to become the right status code rather than a 500, or a
// client cannot tell a bad query from a broken service.
func TestSearchMapsDomainErrorsToStatusCodes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want int
	}{
		"invalid input": {apperror.ErrInvalidInput, http.StatusBadRequest},
		"not found":     {apperror.ErrNotFound, http.StatusNotFound},
		"unavailable":   {apperror.ErrUnavailable, http.StatusServiceUnavailable},
		"unknown":       {errString("boom"), http.StatusInternalServerError},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			testHandler(&stubService{err: tc.err}).ServeHTTP(recorder,
				httptest.NewRequest(http.MethodGet, "/api/v1/search/products?q=aurora", nil))

			if recorder.Code != tc.want {
				t.Errorf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}
}

// A term that is too short for the trigram index must come back as a 400 explaining
// why, and the message has to name the constraint rather than saying "invalid
// input" and leaving the caller to guess.
//
// These two go through the real service rather than the stub, because the rule is
// the service's and a stub that accepted everything would prove nothing about it.
// The handler's job here is only to turn the resulting error into a 400.
func TestInvalidTermsAreRejectedEndToEnd(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		target string
		want   string
	}{
		"too short": {
			target: "/api/v1/search/products?q=ab",
			want:   "at least 3 consecutive",
		},
		"no term": {
			target: "/api/v1/search/products",
			want:   "product-listing-service",
		},
		"only metacharacters": {
			target: "/api/v1/search/products?q=%25",
			want:   "at least 3 consecutive",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			NewSearchHandler(search.NewService(&stubEngine{}), testLogger()).RegisterRoutes(mux)

			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.target, nil))

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", recorder.Code)
			}
			if body := recorder.Body.String(); !strings.Contains(body, tc.want) {
				t.Errorf("body = %s, want it to mention %q", body, tc.want)
			}
		})
	}
}

// The write side belongs to product-service, and so does browsing. A mutating route
// appearing here would mean a second writer for a projection.
func TestSearchHasNoWriteRoutes(t *testing.T) {
	t.Parallel()

	handler := testHandler(&stubService{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder,
			httptest.NewRequest(method, "/api/v1/search/products", nil))

		if recorder.Code != http.StatusMethodNotAllowed && recorder.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want the route to be absent", method, recorder.Code)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// priceIs reports whether a price bound is present and equal to want.
func priceIs(got *int64, want int64) bool { return got != nil && *got == want }

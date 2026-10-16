package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"category-service/internal/apperror"
	"category-service/internal/model"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubService lets handler tests drive the error mapping without a database.
type stubService struct {
	created   *model.Category
	updated   *model.Category
	children  []*model.Category
	ancestors []*model.Category
	tree      []*model.CategoryNode
	all       []*model.Category
	impact    int

	err error

	gotCreate *model.CreateCategoryRequest
	gotUpdate *model.UpdateCategoryRequest
	gotID     int64
}

func (s *stubService) Create(_ context.Context, req *model.CreateCategoryRequest) (*model.Category, error) {
	s.gotCreate = req
	return s.created, s.err
}

func (s *stubService) GetByID(_ context.Context, id int64) (*model.Category, error) {
	s.gotID = id
	return s.created, s.err
}

func (s *stubService) GetAll(context.Context) ([]*model.Category, error) {
	return s.all, s.err
}

func (s *stubService) GetTree(context.Context) ([]*model.CategoryNode, error) {
	return s.tree, s.err
}

func (s *stubService) GetChildren(_ context.Context, id int64) ([]*model.Category, error) {
	s.gotID = id
	return s.children, s.err
}

func (s *stubService) GetAncestors(_ context.Context, id int64) ([]*model.Category, error) {
	s.gotID = id
	return s.ancestors, s.err
}

func (s *stubService) Update(_ context.Context, id int64, req *model.UpdateCategoryRequest) (*model.Category, error) {
	s.gotID, s.gotUpdate = id, req
	return s.updated, s.err
}

func (s *stubService) Delete(_ context.Context, id int64) error {
	s.gotID = id
	return s.err
}

func (s *stubService) DeletionImpact(_ context.Context, id int64) (int, error) {
	s.gotID = id
	return s.impact, s.err
}

func newTestMux(svc *stubService) *http.ServeMux {
	mux := http.NewServeMux()
	NewCategoryHandler(svc, discardLogger()).RegisterRoutes(mux)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestErrorMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", apperror.ErrNotFound, http.StatusNotFound},
		{"invalid input", apperror.ErrInvalidInput, http.StatusBadRequest},
		{"cycle", apperror.ErrCircularReference, http.StatusConflict},
		{"unexpected", errors.New("connection refused"), http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &stubService{err: tc.err}
			mux := newTestMux(svc)

			rec := do(t, mux, http.MethodGet, "/api/v1/categories/1", "")

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestInternalErrorIsNotLeaked(t *testing.T) {
	t.Parallel()

	// A raw driver error must not reach the client verbatim.
	svc := &stubService{err: errors.New("pq: password authentication failed for user \"category_user\"")}
	mux := newTestMux(svc)

	rec := do(t, mux, http.MethodGet, "/api/v1/categories/1", "")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	for _, secret := range []string{"pq:", "category_user", "password"} {
		if strings.Contains(body, secret) {
			t.Errorf("response body leaks %q: %s", secret, body)
		}
	}
}

func TestCreateSetsLocationAndStatus(t *testing.T) {
	t.Parallel()

	svc := &stubService{created: &model.Category{ID: 42, Name: "Books"}}
	mux := newTestMux(svc)

	rec := do(t, mux, http.MethodPost, "/api/v1/categories", `{"name":"Books"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/api/v1/categories/42" {
		t.Errorf("Location = %q, want /api/v1/categories/42", got)
	}
	if svc.gotCreate == nil || svc.gotCreate.Name != "Books" {
		t.Errorf("service received %+v, want name Books", svc.gotCreate)
	}
}

func TestCreateRejectsUnknownField(t *testing.T) {
	t.Parallel()

	mux := newTestMux(&stubService{})

	rec := do(t, mux, http.MethodPost, "/api/v1/categories", `{"name":"Books","nope":1}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCreateRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	mux := newTestMux(&stubService{})

	rec := do(t, mux, http.MethodPost, "/api/v1/categories", `{"name":`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCreateRejectsTrailingDocument(t *testing.T) {
	t.Parallel()

	mux := newTestMux(&stubService{})

	rec := do(t, mux, http.MethodPost, "/api/v1/categories", `{"name":"A"}{"name":"B"}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestGetByIDRejectsNonNumericID(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	mux := newTestMux(svc)

	rec := do(t, mux, http.MethodGet, "/api/v1/categories/abc", "")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if svc.gotID != 0 {
		t.Error("service should not have been called with an invalid id")
	}
}

func TestGetByIDRejectsZeroAndNegativeID(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/api/v1/categories/0", "/api/v1/categories/-3"} {
		mux := newTestMux(&stubService{})
		if rec := do(t, mux, http.MethodGet, path, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, rec.Code)
		}
	}
}

func TestDeleteReturnsNoContent(t *testing.T) {
	t.Parallel()

	svc := &stubService{}
	mux := newTestMux(svc)

	rec := do(t, mux, http.MethodDelete, "/api/v1/categories/7", "")

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("204 body should be empty, got %q", body)
	}
	if svc.gotID != 7 {
		t.Errorf("deleted id = %d, want 7", svc.gotID)
	}
}

func TestUpdatePassesIDAndBody(t *testing.T) {
	t.Parallel()

	svc := &stubService{updated: &model.Category{ID: 3, Name: "New"}}
	mux := newTestMux(svc)

	rec := do(t, mux, http.MethodPut, "/api/v1/categories/3", `{"name":"New"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.gotID != 3 {
		t.Errorf("id = %d, want 3", svc.gotID)
	}
	if svc.gotUpdate == nil || svc.gotUpdate.Name == nil || *svc.gotUpdate.Name != "New" {
		t.Errorf("update body = %+v, want name New", svc.gotUpdate)
	}
}

func TestEmptyListsSerialiseAsArrays(t *testing.T) {
	t.Parallel()

	// A nil slice would encode as JSON null, which breaks clients expecting [].
	svc := &stubService{
		all:       []*model.Category{},
		tree:      []*model.CategoryNode{},
		children:  []*model.Category{},
		ancestors: []*model.Category{},
	}
	mux := newTestMux(svc)

	for _, path := range []string{
		"/api/v1/categories",
		"/api/v1/categories/tree",
		"/api/v1/categories/1/children",
		"/api/v1/categories/1/ancestors",
	} {
		rec := do(t, mux, http.MethodGet, path, "")

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
			continue
		}
		var decoded []json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Errorf("%s: body is not a JSON array: %s", path, rec.Body)
		}
	}
}

func TestDeletionImpact(t *testing.T) {
	t.Parallel()

	svc := &stubService{impact: 4}
	mux := newTestMux(svc)

	rec := do(t, mux, http.MethodGet, "/api/v1/categories/5/impact", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		ID          int64 `json:"id"`
		Descendants int   `json:"descendants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != 5 || got.Descendants != 4 {
		t.Errorf("got %+v, want id 5 with 4 descendants", got)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	t.Parallel()

	mux := newTestMux(&stubService{})

	// PATCH is not registered on the collection route.
	rec := do(t, mux, http.MethodPatch, "/api/v1/categories", "")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestStubServiceSatisfiesHandlerInterface(t *testing.T) {
	t.Parallel()

	// Compile time guard that the stub still matches the handler's contract.
	var _ CategoryService = (*stubService)(nil)
}

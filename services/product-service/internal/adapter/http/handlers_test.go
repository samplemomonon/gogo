// Package http_test covers the product-service HTTP adapters: the product
// routes, the health endpoints, and the logging/recovery middleware.
//
// The tests exercise the handlers through a real http.ServeMux so they pin the
// route patterns themselves, not just the handler functions. The product use
// cases are replaced by a stub that records the decoded request, which keeps
// the tests hermetic and lets them assert on request decoding (presence
// tracking, strict JSON) as well as response mapping.
package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	producthttp "product-service/internal/adapter/http"
	"product-service/internal/domain/product"
	productusecase "product-service/internal/usecase/product"
	"product-service/internal/version"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubUseCases implements productusecase.ProductUseCases and records the last
// decoded request so tests can assert on handler-level decoding.
type stubUseCases struct {
	createProductFn func(ctx context.Context, req *product.CreateProductRequest) (*product.Product, error)
	updateProductFn func(ctx context.Context, id int64, req *product.UpdateProductRequest) (*product.Product, error)
	deleteProductFn func(ctx context.Context, id int64) error
	adjustStockFn   func(ctx context.Context, id int64, delta int64) (*product.Product, error)
	createVariantFn func(ctx context.Context, productID int64, req *product.CreateVariantRequest) (*product.Variant, error)
	updateVariantFn func(ctx context.Context, productID, variantID int64, req *product.UpdateVariantRequest) (*product.Variant, error)
	deleteVariantFn func(ctx context.Context, productID, variantID int64) error

	lastUpdateProduct *product.UpdateProductRequest
	lastUpdateVariant *product.UpdateVariantRequest
	lastProductID     int64
	lastVariantID     int64
}

var _ productusecase.ProductUseCases = (*stubUseCases)(nil)

func (s *stubUseCases) CreateProduct(ctx context.Context, req *product.CreateProductRequest) (*product.Product, error) {
	if s.createProductFn == nil {
		return &product.Product{ID: 42}, nil
	}
	return s.createProductFn(ctx, req)
}

func (s *stubUseCases) UpdateProduct(ctx context.Context, id int64, req *product.UpdateProductRequest) (*product.Product, error) {
	s.lastProductID, s.lastUpdateProduct = id, req
	if s.updateProductFn == nil {
		return &product.Product{ID: id}, nil
	}
	return s.updateProductFn(ctx, id, req)
}

func (s *stubUseCases) DeleteProduct(ctx context.Context, id int64) error {
	if s.deleteProductFn == nil {
		return nil
	}
	return s.deleteProductFn(ctx, id)
}

func (s *stubUseCases) AdjustStock(ctx context.Context, id int64, delta int64) (*product.Product, error) {
	if s.adjustStockFn == nil {
		return &product.Product{ID: id, Stock: delta}, nil
	}
	return s.adjustStockFn(ctx, id, delta)
}

func (s *stubUseCases) CreateVariant(ctx context.Context, productID int64, req *product.CreateVariantRequest) (*product.Variant, error) {
	if s.createVariantFn == nil {
		return &product.Variant{ID: 7, ProductID: productID}, nil
	}
	return s.createVariantFn(ctx, productID, req)
}

func (s *stubUseCases) UpdateVariant(ctx context.Context, productID, variantID int64, req *product.UpdateVariantRequest) (*product.Variant, error) {
	s.lastProductID, s.lastVariantID, s.lastUpdateVariant = productID, variantID, req
	if s.updateVariantFn == nil {
		return &product.Variant{ID: variantID, ProductID: productID}, nil
	}
	return s.updateVariantFn(ctx, productID, variantID, req)
}

func (s *stubUseCases) DeleteVariant(ctx context.Context, productID, variantID int64) error {
	if s.deleteVariantFn == nil {
		return nil
	}
	return s.deleteVariantFn(ctx, productID, variantID)
}

func newProductMux(uc *stubUseCases) *http.ServeMux {
	mux := http.NewServeMux()
	producthttp.NewProductHandler(uc, testLogger()).RegisterRoutes(mux)
	return mux
}

func doRequest(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
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

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response body is not an error object: %v (body %q)", err, rec.Body.String())
	}
	return payload.Error
}

func TestCreateProduct(t *testing.T) {
	uc := &stubUseCases{}
	rec := doRequest(t, newProductMux(uc), http.MethodPost, "/api/v1/products",
		`{"category_id":3,"name":"Kopi","slug":"kopi","price_cents":15000,"currency":"IDR","stock":5}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/api/v1/products/42" {
		t.Fatalf("Location = %q, want %q", got, "/api/v1/products/42")
	}
	if body := rec.Body.String(); !strings.Contains(body, `"id":42`) {
		t.Fatalf("response must use the snake_case wire snapshot, got %s", body)
	}
}

func TestCreateProductRejectsInvalidBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed json", body: `{"name":`},
		{name: "unknown field", body: `{"name":"x","color":"red"}`},
		{name: "two objects", body: `{"name":"x"}{"name":"y"}`},
		{name: "non-object", body: `[1,2]`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uc := &stubUseCases{createProductFn: func(context.Context, *product.CreateProductRequest) (*product.Product, error) {
				t.Fatal("use case must not be called for an invalid body")
				return nil, nil
			}}
			rec := doRequest(t, newProductMux(uc), http.MethodPost, "/api/v1/products", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if msg := decodeError(t, rec); !strings.HasPrefix(msg, "invalid request body: ") {
				t.Fatalf("error = %q, want prefix %q", msg, "invalid request body: ")
			}
		})
	}
}

func TestUpdateProductDecodesPresence(t *testing.T) {
	uc := &stubUseCases{}
	rec := doRequest(t, newProductMux(uc), http.MethodPut, "/api/v1/products/9",
		`{"name":"New","price_cents":0,"brand":null}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s); a body that sets fields must not be rejected", rec.Code, http.StatusOK, rec.Body.String())
	}
	req := uc.lastUpdateProduct
	if req == nil {
		t.Fatal("use case was not called")
	}
	if uc.lastProductID != 9 {
		t.Fatalf("product id = %d, want 9", uc.lastProductID)
	}
	if !req.HasName() || req.Name == nil || *req.Name != "New" {
		t.Fatalf("name not decoded: HasName=%v Name=%v", req.HasName(), req.Name)
	}
	if !req.HasPriceCents() || req.PriceCents == nil || *req.PriceCents != 0 {
		t.Fatalf("price_cents not decoded: HasPriceCents=%v PriceCents=%v", req.HasPriceCents(), req.PriceCents)
	}
	if !req.HasBrand() || req.Brand != nil {
		t.Fatalf("brand null must be recorded as present with a nil pointer: HasBrand=%v Brand=%v", req.HasBrand(), req.Brand)
	}
	if req.HasSlug() || req.HasDescription() || req.HasStatus() {
		t.Fatal("fields absent from the body must not be reported as present")
	}
}

func TestUpdateProductEmptyBody(t *testing.T) {
	uc := &stubUseCases{updateProductFn: func(context.Context, int64, *product.UpdateProductRequest) (*product.Product, error) {
		t.Fatal("use case must not be called for an empty body")
		return nil, nil
	}}
	rec := doRequest(t, newProductMux(uc), http.MethodPut, "/api/v1/products/9", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if msg := decodeError(t, rec); msg != "request body must set at least one field" {
		t.Fatalf("error = %q", msg)
	}
}

func TestUpdateProductRejectsBadFields(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{name: "unknown field", body: `{"colour":"red"}`, wantMsg: `invalid request body: unknown field "colour"`},
		{name: "null scalar", body: `{"name":null}`, wantMsg: "invalid request body: name must be a string"},
		{name: "null category", body: `{"category_id":null}`, wantMsg: "invalid request body: category_id must be an integer"},
		{name: "wrong type", body: `{"price_cents":"free"}`, wantMsg: "invalid request body: price_cents must be an integer: json: cannot unmarshal string into Go value of type int64"},
		{name: "wrong currency type", body: `{"currency":7}`, wantMsg: "invalid request body: currency must be a string: json: cannot unmarshal number into Go value of type string"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uc := &stubUseCases{updateProductFn: func(context.Context, int64, *product.UpdateProductRequest) (*product.Product, error) {
				t.Fatal("use case must not be called for an invalid body")
				return nil, nil
			}}
			rec := doRequest(t, newProductMux(uc), http.MethodPut, "/api/v1/products/9", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if msg := decodeError(t, rec); msg != tc.wantMsg {
				t.Fatalf("error = %q, want %q", msg, tc.wantMsg)
			}
		})
	}
}

func TestUpdateProductBadID(t *testing.T) {
	for _, id := range []string{"0", "-3", "abc", "1.5"} {
		t.Run(id, func(t *testing.T) {
			uc := &stubUseCases{updateProductFn: func(context.Context, int64, *product.UpdateProductRequest) (*product.Product, error) {
				t.Fatal("use case must not be called for an invalid id")
				return nil, nil
			}}
			rec := doRequest(t, newProductMux(uc), http.MethodPut, "/api/v1/products/"+id, `{"name":"x"}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if msg := decodeError(t, rec); !strings.HasPrefix(msg, "invalid product id: ") {
				t.Fatalf("error = %q, want prefix %q", msg, "invalid product id: ")
			}
		})
	}
}

func TestDeleteProduct(t *testing.T) {
	uc := &stubUseCases{}
	rec := doRequest(t, newProductMux(uc), http.MethodDelete, "/api/v1/products/3", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", rec.Body.String())
	}
}

func TestAdjustStock(t *testing.T) {
	uc := &stubUseCases{}
	rec := doRequest(t, newProductMux(uc), http.MethodPost, "/api/v1/products/3/stock", `{"delta":-2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestAdjustStockRejectsZeroDelta(t *testing.T) {
	uc := &stubUseCases{adjustStockFn: func(context.Context, int64, int64) (*product.Product, error) {
		t.Fatal("use case must not be called for a zero delta")
		return nil, nil
	}}
	rec := doRequest(t, newProductMux(uc), http.MethodPost, "/api/v1/products/3/stock", `{"delta":0}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if msg := decodeError(t, rec); msg != "delta must not be zero" {
		t.Fatalf("error = %q", msg)
	}
}

func TestCreateVariant(t *testing.T) {
	uc := &stubUseCases{}
	rec := doRequest(t, newProductMux(uc), http.MethodPost, "/api/v1/products/5/variants",
		`{"sku":"SKU-1","name":"Small","price_cents":1000,"currency":"IDR","stock":2}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/api/v1/products/5/variants/7" {
		t.Fatalf("Location = %q, want %q", got, "/api/v1/products/5/variants/7")
	}
}

func TestUpdateVariantDecodesPresence(t *testing.T) {
	uc := &stubUseCases{}
	rec := doRequest(t, newProductMux(uc), http.MethodPut, "/api/v1/products/5/variants/7",
		`{"sku":"SKU-2","media_id":null}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	req := uc.lastUpdateVariant
	if req == nil {
		t.Fatal("use case was not called")
	}
	if uc.lastProductID != 5 || uc.lastVariantID != 7 {
		t.Fatalf("ids = (%d, %d), want (5, 7)", uc.lastProductID, uc.lastVariantID)
	}
	if !req.HasSKU() || req.SKU == nil || *req.SKU != "SKU-2" {
		t.Fatalf("sku not decoded: HasSKU=%v SKU=%v", req.HasSKU(), req.SKU)
	}
	if !req.HasMediaID() || req.MediaID != nil {
		t.Fatalf("media_id null must be present with a nil pointer: HasMediaID=%v MediaID=%v", req.HasMediaID(), req.MediaID)
	}
	if req.HasName() || req.HasPriceCents() || req.HasCurrency() || req.HasStock() {
		t.Fatal("fields absent from the body must not be reported as present")
	}
}

func TestUpdateVariantRejectsBadBodies(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    string
		wantMsg string
	}{
		{name: "empty body", path: "/api/v1/products/5/variants/7", body: `{}`, wantMsg: "request body must set at least one field"},
		{name: "unknown field", path: "/api/v1/products/5/variants/7", body: `{"barcode":"x"}`, wantMsg: `invalid request body: unknown field "barcode"`},
		{name: "null sku", path: "/api/v1/products/5/variants/7", body: `{"sku":null}`, wantMsg: "invalid request body: sku must be a string"},
		{name: "bad variant id", path: "/api/v1/products/5/variants/x", body: `{"sku":"A"}`, wantMsg: "invalid variant id: x"},
		{name: "bad product id", path: "/api/v1/products/x/variants/7", body: `{"sku":"A"}`, wantMsg: "invalid product id: x"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uc := &stubUseCases{updateVariantFn: func(context.Context, int64, int64, *product.UpdateVariantRequest) (*product.Variant, error) {
				t.Fatal("use case must not be called")
				return nil, nil
			}}
			rec := doRequest(t, newProductMux(uc), http.MethodPut, tc.path, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if msg := decodeError(t, rec); msg != tc.wantMsg {
				t.Fatalf("error = %q, want %q", msg, tc.wantMsg)
			}
		})
	}
}

func TestDeleteVariant(t *testing.T) {
	uc := &stubUseCases{}
	rec := doRequest(t, newProductMux(uc), http.MethodDelete, "/api/v1/products/5/variants/7", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// TestErrorMapping pins the translation from domain errors to HTTP responses.
// The table is the contract the frontends depend on, so each sentinel is
// exercised as a wrapped error, the way use cases return them.
func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "product not found", err: product.ErrProductNotFound, wantStatus: http.StatusNotFound, wantBody: "not found"},
		{name: "variant not found", err: product.ErrVariantNotFound, wantStatus: http.StatusNotFound, wantBody: "not found"},
		{name: "invalid input", err: product.ErrInvalidInput, wantStatus: http.StatusBadRequest, wantBody: "invalid input"},
		{name: "validation error", err: product.NewValidationError("name", product.ErrEmptyName), wantStatus: http.StatusBadRequest, wantBody: "name: name is required"},
		{name: "duplicate sku", err: product.NewValidationError("sku", product.ErrDuplicateSKU), wantStatus: http.StatusBadRequest, wantBody: "sku: duplicate sku in request"},
		{name: "max variants", err: product.ErrMaxVariantsExceeded, wantStatus: http.StatusBadRequest, wantBody: "maximum variants per product exceeded"},
		{name: "conflict", err: product.ErrConflict, wantStatus: http.StatusConflict, wantBody: "conflict"},
		{name: "outbox blocked", err: product.ErrOutboxBlocked, wantStatus: http.StatusServiceUnavailable, wantBody: "the change could not be recorded, retry"},
		{name: "dependency unavailable", err: product.ErrUnavailable, wantStatus: http.StatusServiceUnavailable, wantBody: "a required service is unavailable"},
		{name: "unknown", err: errors.New("boom"), wantStatus: http.StatusInternalServerError, wantBody: "internal server error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uc := &stubUseCases{deleteProductFn: func(context.Context, int64) error {
				return tc.err
			}}
			rec := doRequest(t, newProductMux(uc), http.MethodDelete, "/api/v1/products/1", "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if msg := decodeError(t, rec); msg != tc.wantBody {
				t.Fatalf("error = %q, want %q", msg, tc.wantBody)
			}
		})
	}
}

func TestWrappedDomainErrorMapping(t *testing.T) {
	uc := &stubUseCases{deleteProductFn: func(context.Context, int64) error {
		return fmt.Errorf("get product 3: %w", product.ErrProductNotFound)
	}}
	rec := doRequest(t, newProductMux(uc), http.MethodDelete, "/api/v1/products/3", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; errors.Is must see through wrapping", rec.Code, http.StatusNotFound)
	}
}

func TestHealthEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	checkErr := error(nil)
	producthttp.NewHealthHandler(func(context.Context) error { return checkErr }, testLogger()).RegisterRoutes(mux)

	t.Run("liveness", func(t *testing.T) {
		rec := doRequest(t, mux, http.MethodGet, "/healthz", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var payload map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload["status"] != "ok" {
			t.Fatalf("status = %q, want %q", payload["status"], "ok")
		}
	})

	t.Run("readiness ok", func(t *testing.T) {
		rec := doRequest(t, mux, http.MethodGet, "/readyz", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	})

	t.Run("readiness failing", func(t *testing.T) {
		checkErr = errors.New("database unreachable")
		defer func() { checkErr = nil }()
		rec := doRequest(t, mux, http.MethodGet, "/readyz", "")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
		}
	})

	t.Run("version", func(t *testing.T) {
		rec := doRequest(t, mux, http.MethodGet, "/version", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		var payload map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload["version"] != version.Short() {
			t.Fatalf("version = %q, want %q", payload["version"], version.Short())
		}
		if payload["build"] != version.String() {
			t.Fatalf("build = %q, want %q", payload["build"], version.String())
		}
	})
}

func TestSecurityHeaders(t *testing.T) {
	handler := producthttp.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want %q", got, "DENY")
	}
}

func TestRecoveryMiddleware(t *testing.T) {
	handler := producthttp.Recovery(testLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if msg := decodeError(t, rec); msg != "internal server error" {
		t.Fatalf("error = %q", msg)
	}
}

func TestLoggingMiddlewarePreservesResponse(t *testing.T) {
	handler := producthttp.Logging(testLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("body"))
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if rec.Body.String() != "body" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "body")
	}
}

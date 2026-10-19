// Package detail covers what the detail handler owns: the id parse, the
// failure-to-status mapping (not found is a 404, an outage a 503, anything
// else a 500, all before the composer writes anything), and the fact that a
// successful page is composed as a measured island inside the shell rather
// than hand-rendered to a private buffer.
package detail

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"gogo/internal/features/home/ui/application/ports"
	"gogo/internal/features/home/ui/application/results"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/web/pagekit"
)

// stubListing is an in-memory ports.Listing returning one fixed product (or
// one fixed error), counting the calls so "the id never reached the port" can
// be asserted.
type stubListing struct {
	product *results.Product
	err     error
	calls   int
}

var _ ports.Listing = (*stubListing)(nil)

func (s *stubListing) ListPage(context.Context, ports.Query) (results.Listing, error) {
	return results.Listing{}, nil
}

func (s *stubListing) GetProduct(context.Context, string) (*results.Product, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.product, nil
}

func newTestHandler(t *testing.T, listing ports.Listing) *Handler {
	t.Helper()
	return NewHandler(listing, pagekit.NewComposer(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func get(t *testing.T, handler *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()

	router := chi.NewRouter()
	router.Get("/catalog/{id}", handler.Handle)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestAProductIsComposedAsAMeasuredIsland pins the composition: the page
// arrives through the composer, so the product region carries its ds-island
// marker and its Server-Timing entry, and the response carries the
// negotiation headers — the properties the hand-rendered buffer never had.
func TestAProductIsComposedAsAMeasuredIsland(t *testing.T) {
	t.Parallel()

	stub := &stubListing{product: &results.Product{Name: "Aurora Phone", Price: 12999000, Stock: 3}}
	rec := get(t, newTestHandler(t, stub), "/catalog/7")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Aurora Phone") {
		t.Errorf("body does not contain the product name")
	}
	if !strings.Contains(body, `data-island="home.product-detail:7"`) {
		t.Errorf("body does not carry the island marker for the requested product")
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "HX-Request") {
		t.Errorf("Vary = %q, want HX-Request", got)
	}
	if got := rec.Header().Get("Server-Timing"); !strings.Contains(got, "island-home.product-detail_7;dur=") {
		t.Errorf("Server-Timing = %q, want the island's timing entry", got)
	}
}

func TestAMissingProductAnswers404(t *testing.T) {
	t.Parallel()

	stub := &stubListing{err: productconsumer.ErrNotFound}
	rec := get(t, newTestHandler(t, stub), "/catalog/7")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestAnOutageAnswers503(t *testing.T) {
	t.Parallel()

	stub := &stubListing{err: productconsumer.ErrProductPlatformUnavailable}
	rec := get(t, newTestHandler(t, stub), "/catalog/7")

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// TestAnInvalidIdNeverReachesThePort keeps the cheap rejection cheap: a
// non-numeric id is a 404 without a catalog round trip.
func TestAnInvalidIdNeverReachesThePort(t *testing.T) {
	t.Parallel()

	stub := &stubListing{product: &results.Product{Name: "Aurora Phone"}}
	rec := get(t, newTestHandler(t, stub), "/catalog/not-a-number")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if stub.calls != 0 {
		t.Errorf("port was called %d times for an invalid id, want 0", stub.calls)
	}
}

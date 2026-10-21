package homesearch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"gogo/internal/features/home/ui/application/ports"
	"gogo/internal/features/home/ui/application/results"
)

// fakeListing is a stub ports.Listing for the live-search endpoint. It returns a
// seeded page for a matching term so the test can assert the fragment shapes, and
// fails when the test wants to exercise degradation.
type fakeListing struct {
	page      results.Listing
	listErr   error
	lastQuery ports.Query
	listCalls int
}

func (f *fakeListing) ListPage(_ context.Context, q ports.Query) (results.Listing, error) {
	f.listCalls++
	f.lastQuery = q
	if f.listErr != nil {
		return results.Listing{}, f.listErr
	}
	return f.page, nil
}

func (f *fakeListing) GetProduct(_ context.Context, _ string) (*results.Product, error) {
	return nil, errors.New("not supported by homesearch")
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newRouter(listing ports.Listing) chi.Router {
	r := chi.NewRouter()
	r.Get("/search/suggest", NewHandler(listing, discardLogger()).Handle)
	return r
}

func TestLiveSearchRendersResultsFragment(t *testing.T) {
	listing := &fakeListing{page: results.Listing{Products: []*results.Product{
		{ID: "7", Name: "Aurora Phone", Brand: "Aurora", Price: 1000000, Currency: "IDR", ImageURL: "https://minio.local/gogo/9.jpg"},
	}}}
	router := newRouter(listing)

	req := httptest.NewRequest(http.MethodGet, "/search/suggest?q=aurora", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// The response is a bare fragment, not a page shell.
	if strings.Contains(body, "<html") {
		t.Errorf("live search must not render a page shell; got:\n%s", body)
	}
	for _, want := range []string{"Aurora Phone", `/catalog/7`, `<img src="https://minio.local/gogo/9.jpg"`} {
		if !strings.Contains(body, want) {
			t.Errorf("live search fragment missing %q; got:\n%s", want, body)
		}
	}
	// It must route to the search read model with the typed term.
	if listing.lastQuery.Term != "aurora" {
		t.Errorf("query term = %q, want %q", listing.lastQuery.Term, "aurora")
	}
	if listing.lastQuery.Limit != 8 {
		t.Errorf("query limit = %d, want 8 (a small live page)", listing.lastQuery.Limit)
	}
}

func TestLiveSearchShortTermShowsKeepTyping(t *testing.T) {
	listing := &fakeListing{}
	router := newRouter(listing)

	req := httptest.NewRequest(http.MethodGet, "/search/suggest?q=au", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if listing.listCalls != 0 {
		t.Errorf("list called for a %d-char term; want no round trip", listing.listCalls)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Terus mengetik") {
		t.Errorf("short term should show the keep-typing state; got:\n%s", body)
	}
}

// A listing outage degrades to the quiet state rather than an error page: the
// shopper typed a search, they did not ask for an error, and the sheet must not
// break.
func TestLiveSearchDegradesOnListingOutage(t *testing.T) {
	listing := &fakeListing{listErr: errors.New("platform down")}
	router := newRouter(listing)

	req := httptest.NewRequest(http.MethodGet, "/search/suggest?q=aurora", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even on outage", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Pencarian sedang tidak tersedia") {
		t.Errorf("outage should show the unavailable state; got:\n%s", body)
	}
	if strings.Contains(body, "Aurora Phone") {
		t.Errorf("outage leaked a stale result row")
	}
}

func TestLiveSearchEmptyTermShowsPrompt(t *testing.T) {
	listing := &fakeListing{}
	router := newRouter(listing)

	req := httptest.NewRequest(http.MethodGet, "/search/suggest", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "Mulai mengetik") {
		t.Errorf("empty term should show the keep-typing state; got:\n%s", body)
	}
}

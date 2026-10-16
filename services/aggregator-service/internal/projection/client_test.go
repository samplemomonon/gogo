package projection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aggregator-service/internal/graph/model"
)

func TestListingStatusDecodesProductsField(t *testing.T) {
	// The two services use different names for the record count: "products" here,
	// "documents" in search. Decoding into separate structs is what makes a contract
	// change fail loudly rather than reading a silent zero.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/listing/status" {
			t.Errorf("path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"products":       128,
			"applied_events": 4096,
			"healthy":        true,
		})
	}))
	defer srv.Close()

	reporter := New(Options{ListingBase: srv.URL, SearchBase: srv.URL, Timeout: time.Second})

	got, err := reporter.ListingStatus(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Records != 128 {
		t.Fatalf("records %d, want 128", got.Records)
	}
	if got.AppliedEvents != 4096 {
		t.Fatalf("applied events %d, want 4096", got.AppliedEvents)
	}
	if !got.Healthy {
		t.Fatal("healthy false")
	}
	if got.Source != "product-listing-service" {
		t.Fatalf("source %q", got.Source)
	}
}

func TestSearchStatusDecodesDocumentsField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search/status" {
			t.Errorf("path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"documents":      130,
			"applied_events": 4097,
			"healthy":        true,
		})
	}))
	defer srv.Close()

	reporter := New(Options{ListingBase: srv.URL, SearchBase: srv.URL, Timeout: time.Second})

	got, err := reporter.SearchStatus(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Records != 130 {
		t.Fatalf("records %d, want 130", got.Records)
	}
	if got.Source != "product-search-service" {
		t.Fatalf("source %q", got.Source)
	}
}

func TestUnhealthyProjectionIsReportedNotHidden(t *testing.T) {
	// A stalled projection does not error: reads keep succeeding with stale data. The
	// unhealthy flag is the only signal, so it has to survive.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"products":100,"applied_events":100,"healthy":false}`))
	}))
	defer srv.Close()

	reporter := New(Options{ListingBase: srv.URL, SearchBase: srv.URL})

	got, err := reporter.ListingStatus(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Healthy {
		t.Fatal("healthy should be false")
	}
}

func TestNonOKStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	reporter := New(Options{ListingBase: srv.URL, SearchBase: srv.URL})

	if _, err := reporter.ListingStatus(t.Context()); err == nil {
		t.Fatal("a 500 should be an error, not a zero status")
	}
}

func TestMalformedJSONIsAnError(t *testing.T) {
	// A zero-valued ProjectionStatus would read as "no products, nothing applied,
	// not healthy", which is a claim about the projection rather than a failure to
	// find out. Failing is the honest answer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	reporter := New(Options{ListingBase: srv.URL, SearchBase: srv.URL})

	if _, err := reporter.SearchStatus(t.Context()); err == nil {
		t.Fatal("malformed JSON should be an error")
	}
}

func TestTrailingSlashInBaseIsNormalised(t *testing.T) {
	// A base URL written with a trailing slash would otherwise produce a double slash
	// in the path, which some routers 404 on.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "//") {
			t.Errorf("double slash in path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"products":1,"applied_events":1,"healthy":true}`))
	}))
	defer srv.Close()

	reporter := New(Options{ListingBase: srv.URL + "/", SearchBase: srv.URL + "/"})

	if _, err := reporter.ListingStatus(t.Context()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	// Following a redirect from a status endpoint could send a request to an address
	// nobody reviewed. Treating the redirect as an error keeps the target explicit.
	var target *httptest.Server
	target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the redirect target should never be reached")
		_, _ = w.Write([]byte(`{"products":1,"applied_events":1,"healthy":true}`))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	reporter := New(Options{ListingBase: redirector.URL, SearchBase: redirector.URL})

	if _, err := reporter.ListingStatus(t.Context()); err == nil {
		t.Fatal("a redirect should be reported as an error, not followed")
	}
}

func TestTimeoutIsBounded(t *testing.T) {
	// A hanging status endpoint must not hold a request open. This call sits on the
	// path of every write response, so an unbounded wait here is a write path stall.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	defer close(release)

	reporter := New(Options{ListingBase: srv.URL, SearchBase: srv.URL, Timeout: 50 * time.Millisecond})

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	started := time.Now()
	if _, err := reporter.ListingStatus(ctx); err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("took %s; the per-call timeout should have fired well before the ctx deadline", elapsed)
	}
}

func TestOversizedBodyIsRejected(t *testing.T) {
	// The endpoints return a small object. A huge body means something is wrong, and
	// reading it into memory is how that becomes an outage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"products":1,"padding":"` + strings.Repeat("x", maxResponseBytes*2) + `"}`))
	}))
	defer srv.Close()

	reporter := New(Options{ListingBase: srv.URL, SearchBase: srv.URL, Timeout: 2 * time.Second})

	if _, err := reporter.ListingStatus(t.Context()); err == nil {
		t.Fatal("an oversized body should be rejected")
	}
}

func TestBothStatusesAreIndependent(t *testing.T) {
	// One lagging does not mean the other has lost data, so a failure in one must not
	// suppress the other.
	listing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"products":10,"applied_events":20,"healthy":true}`))
	}))
	defer listing.Close()

	search := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer search.Close()

	reporter := New(Options{ListingBase: listing.URL, SearchBase: search.URL, Timeout: time.Second})

	gotListing, err := reporter.ListingStatus(t.Context())
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if gotListing.Records != 10 {
		t.Fatalf("listing records %d", gotListing.Records)
	}
	if _, err := reporter.SearchStatus(t.Context()); err == nil {
		t.Fatal("search should have failed independently")
	}
}

// Compile-time check that the reporter satisfies the port the graph layer declares.
var _ interface {
	ListingStatus(ctx context.Context) (model.ProjectionStatus, error)
	SearchStatus(ctx context.Context) (model.ProjectionStatus, error)
} = (*Reporter)(nil)

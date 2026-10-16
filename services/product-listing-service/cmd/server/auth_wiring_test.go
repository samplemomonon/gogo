package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/metrics"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/ratelimit"
)

// The guard's own behaviour is covered by the serviceauth package's tests. What
// these cover is that this service installs it — the seam that disappears
// silently when someone edits the middleware chain.

const inboundToken = "test-inbound-service-token-value"

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A refused request must not reach the handler. The counter is the point: an
// authentication control that reports on a request after letting it through is
// not a control.
func TestGuardedHandlerStopsAnUnauthenticatedRead(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/products", func(http.ResponseWriter, *http.Request) { reached++ })

	rec := httptest.NewRecorder()
	guardedHandler(mux, []string{inboundToken}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/api/v1/products", nil))

	if reached != 0 {
		t.Fatalf("the handler ran %d time(s) without a credential", reached)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestGuardedHandlerAdmitsACredentialedRead(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/products", func(http.ResponseWriter, *http.Request) { reached++ })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/products", nil)
	req.Header.Set("Authorization", "Bearer "+inboundToken)

	rec := httptest.NewRecorder()
	guardedHandler(mux, []string{inboundToken}).ServeHTTP(rec, req)

	if reached != 1 {
		t.Errorf("the handler ran %d time(s), want 1", reached)
	}
}

// probePaths is the list production passes, so this reads the same value the
// service does rather than restating it.
func TestProbesStayOpenThroughTheRealGuard(t *testing.T) {
	for _, path := range probePaths {
		var reached bool
		mux := http.NewServeMux()
		mux.HandleFunc("GET "+path, func(http.ResponseWriter, *http.Request) { reached = true })

		rec := httptest.NewRecorder()
		guardedHandler(mux, []string{inboundToken}).ServeHTTP(
			rec, httptest.NewRequest(http.MethodGet, path, nil))

		if !reached {
			t.Errorf("%s was refused through the production guard", path)
		}
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s answered 401; an orchestrator would restart a healthy container", path)
		}
	}
}

func TestAnExemptProbeDoesNotOpenTheAPISurface(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/products", func(http.ResponseWriter, *http.Request) {})

	rec := httptest.NewRecorder()
	guardedHandler(mux, []string{inboundToken}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/api/v1/products", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// The chain itself, not just the guard it wraps.
//
// The tests above cover guardedHandler; this covers the call site, which is the
// half that disappears when someone edits the middleware chain. It was verified
// by sabotage: neutralising guardedHandler(mux, inboundTokens) inside
// middlewareChain left the whole suite green until this existed.
func TestTheChainRefusesAnUnauthenticatedRead(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/products/{id}", func(http.ResponseWriter, *http.Request) { reached++ })

	limiter := ratelimit.New(ratelimit.Options{Rate: 1000, Burst: 1000})
	defer limiter.Stop()

	h := middlewareChain(mux, testLog(), metrics.New(), limiter, []string{inboundToken})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/products/42", nil))

	if reached != 0 {
		t.Fatalf("the handler ran %d time(s) through the production chain", reached)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestTheChainStillServesProbes(t *testing.T) {
	var reached bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(http.ResponseWriter, *http.Request) { reached = true })

	limiter := ratelimit.New(ratelimit.Options{Rate: 1000, Burst: 1000})
	defer limiter.Stop()

	h := middlewareChain(mux, testLog(), metrics.New(), limiter, []string{inboundToken})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if !reached {
		t.Error("/health was refused through the production chain")
	}
	if rec.Code == http.StatusUnauthorized {
		t.Error("/health answered 401 through the production chain")
	}
}

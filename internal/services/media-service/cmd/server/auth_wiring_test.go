package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The guard's own behaviour is covered by the serviceauth package's tests. What
// these cover is that this service installs it — the seam that disappears
// silently when someone edits the middleware chain.

const inboundToken = "test-inbound-service-token-value"

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A refused request must not reach the handler. The counter is the point: an
// authentication control that reports on a request after letting it through is
// not a control.
func TestGuardedHandlerStopsAnUnauthenticatedWrite(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/categories", func(http.ResponseWriter, *http.Request) { reached++ })

	rec := httptest.NewRecorder()
	guardedHandler(mux, []string{inboundToken}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodPost, "/api/v1/categories", nil))

	if reached != 0 {
		t.Fatalf("the write handler ran %d time(s) without a credential", reached)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestGuardedHandlerAdmitsACredentialedWrite(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/categories", func(http.ResponseWriter, *http.Request) { reached++ })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/categories", nil)
	req.Header.Set("Authorization", "Bearer "+inboundToken)

	rec := httptest.NewRecorder()
	guardedHandler(mux, []string{inboundToken}).ServeHTTP(rec, req)

	if reached != 1 {
		t.Errorf("the write handler ran %d time(s), want 1", reached)
	}
}

// probePaths is the list production passes, so this reads the same value rather
// than restating it. Removing a probe from it would make an orchestrator restart
// a healthy container.
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
	mux.HandleFunc("POST /api/v1/categories", func(http.ResponseWriter, *http.Request) {})

	rec := httptest.NewRecorder()
	guardedHandler(mux, []string{inboundToken}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodPost, "/api/v1/categories", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// The chain itself, not just the guard it wraps.
//
// The tests above cover guardedHandler; this covers the call site, which is the
// half that disappears when someone edits the middleware chain. Verified by
// sabotage: neutralising guardedHandler(mux, inboundTokens) inside buildHandler
// left the whole suite green until this existed.
func TestTheChainRefusesAnUnauthenticatedWrite(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/thing", func(http.ResponseWriter, *http.Request) { reached++ })

	rec := httptest.NewRecorder()
	wrapChain(mux, testLog(), []string{inboundToken}).ServeHTTP(
		rec, httptest.NewRequest(http.MethodPost, "/api/v1/thing", nil))

	if reached != 0 {
		t.Fatalf("the write handler ran %d time(s) through the production chain", reached)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestTheChainAdmitsACredentialedWrite(t *testing.T) {
	var reached int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/thing", func(http.ResponseWriter, *http.Request) { reached++ })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/thing", nil)
	req.Header.Set("Authorization", "Bearer "+inboundToken)

	rec := httptest.NewRecorder()
	wrapChain(mux, testLog(), []string{inboundToken}).ServeHTTP(rec, req)

	if reached != 1 {
		t.Errorf("the write handler ran %d time(s), want 1", reached)
	}
}

func TestTheChainStillServesProbes(t *testing.T) {
	for _, path := range probePaths {
		var reached bool
		mux := http.NewServeMux()
		mux.HandleFunc("GET "+path, func(http.ResponseWriter, *http.Request) { reached = true })

		rec := httptest.NewRecorder()
		wrapChain(mux, testLog(), []string{inboundToken}).ServeHTTP(
			rec, httptest.NewRequest(http.MethodGet, path, nil))

		if !reached {
			t.Errorf("%s was refused through the production chain", path)
		}
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("%s answered 401; an orchestrator would restart a healthy container", path)
		}
	}
}

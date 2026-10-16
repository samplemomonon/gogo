package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"product-search-service/internal/httpx"
)

// These cover the infrastructure copied from the sibling read services. They are
// thin, but each has a behaviour that is easy to break silently: the liveness probe
// must not depend on the database, or an outage becomes a restart loop, and the
// recovery middleware must not take the process down with it.
func TestLivenessDoesNotDependOnTheDatabase(t *testing.T) {
	t.Parallel()

	probe := func(context.Context) error { return errors.New("database is down") }
	mux := http.NewServeMux()
	NewHealthHandler(probe, testLogger()).RegisterRoutes(mux)

	for _, path := range []string{"/health", "/healthz"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 even with the database down", path, recorder.Code)
		}
	}
}

func TestReadinessFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	t.Parallel()

	probe := func(context.Context) error { return errors.New("database is down") }
	mux := http.NewServeMux()
	NewHealthHandler(probe, testLogger()).RegisterRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 so the instance leaves the load balancer", recorder.Code)
	}
}

func TestReadinessSucceedsWhenTheDatabaseAnswers(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	NewHealthHandler(func(context.Context) error { return nil }, testLogger()).RegisterRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", recorder.Code)
	}
}

func TestRecoveryTurnsAPanicInto500(t *testing.T) {
	t.Parallel()

	handler := Recovery(testLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/search/products", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 rather than a dead process", recorder.Code)
	}
	// The panic text is for the log, not the caller.
	if body := recorder.Body.String(); body != "" && json.Valid(recorder.Body.Bytes()) {
		var body2 httpx.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &body2); err == nil && body2.Error != "internal server error" {
			t.Errorf("body = %s, want a generic message", body)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := recorder.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
}

// The status endpoint is the only way to tell an empty index from a broken one, so
// its failure has to be visible rather than reported as a healthy zero.
func TestStatusReportsAnUnqueryableIndexAsUnhealthy(t *testing.T) {
	t.Parallel()

	failing := statusReporterFuncs{
		documents: func(context.Context) (int64, error) { return 0, errors.New("no such table") },
		events:    func(context.Context) (int64, error) { return 0, nil },
	}

	mux := http.NewServeMux()
	NewStatusHandler(failing, testLogger()).RegisterRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/search/status", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", recorder.Code)
	}
	var body StatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Healthy {
		t.Error("healthy = true, want false: an unqueryable index must not look healthy")
	}
}

func TestStatusReportsCounts(t *testing.T) {
	t.Parallel()

	reporter := statusReporterFuncs{
		documents: func(context.Context) (int64, error) { return 128, nil },
		events:    func(context.Context) (int64, error) { return 402, nil },
	}

	mux := http.NewServeMux()
	NewStatusHandler(reporter, testLogger()).RegisterRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/search/status", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var body StatusResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Documents != 128 || body.AppliedEvents != 402 || !body.Healthy {
		t.Errorf("body = %+v, want 128 documents, 402 events, healthy", body)
	}
}

// statusReporterFuncs builds a StatusReporter from two functions, mirroring how
// main wires the engine and the projector in.
type statusReporterFuncs struct {
	documents func(context.Context) (int64, error)
	events    func(context.Context) (int64, error)
}

func (r statusReporterFuncs) Documents(ctx context.Context) (int64, error) {
	return r.documents(ctx)
}

func (r statusReporterFuncs) AppliedEvents(ctx context.Context) (int64, error) {
	return r.events(ctx)
}

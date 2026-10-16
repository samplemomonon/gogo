package handler_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"media-service/internal/handler"
)

// TestRecoveryTurnsAPanicIntoA500 pins the containment guarantee: a panicking
// handler must not take the process down, and the stack trace must go to the
// log rather than the client.
func TestRecoveryTurnsAPanicIntoA500(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logged, nil))

	h := handler.Recovery(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	decodeInto(t, rec, &body)
	if body["error"] != "internal server error" {
		t.Errorf("body = %v, want the generic message", body)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("body %q leaked the panic value", rec.Body.String())
	}

	logLine := logged.String()
	if !strings.Contains(logLine, "boom") || !strings.Contains(logLine, "stack") {
		t.Errorf("log %q should carry the panic value and a stack trace", logLine)
	}
}

// TestRecoveryPassesThroughHealthyRequests pins that the middleware does not
// alter a response when nothing panics.
func TestRecoveryPassesThroughHealthyRequests(t *testing.T) {
	t.Parallel()

	h := handler.Recovery(quietLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want unchanged", rec.Body.String())
	}
}

// TestLoggingRecordsOneLinePerRequest pins the observability contract: one
// structured record per request carrying the method, path, status, byte count
// and duration. A handler that writes without an explicit status must be
// logged as 200.
func TestLoggingRecordsOneLinePerRequest(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logged, nil))

	h := handler.Logging(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/media", nil))

	var record map[string]any
	if err := json.Unmarshal(logged.Bytes(), &record); err != nil {
		t.Fatalf("log %q is not one JSON record: %v", logged.String(), err)
	}

	for key, want := range map[string]any{
		"method": "GET",
		"path":   "/api/v1/media",
		"status": float64(200),
		"bytes":  float64(5),
	} {
		if got := record[key]; got != want {
			t.Errorf("log field %s = %v, want %v", key, got, want)
		}
	}
	if _, ok := record["duration_ms"]; !ok {
		t.Errorf("log %q should carry duration_ms", logged.String())
	}
}

// TestSecurityHeaders pins the two conservative response headers.
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	h := handler.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
}

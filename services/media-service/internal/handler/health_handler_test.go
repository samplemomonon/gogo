// Package handler_test pins the HTTP edge of the media service: the health
// endpoints' status vocabulary, the middleware guarantees (panic containment,
// security headers, request logging), and the handler's translation between
// the wire format and the service interface.
package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"media-service/internal/handler"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()

	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("response body %q is not valid JSON: %v", rec.Body.String(), err)
	}
}

// TestLivezAnswersWithoutDependencies pins the liveness contract: it answers
// as long as the process is up. It deliberately takes no probe, so a database
// outage cannot trigger a restart loop.
func TestLivezAnswersWithoutDependencies(t *testing.T) {
	t.Parallel()

	h := handler.NewHealthHandler(func(context.Context) error {
		t.Error("liveness probe consulted a dependency")
		return nil
	}, quietLogger())

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	for _, path := range []string{"/health", "/healthz"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		var body map[string]string
		decodeInto(t, rec, &body)
		if body["status"] != "healthy" {
			t.Errorf("GET %s body = %v, want status healthy", path, body)
		}
	}
}

// TestReadyzReportsTheProbe pins both readiness outcomes: 200 when the probe
// succeeds and 503 when it fails, with the failure not leaking its detail.
func TestReadyzReportsTheProbe(t *testing.T) {
	t.Parallel()

	t.Run("healthy", func(t *testing.T) {
		t.Parallel()

		h := handler.NewHealthHandler(func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("probe context has no deadline; a hanging dependency could hang the endpoint")
			}
			return nil
		}, quietLogger())

		rec := httptest.NewRecorder()
		h.Readyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
		var body map[string]string
		decodeInto(t, rec, &body)
		if body["status"] != "ready" {
			t.Errorf("body = %v, want status ready", body)
		}
	})

	t.Run("dependency down", func(t *testing.T) {
		t.Parallel()

		h := handler.NewHealthHandler(func(context.Context) error {
			return errors.New("pq: connection refused")
		}, quietLogger())

		rec := httptest.NewRecorder()
		h.Readyz(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
		var body map[string]string
		decodeInto(t, rec, &body)
		if body["status"] != "unavailable" {
			t.Errorf("body = %v, want status unavailable", body)
		}
		if body["error"] != "" {
			t.Errorf("body = %v should not carry the probe's error text", body)
		}
	})
}

// TestVersionReportsBuildMetadata pins the /version shape the aggregator and
// compose health checks read.
func TestVersionReportsBuildMetadata(t *testing.T) {
	t.Parallel()

	h := handler.NewHealthHandler(nil, quietLogger())

	rec := httptest.NewRecorder()
	h.Version(rec, httptest.NewRequest(http.MethodGet, "/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	decodeInto(t, rec, &body)
	if body["version"] == "" || body["build"] == "" {
		t.Errorf("body = %v, want non-empty version and build", body)
	}
}

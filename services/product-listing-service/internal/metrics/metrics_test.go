package metrics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// New must be repeatable. promauto registers into prometheus.DefaultRegisterer, a
// process-global, so the previous implementation panicked on the second call with
// "duplicate metrics collector registration attempted". That made any second
// construction a startup crash — including from a test — rather than an obvious
// error.
func TestNewIsRepeatable(t *testing.T) {
	t.Parallel()

	// No defer/recover: a panic here is the failure, and letting it propagate says
	// so directly rather than as a tidy test failure.
	first := New()
	second := New()

	if first.registry == second.registry {
		t.Error("two Metrics share a registry; they are not independent")
	}

	// Each instance counts independently.
	first.HTTPRequestsTotal.WithLabelValues("GET", "/x", "200").Inc()
	second.HTTPRequestsTotal.WithLabelValues("GET", "/x", "200").Inc()

	if got := gatherValue(t, first, "product_listing_http_requests_total"); got != "1" {
		t.Errorf("first registry has %s, want 1", got)
	}
	if got := gatherValue(t, second, "product_listing_http_requests_total"); got != "1" {
		t.Errorf("second registry has %s, want 1", got)
	}
}

// The collectors must be reachable through the handler, which is the only way they
// are ever exported.
func TestHandlerServesThePrivateRegistry(t *testing.T) {
	t.Parallel()

	m := New()
	m.HTTPRequestsTotal.WithLabelValues("GET", "/api/v1/products/{id}", "200").Inc()
	// A Prometheus family with no observations is not exposed at all, so each
	// collector has to be touched before it can be asserted on.
	m.HTTPRequestDuration.WithLabelValues("GET", "/api/v1/products/{id}").Observe(0.01)
	m.ProjectionEventsTotal.WithLabelValues("product.v1.product.created", "applied").Inc()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"product_listing_http_requests_total",
		"product_listing_http_request_duration_seconds_bucket",
		"product_listing_projection_events_total",
	} {
		if !contains(body, want) {
			t.Errorf("/metrics does not expose %s", want)
		}
	}
}

// RecordDBQuery classified by comparing the error's text. That silently degrades
// to "unknown" if the driver changes its wording, and a metric that stops
// classifying looks exactly like one reporting a new failure.
func TestRecordDBQueryClassifiesBySentinelNotByMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "wrapped connection error keeps its class",
			err:  fmt.Errorf("query products: %w", sql.ErrConnDone),
			want: "connection",
		},
		{
			name: "driver bad connection keeps its class",
			err:  fmt.Errorf("query products: %w", driver.ErrBadConn),
			want: "connection",
		},
		{
			name: "a deadline is a timeout",
			err:  fmt.Errorf("query products: %w", context.DeadlineExceeded),
			want: "timeout",
		},
		{
			name: "an unrecognised error is unknown",
			err:  fmt.Errorf("something else"),
			want: "unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := New()
			m.RecordDBQuery("GetByID", time.Millisecond, tc.err)

			body := gatherText(t, m)
			want := `product_listing_db_errors_total{error_type="` + tc.want + `",operation="GetByID"} 1`
			if !contains(body, want) {
				t.Errorf("expected %s in:\n%s", want, body)
			}
		})
	}
}

// A nil error records no error series at all.
func TestRecordDBQueryWithoutAnError(t *testing.T) {
	t.Parallel()

	m := New()
	m.RecordDBQuery("GetByID", time.Millisecond, nil)

	if body := gatherText(t, m); contains(body, "product_listing_db_errors_total") {
		t.Error("a successful query recorded a db error")
	}
}

package httpserver

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"gogo/internal/shared/breaker"
)

func promBody(t *testing.T, metrics *Metrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want Prometheus text", ct)
	}
	return rec.Body.String()
}

func TestMetricsMiddlewareRecordsRequests(t *testing.T) {
	metrics := NewMetrics(nil)

	router := chi.NewRouter()
	router.Use(metrics.Middleware)
	router.Get("/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))

	body := promBody(t, metrics)
	for _, want := range []string{
		"# HELP gogo_http_requests_total",
		`gogo_http_requests_total{method="GET",code="200",route="/hello"} 1`,
		"# HELP gogo_http_inflight_requests",
		"gogo_http_inflight_requests 0",
		"# TYPE gogo_http_request_duration_seconds histogram",
		"gogo_http_request_duration_seconds_bucket{le=\"+Inf\"} 1",
		"gogo_http_request_duration_seconds_count 1",
		"# HELP gogo_goroutines",
		"gogo_goroutines ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsRecordsMultipleStatusesAndUnmatched(t *testing.T) {
	metrics := NewMetrics(nil)

	router := chi.NewRouter()
	router.Use(metrics.Middleware)
	router.Get("/only-get", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/only-get", nil))

	// POST matches the path but not a registered method: the router answers
	// 405 outside any named pattern, recorded as route "unmatched".
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/only-get", nil))

	// A path that matches no route pattern.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	body := promBody(t, metrics)
	for _, want := range []string{
		`gogo_http_requests_total{method="GET",code="200",route="/only-get"} 1`,
		`gogo_http_requests_total{method="POST",code="405",route="unmatched"} 1`,
		`gogo_http_requests_total{method="GET",code="404",route="unmatched"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q:\n%s", want, body)
		}
	}
}

func TestMetricsSnapshotIsJSON(t *testing.T) {
	metrics := NewMetrics(nil)

	router := chi.NewRouter()
	router.Use(metrics.Middleware)
	router.Get("/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello", nil))

	snapshot := metrics.String()
	for _, want := range []string{`"requests_total"`, `"request_count"`, `"uptime_seconds"`, `"inflight"`} {
		if !strings.Contains(snapshot, want) {
			t.Errorf("expvar snapshot missing %q: %s", want, snapshot)
		}
	}
}

// A Prometheus metric name may only contain [a-zA-Z0-9_:]. The product platform's
// services are named with hyphens ("product-listing"), so a raw upstream name would
// produce a line Prometheus rejects — and a rejected line fails the whole scrape, taking
// every other series with it. This pins that the exposition is always scrapeable.
func TestPrometheusExpositionUsesValidMetricNames(t *testing.T) {
	metrics := NewMetrics(map[string]*breaker.Breaker{
		"product":         breaker.NewDefault(),
		"product-listing": breaker.NewDefault(),
		"product-search":  breaker.NewDefault(),
		"category":        breaker.NewDefault(),
		"inventory":       breaker.NewDefault(),
	})

	body := promBody(t, metrics)

	// A Prometheus metric name: must start with a letter/colon/underscore and contain
	// nothing else. Labels are stripped first — a bucket line legitimately carries
	// {le="0.5"}, and that is a label, not part of the name.
	valid := regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, found := strings.Cut(line, " ")
		if !found {
			t.Errorf("exposition line has no value: %q", line)
			continue
		}
		withoutLabels, _, _ := strings.Cut(name, "{")
		if !valid.MatchString(withoutLabels) {
			t.Errorf("invalid Prometheus metric name %q in: %q", withoutLabels, line)
		}
	}

	// The hyphenated upstreams must still appear, under their sanitized names.
	for _, want := range []string{
		"gogo_product_listing_breaker_state",
		"gogo_product_search_breaker_state",
		"gogo_category_breaker_state",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q", want)
		}
	}

	// And the HELP text keeps the real service name, so a human still knows which
	// dependency a series belongs to.
	if !strings.Contains(body, "product-listing calls") {
		t.Error("the HELP text lost the upstream's real name")
	}
}

// Every configured upstream must be reported. A dependency silently missing from /metrics
// is one nobody can alert on.
func TestEveryUpstreamIsReported(t *testing.T) {
	metrics := NewMetrics(map[string]*breaker.Breaker{
		"product":         breaker.NewDefault(),
		"product-listing": breaker.NewDefault(),
		"inventory":       breaker.NewDefault(),
	})

	body := promBody(t, metrics)
	for _, upstream := range []string{"product", "product_listing", "inventory"} {
		for _, suffix := range []string{"_breaker_state", "_breaker_successes_total",
			"_breaker_failures_total", "_breaker_rejects_total", "_breaker_trips_total"} {
			if !strings.Contains(body, "gogo_"+upstream+suffix) {
				t.Errorf("exposition missing gogo_%s%s", upstream, suffix)
			}
		}
	}
}

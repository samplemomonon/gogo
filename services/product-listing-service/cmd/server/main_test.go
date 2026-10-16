package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/cache"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/metrics"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/ratelimit"
)

// capturedRecord is one log line, flattened to attributes so a test can assert on
// a field without parsing JSON.
type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// capturingHandler collects log records instead of writing them anywhere.
type capturingHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	captured := capturedRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		captured.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, captured)
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

// find returns the first record whose message equals msg.
func (h *capturingHandler) find(msg string) (capturedRecord, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.msg == msg {
			return r, true
		}
	}
	return capturedRecord{}, false
}

func (h *capturingHandler) dump() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	for _, r := range h.records {
		b.WriteString(r.msg)
		for k, v := range r.attrs {
			b.WriteString(" " + k + "=" + v)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// A concrete path carries product ids. Every one of those became a distinct
// Prometheus label value, so label cardinality grew with traffic rather than with
// the number of routes — which is how a scrape target takes a Prometheus server
// down without anything in this service reporting a fault.
func TestRouteLabelCollapsesIdentifiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "collapses a product id",
			path: "/api/v1/products/918273",
			want: "/api/v1/products/{id}",
		},
		{
			name: "collapses several ids",
			path: "/api/v1/products/12/variants/34",
			want: "/api/v1/products/{id}/variants/{id}",
		},
		{
			name: "collapses a deeply nested id",
			path: "/api/v1/products/918273/variants/4471/stock",
			want: "/api/v1/products/{id}/variants/{id}/stock",
		},
		{
			name: "leaves a route with no id alone",
			path: "/api/v1/products",
			want: "/api/v1/products",
		},
		{
			name: "leaves a word segment alone, since a slug is not an id",
			path: "/api/v1/products/aurora-phone",
			want: "/api/v1/products/aurora-phone",
		},
		{
			name: "preserves the root",
			path: "/",
			want: "/",
		},
		{
			name: "handles a trailing slash",
			path: "/api/v1/products/",
			want: "/api/v1/products/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := routeLabel(tc.path); got != tc.want {
				t.Errorf("routeLabel(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// Two requests for different products must produce one label, not two series.
func TestRouteLabelBoundsCardinality(t *testing.T) {
	t.Parallel()

	labels := map[string]struct{}{}
	for id := 1; id <= 1000; id++ {
		labels[routeLabel("/api/v1/products/"+strconv.Itoa(id))] = struct{}{}
	}
	if len(labels) != 1 {
		t.Errorf("1000 product ids produced %d labels, want 1", len(labels))
	}
}

// The id in the X-Request-ID response header has to be findable in the logs, or it
// is decoration. RequestID used to be the innermost middleware, wrapping the mux
// directly, so the context it populated was created *after* the logging middleware
// had already run and the access log could never carry it.
func TestAccessLogCarriesTheRequestID(t *testing.T) {
	t.Parallel()

	sink := &capturingHandler{}
	log := slog.New(sink)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	limiter := ratelimit.New(ratelimit.Options{Rate: 1000, Burst: 1000})
	defer limiter.Stop()

	// A credential is presented because the chain now requires one. This test is
	// about the request id reaching the access log, and refusing the call first
	// would make it pass for the wrong reason.
	h := middlewareChain(mux, log, metrics.New(), limiter, []string{"test-inbound-token-value"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/products/42", nil)
	req.Header.Set("Authorization", "Bearer test-inbound-token-value")
	req.Header.Set("Authorization", "Bearer test-inbound-token-value")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	headerID := rec.Header().Get("X-Request-ID")
	if headerID == "" {
		t.Fatal("no X-Request-ID response header")
	}

	entry, ok := sink.find("request")
	if !ok {
		t.Fatalf("no access log entry; captured:\n%s", sink.dump())
	}
	if got := entry.attrs["request_id"]; got != headerID {
		t.Errorf("access log request_id = %q, response header = %q; a client quoting the header cannot find the request",
			got, headerID)
	}
}

// An id supplied by a caller must be propagated rather than replaced, so a trace
// started upstream survives into this service's logs.
func TestCallerSuppliedRequestIDIsPropagated(t *testing.T) {
	t.Parallel()

	sink := &capturingHandler{}
	log := slog.New(sink)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	limiter := ratelimit.New(ratelimit.Options{Rate: 1000, Burst: 1000})
	defer limiter.Stop()

	h := middlewareChain(mux, log, metrics.New(), limiter, []string{"test-inbound-token-value"})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("X-Request-ID", "trace-from-gateway")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "trace-from-gateway" {
		t.Errorf("response header = %q, want the caller's id", got)
	}
	entry, ok := sink.find("request")
	if !ok {
		t.Fatalf("no access log entry; captured:\n%s", sink.dump())
	}
	if got := entry.attrs["request_id"]; got != "trace-from-gateway" {
		t.Errorf("access log request_id = %q, want the caller's id", got)
	}
}

// A panicking handler must be recovered, and the report must identify the request.
func TestPanicIsRecoveredAndIdentified(t *testing.T) {
	t.Parallel()

	sink := &capturingHandler{}
	log := slog.New(sink)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("deliberate test panic")
	})

	limiter := ratelimit.New(ratelimit.Options{Rate: 1000, Burst: 1000})
	defer limiter.Stop()

	h := middlewareChain(mux, log, metrics.New(), limiter, []string{"test-inbound-token-value"})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	// A credential is presented so the request reaches the panicking handler;
	// otherwise the chain refuses it first and this test would assert 500 on a
	// response the guard produced.
	req.Header.Set("Authorization", "Bearer test-inbound-token-value")
	req.Header.Set("X-Request-ID", "panic-trace")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	entry, ok := sink.find("panic in handler")
	if !ok {
		t.Fatalf("no panic report; captured:\n%s", sink.dump())
	}
	if got := entry.attrs["request_id"]; got != "panic-trace" {
		t.Errorf("panic report request_id = %q, want %q", got, "panic-trace")
	}
	// The stack trace must not reach the client.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %q", rec.Body.String())
	}
	if msg, _ := body["error"].(string); strings.Contains(msg, "panic") {
		t.Errorf("panic detail leaked to the client: %q", msg)
	}
}

// middlewareChain must leave the rate limiter in place; extracting the chain is
// only safe if it still composes the same middleware in the same order.
func TestMiddlewareChainStillRateLimits(t *testing.T) {
	t.Parallel()

	sink := &capturingHandler{}
	log := slog.New(sink)

	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	})

	limiter := ratelimit.New(ratelimit.Options{Rate: 0.0001, Burst: 2})
	defer limiter.Stop()

	h := middlewareChain(mux, log, metrics.New(), limiter, []string{"test-inbound-token-value"})

	allowed, rejected := 0, 0
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ping", nil)
		req.Header.Set("Authorization", "Bearer test-inbound-token-value")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		switch rec.Code {
		case http.StatusTooManyRequests:
			rejected++
		default:
			allowed++
		}
	}
	if allowed == 0 {
		t.Error("the rate limiter rejected every request; it is not wired into the chain")
	}
	if rejected == 0 {
		t.Error("the rate limiter rejected nothing; it is not wired into the chain")
	}
	if calls != allowed {
		t.Errorf("handler ran %d times for %d allowed requests; a rejected request must not reach the handler", calls, allowed)
	}
}

// Security headers must survive the extraction.
func TestMiddlewareChainSetsSecurityHeaders(t *testing.T) {
	t.Parallel()

	sink := &capturingHandler{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	limiter := ratelimit.New(ratelimit.Options{Rate: 1000, Burst: 1000})
	defer limiter.Stop()

	h := middlewareChain(mux, slog.New(sink), metrics.New(), limiter, []string{"test-inbound-token-value"})

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set("Authorization", "Bearer test-inbound-token-value")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
}

// metrics.New registers into the Prometheus default registry, so a second call in
// the same process panics. Every test above constructs one, which means this file
// only passes if the collectors are registered exactly once — a property worth
// asserting rather than discovering as a panic in whichever test runs last.
func TestMetricsAreNotRegisteredTwice(t *testing.T) {
	t.Parallel()
	// Constructing a second Metrics must not panic.
	_ = metrics.New()
}

// The cache decorator exists to prove the ProductCache port can be instrumented
// without the service knowing Prometheus exists. If the counters ever go back to
// being incremented inside the service, this fails to compile — which is the point.
func TestMetricsCacheCountsHitsAndMisses(t *testing.T) {
	t.Parallel()

	inner := cache.New[*model.Product](cache.Options{MaxSize: 8})
	stats := metrics.New()
	c := newMetricsCache(inner, stats)

	// Miss, then hit, then miss again.
	if _, ok := c.Get("absent"); ok {
		t.Fatal("expected a miss on an empty cache")
	}
	c.Set("present", &model.Product{ID: 1})
	if _, ok := c.Get("present"); !ok {
		t.Fatal("expected a hit after Set")
	}
	if _, ok := c.Get("absent"); ok {
		t.Fatal("expected a miss for a key that was never set")
	}

	body := gather(t, stats)
	if got := metricValue(body, `product_listing_cache_hits_total`); got != "1" {
		t.Errorf("cache hits = %s, want 1", got)
	}
	if got := metricValue(body, `product_listing_cache_misses_total`); got != "2" {
		t.Errorf("cache misses = %s, want 2", got)
	}
}

// Invalidating through the decorator must still reach the inner cache; a decorator
// that silently drops a write would leave stale reads behind while reporting hits.
func TestMetricsCacheDelegatesDelete(t *testing.T) {
	t.Parallel()

	inner := cache.New[*model.Product](cache.Options{MaxSize: 8})
	c := newMetricsCache(inner, metrics.New())

	c.Set("k", &model.Product{ID: 5})
	c.Delete("k")

	if _, ok := inner.Get("k"); ok {
		t.Error("Delete did not reach the inner cache")
	}
}

// A rate-limited request must be counted. The limiter rejects before any handler
// runs, so this is the only place that rejection is observable.
func TestRateLimitedRequestsAreCounted(t *testing.T) {
	t.Parallel()

	stats := metrics.New()
	limiter := ratelimit.New(ratelimit.Options{Rate: 0.0001, Burst: 1})
	defer limiter.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	h := middlewareChain(mux, slog.New(&capturingHandler{}), stats, limiter, []string{"test-inbound-token-value"})

	for i := 0; i < 4; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	}

	body := gather(t, stats)
	if !strings.Contains(body, "product_listing_ratelimit_requests_total") {
		t.Fatalf("the rate limit counter is not exposed:\n%s", body)
	}
	var rateLimited float64
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "product_listing_ratelimit_requests_total ") {
			rateLimited, _ = strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "product_listing_ratelimit_requests_total ")), 64)
		}
	}
	if rateLimited < 1 {
		t.Errorf("rate limited requests = %v, want at least 1", rateLimited)
	}
}

// gather renders a Metrics registry through its handler and returns the text.
func gather(t *testing.T, m *metrics.Metrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape failed: %d", rec.Code)
	}
	return rec.Body.String()
}

// metricValue returns the value of the first sample whose line starts with name,
// ignoring its labels.
func metricValue(body, name string) string {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		if i := strings.LastIndex(line, " "); i >= 0 {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}

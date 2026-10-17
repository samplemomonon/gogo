package httpserver

import (
	"encoding/json"
	"expvar"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"gogo/internal/shared/breaker"
	"gogo/internal/shared/web/render"
)

// histogramBuckets are the request-duration bucket upper bounds in seconds.
// They follow the standard Prometheus latency ladder so alerts and dashboards
// can reuse well-known thresholds.
var histogramBuckets = []float64{
	0.001, 0.0025, 0.005, 0.01, 0.025,
	0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// Metrics collects gateway request metrics with the standard library only
// (no client_golang dependency): a Prometheus-text /metrics view for
// scrapers and an expvar snapshot for /debug/vars. Every request that passes
// through the gateway router is counted, keyed by HTTP method, status class
// and the matched chi route pattern (never raw paths, so labels cannot grow
// with the product set).
//
// A single Metrics instance is registered in expvar as "gogo_http" and served
// at /metrics by the gateway.
type Metrics struct {
	mu            sync.Mutex
	requestsTotal map[string]uint64 // key: method|code|route
	buckets       []uint64          // cumulative per histogramBuckets
	inflight      atomic.Int64
	requestCount  atomic.Uint64
	durationSumNs atomic.Uint64
	startedAt     time.Time
	breakers      map[string]*breaker.Breaker // named upstream client breakers, for observability
}

// publishOnce guards the expvar registration: expvar refuses duplicate
// variable names, and tests (or a listener restart) build more than one App.
// The registered Func always reflects the most recently constructed Metrics,
// which is the only one a process actually serves.
var (
	publishOnce  sync.Once
	currentStats atomic.Pointer[Metrics]
)

// NewMetrics returns an empty metrics registry. The first instance registers
// a self-updating var in expvar so the /debug/vars endpoint exposes the live
// counters for the lifecycle of the process. breakers maps a dependency name
// to the client circuit breaker guarding it, so every upstream service this
// process calls — the four product-platform services, plus the inventory, sales
// and auth modules it composes in-process — is reported under its own metric
// prefix instead of only the first one. A nil or empty map is valid.
func NewMetrics(breakers map[string]*breaker.Breaker) *Metrics {
	m := &Metrics{
		requestsTotal: make(map[string]uint64),
		buckets:       make([]uint64, len(histogramBuckets)),
		startedAt:     time.Now(),
		breakers:      breakers,
	}
	currentStats.Store(m)
	publishOnce.Do(func() {
		expvar.Publish("gogo_http", expvar.Func(func() any {
			if current := currentStats.Load(); current != nil {
				return current.snapshot()
			}
			return map[string]any{}
		}))
	})
	return m
}

// Middleware records one request: it latches the response status, counts the
// request under its method/status/route pattern and accumulates duration in
// nanoseconds. In-flight gauge is incremented on entry and decremented on
// exit.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		m.inflight.Add(1)
		defer m.inflight.Add(-1)

		next.ServeHTTP(wrapped, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		key := fmt.Sprintf("%s|%d|%s", r.Method, wrapped.Status(), route)

		duration := time.Since(start)
		durationSec := duration.Seconds()

		m.mu.Lock()
		m.requestsTotal[key]++
		for i, upper := range histogramBuckets {
			if durationSec <= upper {
				m.buckets[i]++
			}
		}
		m.mu.Unlock()

		m.requestCount.Add(1)
		m.durationSumNs.Add(uint64(duration.Nanoseconds()))
	})
}

// Handler serves the collected metrics in Prometheus text format. Content
// type matches the standard Prometheus exposition format (v0.0.4).
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		rows := m.prometheusRows()
		for _, row := range rows {
			_, _ = w.Write([]byte(row))
			_, _ = w.Write([]byte("\n"))
		}
	})
}

// snapshot returns the metrics as a JSON-able structure under one lock.
func (m *Metrics) snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()

	requests := make(map[string]uint64, len(m.requestsTotal))
	for key, count := range m.requestsTotal {
		requests[key] = count
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	snapshot := map[string]any{
		"requests_total":  requests,
		"request_count":   m.requestCount.Load(),
		"duration_sum_ns": m.durationSumNs.Load(),
		"inflight":        m.inflight.Load(),
		"uptime_seconds":  int64(time.Since(m.startedAt).Seconds()),
		"goroutines":      runtime.NumGoroutine(),
		"go_gc_cycles":    mem.NumGC,
		"go_heap_bytes":   mem.HeapAlloc,
	}

	for _, name := range sortedBreakerNames(m.breakers) {
		b := m.breakers[name]
		successes, failures, rejects, trips := b.Counters()
		snapshot[name+"_breaker_state"] = b.State()
		snapshot[name+"_breaker_successes"] = successes
		snapshot[name+"_breaker_failures"] = failures
		snapshot[name+"_breaker_rejects"] = rejects
		snapshot[name+"_breaker_trips"] = trips
	}

	return snapshot
}

// sortedBreakerNames returns the breaker map keys in a stable order, so the
// expvar JSON and the Prometheus text output list dependencies deterministically
// instead of in Go's randomized map order.
func sortedBreakerNames(breakers map[string]*breaker.Breaker) []string {
	names := make([]string, 0, len(breakers))
	for name, b := range breakers {
		if b != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// String implements expvar.Var, exposing the metrics as JSON at
// /debug/vars under the "gogo_http" key.
func (m *Metrics) String() string {
	data, err := json.Marshal(m.snapshot())
	if err != nil {
		return `{}`
	}
	return string(data)
}

// prometheusRows renders HELP/TYPE lines plus series for every metric,
// including the duration histogram and the Go runtime gauges scraped on
// demand.
func (m *Metrics) prometheusRows() []string {
	m.mu.Lock()
	requests := make(map[string]uint64, len(m.requestsTotal))
	for key, count := range m.requestsTotal {
		requests[key] = count
	}
	buckets := make([]uint64, len(m.buckets))
	copy(buckets, m.buckets)
	count := m.requestCount.Load()
	sumNs := m.durationSumNs.Load()
	inflight := m.inflight.Load()
	m.mu.Unlock()

	var rows []string

	rows = append(rows,
		"# HELP gogo_http_requests_total Requests processed by method, status and route.",
		"# TYPE gogo_http_requests_total counter",
	)
	keys := make([]string, 0, len(requests))
	for key := range requests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.SplitN(key, "|", 3)
		rows = append(rows, fmt.Sprintf(
			"gogo_http_requests_total{method=%q,code=%q,route=%q} %d",
			parts[0], parts[1], parts[2], requests[key],
		))
	}

	rows = append(rows,
		"# HELP gogo_http_inflight_requests Requests currently being served.",
		"# TYPE gogo_http_inflight_requests gauge",
		fmt.Sprintf("gogo_http_inflight_requests %d", inflight),
		"# HELP gogo_http_request_duration_seconds Request latency in seconds.",
		"# TYPE gogo_http_request_duration_seconds histogram",
	)
	for i, upper := range histogramBuckets {
		rows = append(rows, fmt.Sprintf(
			"gogo_http_request_duration_seconds_bucket{le=%q} %d",
			fmt.Sprintf("%g", upper), buckets[i],
		))
	}
	rows = append(rows,
		fmt.Sprintf("gogo_http_request_duration_seconds_bucket{le=\"+Inf\"} %d", count),
		fmt.Sprintf("gogo_http_request_duration_seconds_sum %g", float64(sumNs)/1e9),
		fmt.Sprintf("gogo_http_request_duration_seconds_count %d", count),
	)

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	rows = append(rows,
		"# HELP gogo_goroutines Current number of goroutines.",
		"# TYPE gogo_goroutines gauge",
		fmt.Sprintf("gogo_goroutines %d", runtime.NumGoroutine()),
		"# HELP gogo_go_gc_cycles Number of completed GC cycles.",
		"# TYPE gogo_go_gc_cycles counter",
		fmt.Sprintf("gogo_go_gc_cycles %d", mem.NumGC),
		"# HELP gogo_go_heap_bytes Heap allocation in bytes.",
		"# TYPE gogo_go_heap_bytes gauge",
		fmt.Sprintf("gogo_go_heap_bytes %d", mem.HeapAlloc),
		"# HELP gogo_uptime_seconds Seconds since the gateway started.",
		"# TYPE gogo_uptime_seconds gauge",
		fmt.Sprintf("gogo_uptime_seconds %d", int64(time.Since(m.startedAt).Seconds())),
	)

	cacheHits, cacheMisses := render.CacheCounters()
	rows = append(rows,
		"# HELP gogo_render_cache_hits_total Islands served from the render cache without re-rendering.",
		"# TYPE gogo_render_cache_hits_total counter",
		fmt.Sprintf("gogo_render_cache_hits_total %d", cacheHits),
		"# HELP gogo_render_cache_misses_total Islands rendered because they were absent or expired in the render cache.",
		"# TYPE gogo_render_cache_misses_total counter",
		fmt.Sprintf("gogo_render_cache_misses_total %d", cacheMisses),
	)

	// Per-island render totals, keyed by island family (the search term a
	// paged island was requested with is collapsed away — see
	// render.IslandCounters). Errors and fallbacks are separate series so a
	// degrading island is visible before its fallback rate moves.
	islands := render.IslandCounters()
	if len(islands) > 0 {
		rows = append(rows,
			"# HELP gogo_render_island_renders_total Server-rendered island regions by island family.",
			"# TYPE gogo_render_island_renders_total counter",
			"# HELP gogo_render_island_fallbacks_total Island renders degraded to their fallback.",
			"# TYPE gogo_render_island_fallbacks_total counter",
			"# HELP gogo_render_island_timeouts_total Island renders that exceeded their timeout.",
			"# TYPE gogo_render_island_timeouts_total counter",
			"# HELP gogo_render_island_errors_total Island renders that failed.",
			"# TYPE gogo_render_island_errors_total counter",
			"# HELP gogo_render_island_duration_seconds_sum Total island render time in seconds.",
			"# TYPE gogo_render_island_duration_seconds_sum counter",
		)
		names := make([]string, 0, len(islands))
		for name := range islands {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			stat := islands[name]
			rows = append(rows,
				fmt.Sprintf("gogo_render_island_renders_total{island=%q} %d", name, stat.Renders),
				fmt.Sprintf("gogo_render_island_fallbacks_total{island=%q} %d", name, stat.Fallbacks),
				fmt.Sprintf("gogo_render_island_timeouts_total{island=%q} %d", name, stat.Timeouts),
				fmt.Sprintf("gogo_render_island_errors_total{island=%q} %d", name, stat.Errors),
				fmt.Sprintf("gogo_render_island_duration_seconds_sum{island=%q} %g", name, stat.DurationSeconds),
			)
		}
	}

	// Client-side web-vitals, aggregated by the /rum beacon (rum.go). Same
	// bounded-label policy as the island counters above.
	rows = append(rows, rumRows()...)

	for _, name := range sortedBreakerNames(m.breakers) {
		b := m.breakers[name]
		successes, failures, rejects, trips := b.Counters()
		state := b.State()
		metric := metricName(name)
		rows = append(rows,
			fmt.Sprintf("# HELP gogo_%s_breaker_state Circuit breaker state: 0=closed, 1=open, 2=half-open.", metric),
			fmt.Sprintf("# TYPE gogo_%s_breaker_state gauge", metric),
			fmt.Sprintf("gogo_%s_breaker_state %d", metric, state),
			fmt.Sprintf("# HELP gogo_%s_breaker_successes_total Successful %s calls.", metric, name),
			fmt.Sprintf("# TYPE gogo_%s_breaker_successes_total counter", metric),
			fmt.Sprintf("gogo_%s_breaker_successes_total %d", metric, successes),
			fmt.Sprintf("# HELP gogo_%s_breaker_failures_total Failed %s calls.", metric, name),
			fmt.Sprintf("# TYPE gogo_%s_breaker_failures_total counter", metric),
			fmt.Sprintf("gogo_%s_breaker_failures_total %d", metric, failures),
			fmt.Sprintf("# HELP gogo_%s_breaker_rejects_total Calls rejected by open circuit.", metric),
			fmt.Sprintf("# TYPE gogo_%s_breaker_rejects_total counter", metric),
			fmt.Sprintf("gogo_%s_breaker_rejects_total %d", metric, rejects),
			fmt.Sprintf("# HELP gogo_%s_breaker_trips_total Number of times circuit has opened.", metric),
			fmt.Sprintf("# TYPE gogo_%s_breaker_trips_total counter", metric),
			fmt.Sprintf("gogo_%s_breaker_trips_total %d", metric, trips),
		)
	}

	return rows
}

// metricName converts an upstream's identity into a Prometheus metric-name fragment.
//
// A Prometheus metric name may only contain [a-zA-Z0-9_:] and must start with a letter,
// colon or underscore. The product platform's service names are hyphenated
// ("product-listing", "product-search") because that is what the deployment calls them,
// so emitting the raw name would produce a scrape target Prometheus rejects outright —
// which fails the whole /metrics scrape, not just those series.
//
// The mapping is hyphen → underscore, which is the conventional one, and the HELP text
// keeps the original name so a human reading the metric still sees which service it is.
// A service named the same way in both notations is the one ambiguity this introduces,
// and it is cheaper than a scrape that fails.
func metricName(upstream string) string {
	return strings.ReplaceAll(upstream, "-", "_")
}

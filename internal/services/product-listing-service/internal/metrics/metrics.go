// Package metrics provides Prometheus instrumentation for the service.
//
// Metrics are exposed on a separate port from the main API so that scraping
// never competes with request handling, and so that a broken metrics endpoint
// cannot take down the read path. The package is safe for concurrent use and
// has no dependencies beyond the Prometheus client library.
package metrics

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace is the prefix for all metrics exported by this service.
const Namespace = "product_listing"

// Metrics holds all Prometheus collectors for the service.
type Metrics struct {
	// registry is this service's private registry rather than
	// prometheus.DefaultRegisterer. Registering into the default one made New a
	// hidden global side effect and not idempotent: a second call in the same
	// process panics with "duplicate metrics collector registration attempted",
	// which turns any second construction — a test, a second server in one binary
	// — into a crash at startup rather than a compile-time or obvious runtime
	// error. A private registry also keeps these collectors from colliding with
	// anything else the process registers.
	registry *prometheus.Registry

	// HTTP request metrics
	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec
	HTTPRequestSize     *prometheus.HistogramVec
	HTTPResponseSize    *prometheus.HistogramVec
	HTTPActiveRequests  prometheus.Gauge

	// gRPC request metrics
	GRPCRequestsTotal   *prometheus.CounterVec
	GRPCRequestDuration *prometheus.HistogramVec
	GRPCActiveRequests  prometheus.Gauge

	// Consumer metrics
	ConsumerLag           prometheus.Gauge
	ConsumerEventsTotal   *prometheus.CounterVec
	ConsumerBatchDuration prometheus.Histogram
	ConsumerDeadLetters   prometheus.Counter
	ConsumerOffset        *prometheus.GaugeVec

	// Database metrics
	DBQueryDuration *prometheus.HistogramVec
	DBErrorsTotal   *prometheus.CounterVec
	DBActiveConns   prometheus.Gauge
	DBIdleConns     prometheus.Gauge

	// Cache metrics
	CacheHitsTotal   prometheus.Counter
	CacheMissesTotal prometheus.Counter
	CacheSize        prometheus.Gauge
	CacheEvictions   prometheus.Counter

	// Projection metrics
	ProjectionEventsTotal *prometheus.CounterVec
	ProjectionDuration    prometheus.Histogram
	ProjectionLag         prometheus.Gauge

	// Rate limiter metrics
	RateLimitedRequests prometheus.Counter
}

// New creates the service's collectors and registers them with a private
// Prometheus registry.
//
// Calling New more than once is safe and produces independent sets of collectors.
// Nothing is registered with prometheus.DefaultRegisterer, so a second call cannot
// panic and this package has no effect on any other registry the process uses.
func New() *Metrics {
	registry := prometheus.NewRegistry()
	m := &Metrics{
		registry: registry,
		HTTPRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "http",
				Name:      "requests_total",
				Help:      "Total number of HTTP requests handled.",
			},
			[]string{"method", "path", "status"},
		),
		HTTPRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Subsystem: "http",
				Name:      "request_duration_seconds",
				Help:      "HTTP request duration in seconds.",
				Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"method", "path"},
		),
		HTTPRequestSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Subsystem: "http",
				Name:      "request_size_bytes",
				Help:      "HTTP request size in bytes.",
				Buckets:   prometheus.ExponentialBuckets(100, 10, 8),
			},
			[]string{"method", "path"},
		),
		HTTPResponseSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Subsystem: "http",
				Name:      "response_size_bytes",
				Help:      "HTTP response size in bytes.",
				Buckets:   prometheus.ExponentialBuckets(100, 10, 8),
			},
			[]string{"method", "path"},
		),
		HTTPActiveRequests: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Subsystem: "http",
				Name:      "active_requests",
				Help:      "Number of HTTP requests currently being processed.",
			},
		),

		GRPCRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "grpc",
				Name:      "requests_total",
				Help:      "Total number of gRPC requests handled.",
			},
			[]string{"method", "code"},
		),
		GRPCRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Subsystem: "grpc",
				Name:      "request_duration_seconds",
				Help:      "gRPC request duration in seconds.",
				Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"method"},
		),
		GRPCActiveRequests: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Subsystem: "grpc",
				Name:      "active_requests",
				Help:      "Number of gRPC requests currently being processed.",
			},
		),

		ConsumerLag: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Subsystem: "consumer",
				Name:      "lag",
				Help:      "Number of events the consumer is behind the latest offset.",
			},
		),
		ConsumerEventsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "consumer",
				Name:      "events_total",
				Help:      "Total number of events processed by the consumer.",
			},
			[]string{"event_type", "outcome"},
		),
		ConsumerBatchDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Subsystem: "consumer",
				Name:      "batch_duration_seconds",
				Help:      "Duration of consumer batch processing in seconds.",
				Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			},
		),
		ConsumerDeadLetters: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "consumer",
				Name:      "dead_letters_total",
				Help:      "Total number of records sent to the dead-letter topic.",
			},
		),
		ConsumerOffset: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Subsystem: "consumer",
				Name:      "offset",
				Help:      "Current consumer offset per partition.",
			},
			[]string{"topic", "partition"},
		),

		DBQueryDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Subsystem: "db",
				Name:      "query_duration_seconds",
				Help:      "Database query duration in seconds.",
				Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			},
			[]string{"operation"},
		),
		DBErrorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "db",
				Name:      "errors_total",
				Help:      "Total number of database errors.",
			},
			[]string{"operation", "error_type"},
		),
		DBActiveConns: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Subsystem: "db",
				Name:      "active_connections",
				Help:      "Number of active database connections.",
			},
		),
		DBIdleConns: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Subsystem: "db",
				Name:      "idle_connections",
				Help:      "Number of idle database connections.",
			},
		),

		CacheHitsTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "cache",
				Name:      "hits_total",
				Help:      "Total number of cache hits.",
			},
		),
		CacheMissesTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "cache",
				Name:      "misses_total",
				Help:      "Total number of cache misses.",
			},
		),
		CacheSize: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Subsystem: "cache",
				Name:      "size",
				Help:      "Current number of entries in the cache.",
			},
		),
		CacheEvictions: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "cache",
				Name:      "evictions_total",
				Help:      "Total number of cache evictions.",
			},
		),

		ProjectionEventsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "projection",
				Name:      "events_total",
				Help:      "Total number of events projected.",
			},
			[]string{"event_type", "outcome"},
		),
		ProjectionDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: Namespace,
				Subsystem: "projection",
				Name:      "duration_seconds",
				Help:      "Duration of event projection in seconds.",
				Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
			},
		),
		ProjectionLag: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Namespace: Namespace,
				Subsystem: "projection",
				Name:      "lag_seconds",
				Help:      "Time between event occurrence and projection in seconds.",
			},
		),

		RateLimitedRequests: prometheus.NewCounter(
			prometheus.CounterOpts{
				Namespace: Namespace,
				Subsystem: "ratelimit",
				Name:      "requests_total",
				Help:      "Total number of requests rejected by the rate limiter.",
			},
		),
	}

	// Registration is explicit rather than via promauto, because promauto writes to
	// the process-global default registry and that is the behaviour this
	// constructor exists to avoid.
	m.registry.MustRegister(
		m.HTTPRequestsTotal,
		m.HTTPRequestDuration,
		m.HTTPRequestSize,
		m.HTTPResponseSize,
		m.HTTPActiveRequests,
		m.GRPCRequestsTotal,
		m.GRPCRequestDuration,
		m.DBQueryDuration,
		m.DBErrorsTotal,
		m.CacheHitsTotal,
		m.CacheMissesTotal,
		m.CacheSize,
		m.CacheEvictions,
		m.ProjectionEventsTotal,
		m.ProjectionDuration,
		m.ProjectionLag,
		m.RateLimitedRequests,
		m.ConsumerEventsTotal,
		m.ConsumerDeadLetters,
		m.ConsumerLag,
	)

	return m
}

// Registry exposes the private registry, so a test can gather the collectors
// without scraping an HTTP endpoint.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler returns an HTTP handler for the Prometheus metrics endpoint. It serves
// this service's private registry, not the default one.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// RecordHTTPRequest records metrics for an HTTP request.
func (m *Metrics) RecordHTTPRequest(method, path string, status int, duration time.Duration, requestSize, responseSize int64) {
	statusStr := strconv.Itoa(status)
	m.HTTPRequestsTotal.WithLabelValues(method, path, statusStr).Inc()
	m.HTTPRequestDuration.WithLabelValues(method, path).Observe(duration.Seconds())
	m.HTTPRequestSize.WithLabelValues(method, path).Observe(float64(requestSize))
	m.HTTPResponseSize.WithLabelValues(method, path).Observe(float64(responseSize))
}

// RecordGRPCRequest records metrics for a gRPC request.
func (m *Metrics) RecordGRPCRequest(method, code string, duration time.Duration) {
	m.GRPCRequestsTotal.WithLabelValues(method, code).Inc()
	m.GRPCRequestDuration.WithLabelValues(method).Observe(duration.Seconds())
}

// RecordDBQuery records metrics for a database query.
func (m *Metrics) RecordDBQuery(operation string, duration time.Duration, err error) {
	m.DBQueryDuration.WithLabelValues(operation).Observe(duration.Seconds())
	if err != nil {
		// errors.Is, not err.Error() == "...". Matching on the message means the
		// classification silently becomes "unknown" the moment the driver changes
		// its wording — and a metric that quietly stops classifying looks
		// identical to one reporting a novel failure, so nothing surfaces the loss.
		errorType := "unknown"
		switch {
		case errors.Is(err, sql.ErrConnDone):
			errorType = "connection"
		case errors.Is(err, driver.ErrBadConn):
			errorType = "connection"
		case errors.Is(err, context.DeadlineExceeded):
			errorType = "timeout"
		case errors.Is(err, context.Canceled):
			errorType = "canceled"
		}
		m.DBErrorsTotal.WithLabelValues(operation, errorType).Inc()
	}
}

// RecordCacheHit records a cache hit.
func (m *Metrics) RecordCacheHit() {
	m.CacheHitsTotal.Inc()
}

// RecordCacheMiss records a cache miss.
func (m *Metrics) RecordCacheMiss() {
	m.CacheMissesTotal.Inc()
}

// RecordConsumerEvent records metrics for a consumed event.
func (m *Metrics) RecordConsumerEvent(eventType, outcome string) {
	m.ConsumerEventsTotal.WithLabelValues(eventType, outcome).Inc()
}

// RecordDeadLetter records a dead-lettered record.
func (m *Metrics) RecordDeadLetter() {
	m.ConsumerDeadLetters.Inc()
}

// RecordProjection records metrics for a projection operation.
func (m *Metrics) RecordProjection(eventType, outcome string, duration time.Duration) {
	m.ProjectionEventsTotal.WithLabelValues(eventType, outcome).Inc()
	m.ProjectionDuration.Observe(duration.Seconds())
}

// RecordRateLimited records a rate-limited request.
func (m *Metrics) RecordRateLimited() {
	m.RateLimitedRequests.Inc()
}

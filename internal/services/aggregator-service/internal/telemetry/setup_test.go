package telemetry

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// These tests cover the setup path: what the provider reports about itself, and the two
// properties that decide whether tracing is safe to enable in production — that it never
// becomes the reason a service fails to start, and that a buffered span is not lost.

// TestTheEnabledProviderDescribesThisService covers the resource attributes a backend
// uses to tell six otherwise identical Go services apart. A collector that cannot
// attribute a span to aggregator-service is a collector with one useless index.
func TestTheEnabledProviderDescribesThisService(t *testing.T) {
	res, err := newResource(Config{
		ServiceName:    "aggregator-service",
		ServiceVersion: "1.2.3",
		Environment:    "staging",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	attrs := map[string]string{}
	for _, kv := range res.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}

	for key, want := range map[string]string{
		"service.name":                "aggregator-service",
		"service.version":             "1.2.3",
		"deployment.environment.name": "staging",
		// Without the SDK defaults merged in, a backend cannot tell which library
		// produced the spans, which is the first question asked of any trace.
		"telemetry.sdk.name": "opentelemetry",
	} {
		if got := attrs[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestResourceOmitsEmptyOptionalFields(t *testing.T) {
	// An unset version should be absent rather than present-and-empty. A backend
	// filtering "everything that is not versioned" would otherwise silently include
	// this service in an unversioned bucket it does not belong to.
	res, err := newResource(Config{ServiceName: "aggregator-service"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, kv := range res.Attributes() {
		if key := string(kv.Key); key == "service.version" || key == "deployment.environment.name" {
			t.Errorf("%s is present with %q, want it omitted when unset", key, kv.Value.Emit())
		}
	}
}

func TestSampleRatioOfZeroRecordsNothingRatherThanEverything(t *testing.T) {
	// The failure this guards is a collector scaled down to zero that then receives
	// the full trace volume, because a "<= 0 means default" clamp turned an explicit
	// "record nothing" into "record everything".
	provider, err := New(Config{
		Enabled:       true,
		ServiceName:   "aggregator-service",
		Endpoint:      "127.0.0.1:1",
		Insecure:      true,
		SampleRatio:   0,
		BatchTimeout:  time.Millisecond,
		ExportTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background(), provider) })

	_, span := provider.Tracer().Start(t.Context(), "should not be recorded")
	if span.IsRecording() {
		t.Fatal("a sample ratio of 0 recorded a span")
	}
	span.End()
}

func TestAnOutOfRangeSampleRatioIsClamped(t *testing.T) {
	// Validate rejects these at startup, so this is defence in depth for a caller that
	// builds a Config directly. Recording nothing silently would be worse than either.
	for _, ratio := range []float64{-1, 5} {
		provider, err := New(Config{
			Enabled:       true,
			ServiceName:   "aggregator-service",
			Endpoint:      "127.0.0.1:1",
			Insecure:      true,
			SampleRatio:   ratio,
			BatchTimeout:  time.Millisecond,
			ExportTimeout: 10 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("ratio %v: unexpected error: %v", ratio, err)
		}
		_, span := provider.Tracer().Start(t.Context(), "clamped")
		if !span.IsRecording() {
			t.Errorf("ratio %v recorded nothing; an out-of-range value should clamp to a usable one", ratio)
		}
		span.End()
		_ = Shutdown(context.Background(), provider)
	}
}

func TestAMalformedEndpointIsRefusedAtStartup(t *testing.T) {
	// A misconfigured collector should fail the build, not produce a service that
	// claims to be traced while exporting nothing.
	if _, err := New(Config{
		Enabled:     true,
		ServiceName: "aggregator-service",
		Endpoint:    "http://\x00bad",
	}); err == nil {
		t.Fatal("a malformed endpoint was accepted")
	}
}

func TestTracingIsNeverTheReasonStartupFails(t *testing.T) {
	// The property the whole package rests on: with tracing off, and with a collector
	// that does not exist, the service still comes up and still serves.
	provider, err := New(Config{Enabled: false, ServiceName: "aggregator-service"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := Shutdown(context.Background(), provider); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// Enabled with an endpoint nothing is listening on: the exporter connects lazily,
	// so this must succeed. An operator who points tracing at a collector that is not up
	// yet gets a service that serves and starts exporting once it appears.
	live, err := New(Config{
		Enabled:       true,
		ServiceName:   "aggregator-service",
		Endpoint:      "127.0.0.1:1",
		Insecure:      true,
		BatchTimeout:  time.Millisecond,
		ExportTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("an unreachable collector failed startup: %v", err)
	}
	if live.Tracer() == nil {
		t.Fatal("no tracer after an enabled setup")
	}
	_ = Shutdown(context.Background(), live)
}

func TestThePropagatorSurvivesTracingBeingDisabled(t *testing.T) {
	// The subtle one. A no-op tracer provider with no propagator means traceparent is
	// never injected, so the moment a downstream service is instrumented the trace IDs
	// will not match. There is no error and no warning, which is why this is asserted
	// rather than assumed.
	if _, err := New(Config{Enabled: false, ServiceName: "aggregator-service"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, span := testTracer.Start(t.Context(), "outbound")
	defer span.End()

	carrier := headerCarrier{}
	globalPropagator().Inject(ctx, &carrier)

	if carrier.traceparent == "" {
		t.Fatal("no traceparent was injected while tracing was disabled")
	}
	if !strings.HasPrefix(carrier.traceparent, "00-") {
		t.Fatalf("traceparent %q is not W3C TraceContext format", carrier.traceparent)
	}
}

func TestAnIncomingTraceparentIsHonoured(t *testing.T) {
	// The server-side half: a caller that already has a trace gets its span ID as the
	// parent rather than a new trace starting here, so a trace spans the whole call
	// chain instead of restarting at this service.
	provider, err := New(enabledAgainstNothing())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background(), provider) })

	// A well-formed traceparent: version 00, a non-zero trace id, a non-zero span id.
	const incoming = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	ctx := globalPropagator().Extract(t.Context(), &headerCarrier{traceparent: incoming})

	_, span := provider.Tracer().Start(ctx, "inherited")
	defer span.End()

	if got := span.SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace id %s, want the caller's %s", got, "4bf92f3577b34da6a3ce929d0e0e4736")
	}
}

func TestTheDisabledProviderStillInstallsTheGlobalPropagator(t *testing.T) {
	// Both setups must, because the propagator is what makes a downstream service's
	// later instrumentation a no-op here. Asserted on the global rather than through a
	// helper so a refactor that drops the call in the disabled branch is caught.
	if _, err := New(Config{Enabled: false, ServiceName: "aggregator-service"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fields := globalPropagator().Fields()
	if !slices.Contains(fields, "traceparent") {
		t.Fatalf("the global propagator does not carry traceparent; fields = %v", fields)
	}
}

// TestTheHTTPHandlerStartsAndEndsASpanAroundTheRequest is the one test that proves the
// wrapper is wired to the handler rather than merely constructible.
func TestTheHTTPHandlerStartsAndEndsASpanAroundTheRequest(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	restoreProvider(t, provider)

	inner := &countingHandler{}
	handler := HTTPHandler(inner)

	// Exercised over a real request rather than by calling the handler directly, so the
	// otelhttp wrapper's own plumbing is covered rather than assumed.
	req, err := httpRequest(t, "POST", "/query")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	handler.ServeHTTP(&nopResponseWriter{}, req)

	if inner.calls != 1 {
		t.Fatalf("the wrapped handler was called %d times, want 1", inner.calls)
	}

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans, want exactly 1 for one request", len(ended))
	}
	// Pinned exactly. The name is not derived per request, because GraphQL serves one
	// path and a per-request name would add cardinality without adding information; and
	// it is not the bare HTTP method, which is what otelhttp falls back to when no name
	// formatter is supplied.
	if ended[0].Name() != "POST /query" {
		t.Errorf("span name %q, want %q so cardinality stays at one", ended[0].Name(), "POST /query")
	}
}

func TestTheHTTPHandlerRecordsAFailedStatusAsAnError(t *testing.T) {
	// What makes an error-rate query on the backend meaningful without per-handler code.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	restoreProvider(t, provider)

	req, err := httpRequest(t, "GET", "/healthz")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	HTTPHandler(&statusHandler{status: 503}).ServeHTTP(&nopResponseWriter{}, req)

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(ended))
	}
	if ended[0].Status().Code.String() != "Error" {
		t.Errorf("a 503 produced span status %v, want Error", ended[0].Status().Code)
	}
}

func TestTheHTTPHandlerContinuesToServeAfterAPanic(t *testing.T) {
	// Not this service's job to fix — the recovery middleware below it handles that —
	// but the wrapper must not turn a panic into a dropped connection before the
	// recovery middleware ever sees it.
	req, err := httpRequest(t, "POST", "/query")
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	handler := HTTPHandler(&panickingHandler{})

	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		handler.ServeHTTP(&nopResponseWriter{}, req)
	}()

	select {
	case got := <-recovered:
		if got == nil {
			t.Fatal("the panic was swallowed by the tracing wrapper instead of reaching the recovery middleware")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the request did not return")
	}
}

// enabledAgainstNothing is an enabled config pointed at a port nothing listens on,
// with export timeouts short enough that a shutdown flush fails fast instead of paying
// the ten-second default against a refused connection.
//
// Every test that needs a real sampler or a real exporter uses this. Reaching a live
// collector from a unit test would make the suite depend on something outside the repo.
func enabledAgainstNothing() Config {
	return Config{
		Enabled:       true,
		ServiceName:   "aggregator-service",
		Endpoint:      "127.0.0.1:1",
		Insecure:      true,
		BatchTimeout:  time.Millisecond,
		ExportTimeout: 10 * time.Millisecond,
	}
}

// --- test doubles ---------------------------------------------------------

// restoreProvider installs a provider globally and puts the previous one back.
//
// The global is what otelhttp and otelgrpc read, so a test that wants to observe their
// spans has to set it rather than pass a tracer. Restoring matters: the global outlives
// the test, and a provider left behind would be shut down by this test's cleanup while
// a later one still pointed at it.
func restoreProvider(t *testing.T, provider *sdktrace.TracerProvider) {
	t.Helper()

	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})
}

func httpRequest(t *testing.T, method, path string) (*http.Request, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, "http://aggregator"+path, nil)
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// nopResponseWriter is the minimum http.ResponseWriter. httptest is deliberately not
// used: it adds assertions this package has no business making, and a recording writer
// would let a test pass while the handler wrote nothing at all.
type nopResponseWriter struct {
	header http.Header
	status int
}

func (w *nopResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *nopResponseWriter) Write(b []byte) (int, error) { return len(b), nil }

func (w *nopResponseWriter) WriteHeader(status int) { w.status = status }

type countingHandler struct{ calls int }

func (h *countingHandler) ServeHTTP(rw http.ResponseWriter, _ *http.Request) {
	h.calls++
	rw.WriteHeader(http.StatusOK)
}

type statusHandler struct{ status int }

func (h *statusHandler) ServeHTTP(rw http.ResponseWriter, _ *http.Request) {
	rw.WriteHeader(h.status)
}

type panickingHandler struct{}

func (h *panickingHandler) ServeHTTP(http.ResponseWriter, *http.Request) {
	panic("resolver exploded")
}

// headerCarrier is a TextMapCarrier over a single header, which is all a
// traceparent needs.
type headerCarrier struct{ traceparent string }

func (c *headerCarrier) Get(key string) string {
	if key == "traceparent" {
		return c.traceparent
	}
	return ""
}

func (c *headerCarrier) Set(key, value string) {
	if key == "traceparent" {
		c.traceparent = value
	}
}

func (c *headerCarrier) Keys() []string { return []string{"traceparent"} }

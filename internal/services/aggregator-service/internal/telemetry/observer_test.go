package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/breaker"
	"aggregator-service/internal/grpcclient"
	"aggregator-service/internal/retry"
)

// The exporter is replaced with an in-memory recorder so the tests assert on the spans
// that would actually be exported, rather than on the calls made to a wrapper.

type recordingExporter struct {
	mu       sync.Mutex
	finished []sdktrace.ReadOnlySpan
}

func (r *recordingExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished = append(r.finished, spans...)
	return nil
}

func (r *recordingExporter) Shutdown(context.Context) error { return nil }

func (r *recordingExporter) spans() []sdktrace.ReadOnlySpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sdktrace.ReadOnlySpan, len(r.finished))
	copy(out, r.finished)
	return out
}

// newRecordingProvider builds a provider whose spans land in memory.
// harness is a real TracerProvider feeding an in-memory recorder.
//
// SpanRecorder is a SpanProcessor, not a TracerProvider, so it needs the provider to be
// built around it. Doing that rather than stubbing the tracer is what makes the
// assertions meaningful: the spans under test are the ones the SDK would export.
type harness struct {
	*tracetest.SpanRecorder
	tracer trace.Tracer
}

func newHarness(t *testing.T) harness {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return harness{SpanRecorder: recorder, tracer: provider.Tracer("test")}
}

// testTracer is a tracer from an always-sampling in-memory provider.
//
// tracetest.SpanRecorder implements TracerProvider, so a tracer comes straight from it
// and every span it starts is recorded for the assertions below.
var testTracer = sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample())).Tracer("test")

// globalPropagator is the propagator New installs.
func globalPropagator() propagation.TextMapPropagator { return otel.GetTextMapPropagator() }

var errTransient = errors.Join(apperror.ErrUnavailable, errors.New("connection refused"))

// policyForTest is a retry policy with a real attempt budget and no waiting, so the
// tests can drive several attempts without sleeping.
func policyForTest() retry.Policy {
	return retry.New(retry.Options{MaxAttempts: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
}

func TestObserverRecordsOneSpanPerOperation(t *testing.T) {
	// The span hierarchy is the design decision. One span per operation means a retried
	// call is one span with retry events, not three spans that look unrelated.
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	ctx, span := observer.Start(t.Context(), "category-service", retry.New(retry.Options{MaxAttempts: 3}))
	observer.ReportOutcome(ctx, span, nil)
	span.End()

	if got := len(recorder.Ended()); got != 1 {
		t.Fatalf("recorded %d spans for one operation, want 1", got)
	}
}

func TestObserverRecordsRetryEventsNotSiblingSpans(t *testing.T) {
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	policy := retry.New(retry.Options{MaxAttempts: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
	ctx, span := observer.Start(t.Context(), "media-service", policy)

	observer.ReportAttempt(ctx, span, 1, policy, errTransient)
	observer.ReportAttempt(ctx, span, 2, policy, errTransient)
	observer.ReportAttempt(ctx, span, 3, policy, nil)
	observer.ReportOutcome(ctx, span, nil)
	span.End()

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(ended))
	}

	events := eventNames(ended[0])
	if len(events) != 3 {
		t.Fatalf("got events %v, want one per attempt", events)
	}
	// The first two are retries, the third is the success. Getting this wrong would
	// report a successful call as a failed one in any query over attempt events.
	want := []string{"attempt.failed.retrying", "attempt.failed.retrying", "attempt.succeeded"}
	for i, w := range want {
		if events[i] != w {
			t.Errorf("event %d: got %q, want %q", i, events[i], w)
		}
	}
}

func TestObserverMarksTheFinalAttemptAsFinalNotRetrying(t *testing.T) {
	// An exhausted budget must be visible as final. Reporting it as retrying would
	// suggest a retry happened that did not.
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	policy := retry.New(retry.Options{MaxAttempts: 2, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
	ctx, span := observer.Start(t.Context(), "listing", policy)
	observer.ReportAttempt(ctx, span, 2, policy, errTransient)
	span.End()

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans", len(ended))
	}
	if got := eventNames(ended[0]); len(got) != 1 || got[0] != "attempt.failed.final" {
		t.Fatalf("events %v, want a single attempt.failed.final", got)
	}
}

func TestObserverDoesNotMarkAPermanentFailureAsRetrying(t *testing.T) {
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	policy := retry.New(retry.Options{MaxAttempts: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
	ctx, span := observer.Start(t.Context(), "listing", policy)
	observer.ReportAttempt(ctx, span, 1, policy, apperror.ErrInvalidInput)
	span.End()

	ended := recorder.Ended()
	events := eventNames(ended[0])
	if len(events) != 1 || events[0] != "attempt.failed.final" {
		t.Fatalf("events %v, want a client error recorded as final", events)
	}
}

func TestObserverSetsErrorOnAFailedOperation(t *testing.T) {
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	ctx, span := observer.Start(t.Context(), "search", retry.Read())
	observer.ReportOutcome(ctx, span, errTransient)
	span.End()

	ended := recorder.Ended()
	if ended[0].Status().Code.String() != "Error" {
		t.Fatalf("status %v, want Error", ended[0].Status().Code)
	}
}

func TestObserverLeavesASuccessfulOperationOK(t *testing.T) {
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	ctx, span := observer.Start(t.Context(), "search", retry.Read())
	observer.ReportOutcome(ctx, span, nil)
	span.End()

	ended := recorder.Ended()
	// Unset means OK. Setting it explicitly would be correct but would add noise to
	// every successful span.
	if ended[0].Status().Code != otelcodes.Unset {
		t.Fatalf("status %v, want Unset on success", ended[0].Status().Code)
	}
}

func TestObserverRecordsABreakerRefusalAsAnError(t *testing.T) {
	// Shed traffic is a failure from the caller's point of view even though the service
	// is behaving exactly as designed, so an error-rate query has to count it.
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	ctx, span := observer.Start(t.Context(), "category-service", retry.Read())
	observer.ReportRefused(ctx, span, "category-service", breaker.StateOpen, breaker.ErrOpenCircuit)
	span.End()

	ended := recorder.Ended()
	if ended[0].Status().Code != otelcodes.Error {
		t.Fatalf("status %v, want Error", ended[0].Status().Code)
	}
	if got := eventNames(ended[0]); len(got) != 1 || got[0] != "breaker.refused" {
		t.Fatalf("events %v, want breaker.refused", got)
	}
	if !hasAttribute(ended[0], AttrBreakerRefused, true) {
		t.Fatal("the span is missing the breaker.refused attribute, so shed traffic is not queryable")
	}
	if !hasAttribute(ended[0], AttrBreakerState, "OPEN") {
		t.Fatal("the span is missing the breaker state")
	}
}

func TestSpanCarriesDependencyAndBudget(t *testing.T) {
	// These two attributes are what make a trace filterable by dependency without
	// parsing span names.
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	policy := retry.Read()
	ctx, span := observer.Start(t.Context(), "product-listing-service", policy)
	span.End()
	_ = ctx

	ended := recorder.Ended()
	if !strings.Contains(ended[0].Name(), "product-listing-service") {
		t.Errorf("span name %q should name the dependency", ended[0].Name())
	}
	if !hasAttribute(ended[0], AttrDependency, "product-listing-service") {
		t.Error("the span is missing its dependency attribute")
	}
	if !hasAttribute(ended[0], AttrMaxAttempts, int64(policy.MaxAttempts())) {
		t.Errorf("the span is missing its max-attempts attribute (want %d)", policy.MaxAttempts())
	}
}

func TestWritePolicyIsNamedDistinctlyInTheTrace(t *testing.T) {
	// An operator seeing a retried span needs to know immediately whether a duplicate
	// was possible. The write side makes exactly one attempt, and the span says so.
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	ctx, span := observer.Start(t.Context(), "product-service", retry.Sensitive())
	span.End()
	_ = ctx

	if !hasAttribute(recorder.Ended()[0], "ecommerce.retry.policy", "none") {
		t.Fatalf("write spans should be marked as having no retry policy")
	}
}

func TestDegradedFieldIsAnEventNotAnError(t *testing.T) {
	// The distinction that matters most for the partial-failure design. A browse query
	// that returned twenty products with every category null is a success: the response
	// was a 200 with usable data. Marking the span Error would make a degraded-but-useful
	// response indistinguishable from a failed one in an error-rate query.
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	ctx, span := observer.Start(t.Context(), "Product.category", retry.Read())
	observer.ReportDegraded(ctx, span, "Product.category", "category-service", errTransient)
	span.End()

	ended := recorder.Ended()
	if ended[0].Status().Code != otelcodes.Unset {
		t.Fatalf("status %v, want Unset: a degraded field is not a failed request", ended[0].Status().Code)
	}
	if got := eventNames(ended[0]); len(got) != 1 || got[0] != "field.degraded" {
		t.Fatalf("events %v, want field.degraded", got)
	}
	if !hasAttribute(ended[0], AttrDegraded, true) {
		t.Fatal("the degraded attribute is missing, so partial degradation is not queryable")
	}
}

func TestDependentsMakesBatchingVisible(t *testing.T) {
	// Without this, a reader has to infer from a span's children that twenty products
	// cost one call. With it, the batching is a number in a query.
	h := newHarness(t)
	recorder := h.SpanRecorder
	observer := NewObserver(h.tracer)

	ctx, span := observer.Start(t.Context(), "Product.category", retry.Read())
	observer.ReportDependents(span, 20)
	span.End()
	_ = ctx

	if !hasAttribute(recorder.Ended()[0], AttrDependents, int64(20)) {
		t.Fatal("the dependents attribute is missing")
	}
}

func TestResolversNestUnderTheRequestSpan(t *testing.T) {
	// The whole point of otelhttp: a resolver span has to be a child of the request span,
	// or the trace is a flat list and the timing relationships are lost.
	h := newHarness(t)
	recorder := h.SpanRecorder
	tracer := h.tracer

	// Stand in for the HTTP handler span.
	ctx, requestSpan := tracer.Start(t.Context(), "POST /query")

	observer := NewObserver(tracer)
	resolverCtx, resolverSpan := observer.Start(ctx, "Product.category", retry.Read())
	observer.ReportDependents(resolverSpan, 20)

	// And a downstream call beneath that.
	downCtx, downSpan := observer.Start(resolverCtx, "category-service", retry.Read())
	downSpan.End()

	resolverSpan.End()
	requestSpan.End()

	ended := recorder.Ended()
	if len(ended) != 3 {
		t.Fatalf("recorded %d spans, want 3", len(ended))
	}

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range ended {
		byName[span.Name()] = span
	}
	// Start names spans "downstream <dependency>", so a resolver span and the call it
	// makes are distinguishable by their dependency suffix.
	downstream, ok := byName["downstream category-service"]
	if !ok {
		t.Fatalf("no span named %q in %v", "downstream category-service", spanNames(ended))
	}
	resolver, ok := byName["downstream Product.category"]
	if !ok {
		t.Fatalf("no span named %q in %v", "downstream Product.category", spanNames(ended))
	}
	if !downstream.Parent().IsValid() {
		t.Fatal("the downstream span has no parent")
	}
	if downstream.Parent().SpanID() != resolver.SpanContext().SpanID() {
		t.Error("the downstream span is not a child of the resolver span")
	}
	if resolver.Parent().SpanID() != requestSpan.SpanContext().SpanID() {
		t.Error("the resolver span is not a child of the request span")
	}
	_ = downCtx
}

func TestTraceparentIsPropagatedToTheOutgoingRequest(t *testing.T) {
	// The downstream services do not extract it today, so nothing changes for them.
	// This asserts the header is produced, which is what makes their later
	// instrumentation a no-op on this side.
	h := newHarness(t)
	tracer := h.tracer

	// New sets the global propagator; the disabled path already did it, but assert it
	// directly so a future refactor of New cannot silently drop it.
	provider, err := New(Config{Enabled: false, ServiceName: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = provider

	ctx, span := tracer.Start(t.Context(), "outbound")
	carrier := propagationCarrier{}
	propagator := globalPropagator()
	propagator.Inject(ctx, &carrier)

	span.End()

	if carrier.value == "" {
		t.Fatal("no traceparent was injected; propagation is a silent no-op without a propagator")
	}
	if !strings.Contains(carrier.value, "00-") {
		t.Fatalf("traceparent %q is not W3C format", carrier.value)
	}
}

func TestDisabledProviderProducesNoSpansButStillWorks(t *testing.T) {
	// Tracing must never be the reason a request fails. A disabled provider has to
	// produce usable no-op spans and a shutdown that succeeds.
	provider, err := New(Config{Enabled: false, ServiceName: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.Tracer() == nil {
		t.Fatal("a disabled provider must still return a usable tracer")
	}
	if err := provider.Shutdown(t.Context()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	ctx, span := provider.Tracer().Start(t.Context(), "noop")
	if span.IsRecording() {
		t.Fatal("a disabled provider recorded a span")
	}
	// Every span method must be safe on a no-op span, because a caller that forgot to
	// instrument a path should get nothing rather than a panic.
	span.SetAttributes(attribute.String("k", "v"))
	span.AddEvent("event")
	span.SetStatus(otelcodes.Error, "boom")
	span.RecordError(errors.New("boom"))
	span.End()
	_ = ctx
}

func TestShutdownToleratesANilProvider(t *testing.T) {
	// main calls this unconditionally on the shutdown path, including when startup
	// failed partway and no provider was ever built.
	if err := Shutdown(t.Context(), nil); err != nil {
		t.Fatalf("shutdown of a nil provider: %v", err)
	}
}

func TestEnabledProviderFlushesOnShutdown(t *testing.T) {
	// Spans still in the batch queue when the process exits are lost, and during an
	// incident those are exactly the traces most wanted.
	recorder := &recordingExporter{}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(recorder, sdktrace.WithBatchTimeout(time.Hour)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	_, span := provider.Tracer("test").Start(t.Context(), "buffered")
	span.End()

	shutdownCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := provider.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if got := len(recorder.spans()); got != 1 {
		t.Fatalf("flushed %d spans, want 1: shutdown must drain the batch queue", got)
	}
}

// --- helpers --------------------------------------------------------------

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	out := make([]string, 0, len(spans))
	for _, span := range spans {
		out = append(out, span.Name())
	}
	return out
}

func eventNames(span sdktrace.ReadOnlySpan) []string {
	out := make([]string, 0, len(span.Events()))
	for _, event := range span.Events() {
		out = append(out, event.Name)
	}
	return out
}

func hasAttribute(span sdktrace.ReadOnlySpan, key attribute.Key, want any) bool {
	for _, attr := range span.Attributes() {
		if attr.Key != key {
			continue
		}
		switch expected := want.(type) {
		case bool:
			if attr.Value.AsBool() == expected {
				return true
			}
		case int64:
			if attr.Value.AsInt64() == expected {
				return true
			}
		case string:
			if attr.Value.AsString() == expected {
				return true
			}
		}
	}
	return false
}

type propagationCarrier struct {
	value string
}

func (c *propagationCarrier) Get(string) string { return "" }
func (c *propagationCarrier) Set(key, value string) {
	if key == "traceparent" {
		c.value = value
	}
}
func (c *propagationCarrier) Keys() []string { return []string{"traceparent"} }

// Compile-time check: the observer is usable wherever the transport wants one.
var _ grpcclient.AttemptObserver = (*Observer)(nil)

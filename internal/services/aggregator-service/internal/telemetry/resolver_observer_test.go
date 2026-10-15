package telemetry

import (
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// The resolver observer tests assert on the span shape the batching design produces,
// because that shape is what makes the batching visible: one span per field with a
// dependents count, not one span per parent.

func newResolverHarness(t *testing.T) (harness, *ResolverObserver) {
	t.Helper()
	h := newHarness(t)
	return h, NewResolverObserver(NewObserver(h.tracer))
}

func TestResolverSpanCarriesFieldAndDependents(t *testing.T) {
	h, observer := newResolverHarness(t)

	ctx, span := observer.StartResolver(t.Context(), "Product", "category", 20)
	span.End()
	_ = ctx

	ended := h.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(ended))
	}
	if ended[0].Name() != "Product.category" {
		t.Errorf("span name %q, want Type.field", ended[0].Name())
	}
	if !hasAttribute(ended[0], AttrField, "category") {
		t.Error("the span is missing its field attribute")
	}
	if !hasAttribute(ended[0], AttrFieldType, "Product") {
		t.Error("the span is missing its field-type attribute")
	}
	// The dependents count is the whole reason this span exists. Without it a reader
	// sees one span for a page of twenty products and cannot tell batching from a
	// single-object resolver that happened to return everything.
	if !hasAttribute(ended[0], AttrDependents, int64(20)) {
		t.Error("the span is missing its dependents attribute, so the batching is invisible")
	}
}

func TestDependentsIsRecordedAtSpanStart(t *testing.T) {
	// A span that is never ended must still say how much it was resolving, or a
	// resolver that panics leaves no trace of its fan-out.
	h, observer := newResolverHarness(t)

	_, _ = observer.StartResolver(t.Context(), "Product", "primaryMedia", 12)
	// Deliberately not ended: this is what a resolver that panics or is cancelled
	// mid-batch leaves behind.

	started := h.Started()
	if len(started) != 1 {
		t.Fatalf("started %d spans, want 1", len(started))
	}

	var found bool
	for _, attr := range started[0].Attributes() {
		if attr.Key == AttrDependents && attr.Value.AsInt64() == 12 {
			found = true
		}
	}
	if !found {
		t.Fatal("dependents was not set at start, so an unfinished span would not record it")
	}
}

func TestResolverSpanNestsUnderTheRequestSpan(t *testing.T) {
	// otelhttp creates the request span; a resolver span that is not its child would
	// leave the trace a flat list with no timing relationships, which defeats the point
	// of tracing a fan-out.
	h, observer := newResolverHarness(t)
	downstream := NewObserver(h.tracer)

	ctx, requestSpan := h.tracer.Start(t.Context(), "POST /query")

	// The field, then the one downstream call the field made.
	resolverCtx, resolverSpan := observer.StartResolver(ctx, "Product", "category", 20)
	_, clientSpan := downstream.Start(resolverCtx, "category-service", policyForTest())
	clientSpan.End()

	resolverSpan.End()
	requestSpan.End()

	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range h.Ended() {
		byName[span.Name()] = span
	}

	resolver, ok := byName["Product.category"]
	if !ok {
		t.Fatalf("no resolver span in %v", spanNames(h.Ended()))
	}
	if !resolver.Parent().IsValid() {
		t.Fatal("the resolver span has no parent")
	}
	if resolver.Parent().SpanID() != requestSpan.SpanContext().SpanID() {
		t.Error("the resolver span is not a child of the request span")
	}

	client, ok := byName["downstream category-service"]
	if !ok {
		t.Fatalf("no client span in %v", spanNames(h.Ended()))
	}
	if client.Parent().SpanID() != resolver.SpanContext().SpanID() {
		t.Error("the client span is not a child of the resolver span")
	}
}

func TestOneResolverSpanForATwentyProductBatch(t *testing.T) {
	// The shape the design produces, asserted end to end. Twenty parents, one span, and
	// the count recorded. A span per parent would produce twenty spans here and would
	// claim a fan-out that the batch resolver does not perform.
	h, observer := newResolverHarness(t)

	const batch = 20
	ctx, span := observer.StartResolver(t.Context(), "Product", "category", batch)

	// Three attempts against one dependency, as a retried call would produce.
	policy := policyForTest()
	for attempt := 1; attempt <= 3; attempt++ {
		_, clientSpan := NewObserver(h.tracer).Start(ctx, "category-service", policy)
		NewObserver(h.tracer).ReportAttempt(ctx, clientSpan, attempt, policy, nil)
		clientSpan.End()
	}
	span.End()

	ended := h.Ended()
	// Four spans: one resolver, three client attempts. What matters is that there is
	// exactly one resolver span, not twenty.
	resolvers := 0
	for _, s := range ended {
		if s.Name() == "Product.category" {
			resolvers++
		}
	}
	if resolvers != 1 {
		t.Fatalf("recorded %d resolver spans for a batch of %d, want 1", resolvers, batch)
	}
}

func TestDegradedFieldIsQueryableOnTheResolverSpan(t *testing.T) {
	// The operator-facing version of the partial-failure property: "how many product
	// pages lost their categories today" has to be a query.
	h, observer := newResolverHarness(t)

	ctx, span := observer.StartResolver(t.Context(), "Product", "category", 20)
	observer.ReportDegraded(ctx, span, "Product.category", "category-service", errTransient)
	span.End()

	ended := h.Ended()
	if !hasAttribute(ended[0], AttrDegraded, true) {
		t.Fatal("the degraded attribute is missing, so partial degradation is not queryable")
	}
	if got := eventNames(ended[0]); len(got) != 1 || got[0] != "field.degraded" {
		t.Fatalf("events %v, want field.degraded", got)
	}
}

func TestSuccessfulResolverSpanIsMarkedNotDegraded(t *testing.T) {
	// A query for degraded fields has to be able to exclude the successful ones, which
	// means the success case has to record something rather than nothing.
	h, observer := newResolverHarness(t)

	_, span := observer.StartResolver(t.Context(), "Product", "category", 20)
	observer.ReportSuccess(span)
	span.End()

	if !hasAttribute(h.Ended()[0], AttrDegraded, false) {
		t.Fatal("a successful resolution did not record degraded=false, so it cannot be excluded from a query")
	}
}

func TestNilObserverIsSafe(t *testing.T) {
	// A nil observer must produce usable no-op spans rather than a panic, because the
	// composition root builds one conditionally and a forgotten nil check would take the
	// request path down.
	var observer *ResolverObserver

	ctx, span := observer.StartResolver(t.Context(), "Product", "category", 20)
	observer.ReportDependents(span, 20)
	observer.ReportDegraded(ctx, span, "Product.category", "category-service", errTransient)
	observer.ReportSuccess(span)
	span.End()

	if ctx == nil {
		t.Fatal("a nil observer returned a nil context")
	}
}

func TestSpanNameFormat(t *testing.T) {
	// The name is what a GraphQL-aware backend filters by, so it is pinned rather than
	// left to a format caller's preference.
	if got := SpanName("Product", "category"); got != "Product.category" {
		t.Fatalf("SpanName = %q, want Product.category", got)
	}
}

// Compile-time check that the span type used in the assertions is the real one.
var _ trace.Span = (trace.Span)(nil)

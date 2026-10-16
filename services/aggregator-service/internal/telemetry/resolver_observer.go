package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/trace"

	"aggregator-service/graph"
)

// ResolverObserver reports GraphQL resolver work to OpenTelemetry.
//
// It implements graph.SpanObserver. The dependency direction is the point: graph owns
// the interface and this package supplies the implementation, so the resolver layer has
// no dependency on the tracing library and disabling tracing removes every resolver span
// without touching a resolver signature or a build tag.
//
// The span hierarchy is:
//
//	POST /query                        the request
//	  Product.category                 the field, once for the whole batch
//	    downstream category-service    the one call the field made
//
// A span per parent would be the wrong shape. A batch resolver makes one downstream call
// for every parent in the list, so twenty product parents produce one span with a
// dependents attribute of 20, not twenty spans. Recording it the other way would make a
// trace claim a fan-out that does not happen.
type ResolverObserver struct {
	observer *Observer
}

// NewResolverObserver builds a ResolverObserver.
func NewResolverObserver(observer *Observer) *ResolverObserver {
	return &ResolverObserver{observer: observer}
}

// StartResolver opens a span for one field resolution.
//
// fieldType and field form the span name, e.g. "Product.category", which is what a
// GraphQL-aware backend expects and what lets a query filter by field without parsing
// attributes.
func (o *ResolverObserver) StartResolver(ctx context.Context, fieldType, field string, dependents int) (context.Context, trace.Span) {
	if o == nil || o.observer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}

	tracer := o.observer.Tracer()
	ctx, span := tracer.Start(ctx, SpanName(fieldType, field),
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			AttrField.String(field),
			AttrFieldType.String(fieldType),
			// Recorded at start rather than after, so a span that is never ended still
			// says how much it was resolving.
			AttrDependents.Int(dependents),
		),
	)
	return ctx, span
}

// ReportDependents records how many parents a batch resolver served.
func (o *ResolverObserver) ReportDependents(span trace.Span, dependents int) {
	if o == nil || o.observer == nil {
		return
	}
	o.observer.ReportDependents(span, dependents)
}

// ReportDegraded records a field-level failure that did not fail the request.
func (o *ResolverObserver) ReportDegraded(ctx context.Context, span trace.Span, field, source string, err error) {
	if o == nil || o.observer == nil {
		return
	}
	o.observer.ReportDegraded(ctx, span, field, source, err)
}

// ReportSuccess marks a resolution that completed without degradation.
func (o *ResolverObserver) ReportSuccess(span trace.Span) {
	if o == nil || o.observer == nil {
		return
	}
	o.observer.ReportSuccess(span)
}

// Compile-time check. A signature change on either side should break here rather than
// at the composition root in main.
var _ graph.SpanObserver = (*ResolverObserver)(nil)

package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"aggregator-service/internal/breaker"
	"aggregator-service/internal/grpcclient"
	"aggregator-service/internal/retry"
)

// Observer reports downstream calls to OpenTelemetry.
//
// It implements grpcclient.AttemptObserver, which is the seam that keeps the gRPC
// adapter free of any OpenTelemetry import: the transport calls through an interface
// it owns, and this package supplies the implementation. The consequence is that
// disabling tracing removes every span from the request path without a single build
// tag and without the gRPC client changing shape.
//
// The span hierarchy it produces is one span per downstream operation, with retry and
// refusal as events on that span rather than as sibling spans. That is a deliberate
// choice. A span per attempt would make three attempts look like three unrelated calls
// to the same method, and the fact that they were retries of one operation — which is
// usually the whole explanation for a slow request — would be nowhere in the trace.
type Observer struct {
	tracer trace.Tracer
	// now is injectable so the tests can assert on durations without sleeping.
	now func() time.Time
}

// NewObserver builds an Observer.
func NewObserver(tracer trace.Tracer) *Observer {
	return &Observer{tracer: tracer, now: time.Now}
}

// Start opens the operation span.
//
// The span name is the dependency and the operation, e.g. "category-service GetCategory",
// because that is the granularity an operator reasons about: which dependency, and
// whether the operation as a whole or one of its attempts was slow.
func (o *Observer) Start(ctx context.Context, dependency string, policy retry.Policy) (context.Context, trace.Span) {
	if o == nil || o.tracer == nil {
		return ctx, trace.SpanFromContext(ctx)
	}

	ctx, span := o.tracer.Start(ctx, "downstream "+dependency,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			Dependency(dependency),
			AttrMaxAttempts.Int(policy.MaxAttempts()),
			// The retry policy is named rather than its delays, because the delays
			// belong on the retry events where they can be compared attempt by attempt.
			attribute.String("ecommerce.retry.policy", policyName(policy)),
		),
	)
	return ctx, span
}

// ReportAttempt records one attempt's result.
func (o *Observer) ReportAttempt(ctx context.Context, span trace.Span, attempt int, policy retry.Policy, err error) {
	if span == nil || !span.IsRecording() {
		return
	}

	attrs := []attribute.KeyValue{AttrAttempt.Int(attempt)}
	if err == nil {
		span.AddEvent("attempt.succeeded", trace.WithAttributes(attrs...))
		return
	}

	retryable := retry.Retryable(ctx, err)
	attrs = append(attrs,
		AttrRetryable.Bool(retryable),
		attribute.String("error.type", classify(err)),
	)

	if retryable && attempt < policy.MaxAttempts() {
		// The delay is not known here, so this records the decision and the next event
		// records the duration. Splitting them means a reader can see "it decided to
		// retry" and "it waited 180ms" without inferring one from the other.
		span.AddEvent("attempt.failed.retrying", trace.WithAttributes(attrs...))
		return
	}
	span.AddEvent("attempt.failed.final", trace.WithAttributes(attrs...))
}

// ReportRefused records a call the breaker declined to make.
func (o *Observer) ReportRefused(ctx context.Context, span trace.Span, dependency string, state breaker.State, err error) {
	if span == nil || !span.IsRecording() {
		return
	}

	// Set to Error rather than left unset. The service is unhealthy from the caller's
	// point of view even though it is working exactly as designed, and an error-rate
	// query should count shed traffic as failures.
	span.SetStatus(codes.Error, "circuit breaker refused the call")

	// Set as attributes as well as an event, so "how much traffic did we shed, and
	// which dependency" is one aggregate query rather than a scan over events.
	attrs := []attribute.KeyValue{
		Dependency(dependency),
		AttrBreakerState.String(state.String()),
		AttrBreakerRefused.Bool(true),
	}
	span.SetAttributes(attrs...)
	span.AddEvent("breaker.refused", trace.WithAttributes(attrs...))
}

// ReportDependents records how many parents a batch resolver served.
//
// This is the attribute that makes batching visible in a trace. Without it a reader
// sees a single span for a page of twenty products and has to infer from the absence
// of children that the batching worked; with it, the same fact is a number to query
// and aggregate on.
func (o *Observer) ReportDependents(span trace.Span, dependents int) {
	if span == nil || !span.IsRecording() {
		return
	}
	span.SetAttributes(AttrDependents.Int(dependents))
}

// ReportDegraded records a field-level failure that did not fail the request.
//
// The span is deliberately left at OK. A browse query that returned twenty products
// with every category null succeeded: the response was a 200 with usable data. Marking
// the span Error would make a degraded-but-useful response indistinguishable from a
// failed one in an error-rate query, which is the same ambiguity the response format
// avoids by putting the failure in a separate channel.
func (o *Observer) ReportDegraded(_ context.Context, span trace.Span, field, source string, err error) {
	if span == nil || !span.IsRecording() {
		return
	}

	attrs := []attribute.KeyValue{
		AttrDegraded.Bool(true),
		AttrField.String(field),
		Dependency(source),
	}
	if err != nil {
		attrs = append(attrs, attribute.String("error.type", classify(err)))
	}
	span.AddEvent("field.degraded", trace.WithAttributes(attrs...))
	span.SetAttributes(attrs...)
}

// ReportSuccess marks a resolution that completed without degradation.
//
// Recorded as an event so that a trace can distinguish "resolved from cache-free
// downstream call" from "not instrumented", which are otherwise the same empty span.
func (o *Observer) ReportSuccess(span trace.Span) {
	if span == nil || !span.IsRecording() {
		return
	}
	span.SetAttributes(AttrDegraded.Bool(false))
}

// ReportOutcome records the final result and closes the operation's status.
// Tracer returns the underlying tracer, so the resolver observer can create spans of
// its own without the Observer having to proxy every start.
func (o *Observer) Tracer() trace.Tracer {
	if o == nil {
		return nil
	}
	return o.tracer
}

func (o *Observer) ReportOutcome(ctx context.Context, span trace.Span, err error) {
	if span == nil || !span.IsRecording() {
		return
	}
	if err == nil {
		// Unset status means OK. Setting it explicitly would also be correct but adds
		// noise to every successful span.
		return
	}
	span.SetStatus(codes.Error, err.Error())
}

// Compile-time check that the Observer satisfies the transport's seam. Without it a
// signature change would break at the first call site rather than here.
var _ grpcclient.AttemptObserver = (*Observer)(nil)

// policyName renders a retry policy for an attribute.
//
// The attempt budget is the useful part; the exact delays are not, because they are
// already on the retry events and a policy's delay schedule is not something a trace
// reader acts on.
func policyName(policy retry.Policy) string {
	switch policy.MaxAttempts() {
	case 1:
		return "none"
	case 2:
		return "conservative"
	case 3:
		return "default"
	default:
		return "custom"
	}
}

// classify renders an error type for an attribute, without its message.
//
// The message goes on the error itself, where it is already recorded. Duplicating it
// into an attribute would put unbounded text into a low-cardinality field, which is the
// fastest way to make a backend expensive.
func classify(err error) string {
	switch {
	case err == nil:
		return ""
	case retry.Retryable(context.Background(), err):
		return "transient"
	default:
		return "permanent"
	}
}

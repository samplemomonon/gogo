package telemetry

import (
	"fmt"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/stats"
)

// Instrumentation lives here rather than at the call sites so the attribute keys and
// span naming are decided once. A span named differently by two callers is two series
// in the backend, and an attribute written under two spellings is an attribute that
// cannot be aggregated on.

// endpointSpanName is the name every request span carries.
//
// There is exactly one GraphQL endpoint, so naming spans by route would add cardinality
// without adding information. It also avoids a subtler problem: this handler is not
// behind a router, so otelhttp has no route pattern to name a span from and falls back
// to the bare method, which would make every span on every endpoint in the system read
// "POST".
const endpointSpanName = "POST /query"

// HTTPHandler wraps an http.Handler with tracing and metrics.
//
// otelhttp extracts an incoming traceparent and, when there is none, starts a root
// span. It also records the standard http.* semantic attributes and marks the span as
// an error for a 5xx response, which is the part that makes an error-rate query on the
// backend meaningful without any per-handler code.
//
// The span name is supplied explicitly rather than left to otelhttp's own naming. The
// operation argument is not the span name; it is only consulted when a name formatter
// is configured, and without one the name falls back to the HTTP method alone.
func HTTPHandler(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, endpointSpanName,
		otelhttp.WithSpanNameFormatter(func(operation string, _ *http.Request) string {
			return operation
		}),
	)
}

// HTTPTransport wraps an outbound http.RoundTripper.
//
// Used by the projection status client, which is the one place the aggregator speaks
// HTTP to a downstream service. Those calls land on spans that already have a parent,
// so they nest under the request rather than starting roots of their own.
//
// The tracer is not passed: otelhttp reads the global provider, which New has already
// installed by the time this is called. Passing it would mean threading the tracer
// through one more constructor for no difference.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base)
}

// GRPCClientStatsHandler instruments outbound gRPC calls.
//
// It is a stats.Handler rather than an interceptor because that is the current API.
// Behaviourally it is what an interceptor did: it starts a span per RPC, injects the
// W3C traceparent into the outgoing metadata, and sets the span to Error with the
// gRPC code as an attribute when the call fails.
//
// Traceparent injection is the part worth relying on. It happens automatically from
// the global propagator, which is why New sets one even when tracing is disabled: a
// downstream service that is later instrumented receives a valid traceparent with no
// change on this side.
//
// The returned type is grpc's stats.Handler, named so callers do not have to import
// the stats package to hold the value.
func GRPCClientStatsHandler() stats.Handler {
	return otelgrpc.NewClientHandler()
}

// Attribute keys.
//
// Namespaced so they cannot collide with the semantic conventions, and declared as
// constants so a typo is a compile error rather than a silently separate series.
const (
	// AttrDependency is the downstream service a span concerns.
	AttrDependency = attribute.Key("ecommerce.dependency")
	// AttrRPC is the full gRPC method, e.g. category.v1.CategoryService/ListCategories.
	AttrRPC = attribute.Key("rpc.grpc.method")
	// AttrGRPCStatus is the numeric gRPC code on a failed call.
	AttrGRPCStatus = attribute.Key("rpc.grpc.status_code")
	// AttrAttempt is the 1-based attempt number within a retry loop.
	AttrAttempt = attribute.Key("ecommerce.attempt")
	// AttrMaxAttempts is the retry budget the call was given.
	AttrMaxAttempts = attribute.Key("ecommerce.max_attempts")
	// AttrRetryable says whether the failure was classified as transient.
	AttrRetryable = attribute.Key("ecommerce.retryable")
	// AttrBreakerState is the breaker state at the time of the call.
	AttrBreakerState = attribute.Key("ecommerce.breaker.state")
	// AttrBreakerRefused marks a call that was never attempted because the circuit was
	// open. A query for these answers "how much traffic did we shed", which is a
	// different question from "how many calls failed".
	AttrBreakerRefused = attribute.Key("ecommerce.breaker.refused")
	// AttrField is the GraphQL field a resolver span concerns.
	AttrField = attribute.Key("graphql.field.name")
	// AttrFieldType is the GraphQL type the field belongs to.
	AttrFieldType = attribute.Key("graphql.field.type")
	// AttrDegraded marks a span that recorded a field-level failure rather than
	// failing outright.
	AttrDegraded = attribute.Key("ecommerce.degraded")
	// AttrDependents is how many parents a batch resolver resolved in one call.
	AttrDependents = attribute.Key("ecommerce.dependents")
)

// Dependency returns an attribute naming a downstream service.
func Dependency(name string) attribute.KeyValue {
	return AttrDependency.String(name)
}

// SpanName builds a resolver span name.
//
// The shape is Type.field, which is what a GraphQL-aware backend expects and what lets
// a query filter by field without parsing attributes.
func SpanName(fieldType, field string) string {
	return fmt.Sprintf("%s.%s", fieldType, field)
}

// Package telemetry sets up OpenTelemetry tracing for the aggregator.
//
// The shape of a trace from here is worth stating before anything else, because it
// is not what a reader might assume. The five downstream services in this repo do not
// extract traceparent, so a request through this service produces a tree rooted at the
// HTTP handler with client spans for each gRPC call beneath it, and nothing below
// those. The spans are honest about this service's own view and silent about
// everything past the wire.
//
// That is enough for the questions this service is asked in production: which
// dependency is slow, which field is being degraded, how many attempts a call took,
// and whether a breaker was open when the call was refused. It is not enough to see
// what a downstream service did with the request, and no amount of configuration here
// will change that. When the downstream services are instrumented, the same trace IDs
// light up with children and nothing on this side has to change.
package telemetry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Config describes the tracing setup.
type Config struct {
	// Enabled turns tracing on. Off means a no-op provider, which is the right default
	// for tests and for a deployment with no collector.
	Enabled bool
	// ServiceName identifies this service in the backend.
	ServiceName string
	// ServiceVersion is the build version, so a trace can be tied to a release.
	ServiceVersion string
	// Environment distinguishes dev from prod in the same collector.
	Environment string
	// Endpoint is the OTLP collector address, host:port. Empty uses the exporter
	// default of localhost:4317.
	Endpoint string
	// Insecure sends plaintext OTLP. Correct for a collector reached over the compose
	// network or a sidecar, wrong across a real network.
	Insecure bool
	// Headers are added to the OTLP export request, for a collector that requires
	// authentication.
	Headers map[string]string
	// SampleRatio is the fraction of traces to record, for root spans only. 1 records
	// everything.
	SampleRatio float64
	// BatchTimeout is how long the exporter waits before sending a batch. Shorter means
	// lower latency to the backend and more requests.
	BatchTimeout time.Duration
	// ExportTimeout bounds one export attempt, so a hung collector cannot hold the
	// shutdown path open.
	ExportTimeout time.Duration
}

// Provider is an initialised tracing provider plus the shutdown function that flushes
// it.
type Provider struct {
	tracerProvider *sdktrace.TracerProvider
	tracer         trace.Tracer
	shutdown       func(context.Context) error
}

// Tracer returns the tracer resolvers and clients should use.
func (p *Provider) Tracer() trace.Tracer { return p.tracer }

// Shutdown flushes any buffered spans.
//
// It must be called on the way out of the process. Without it, spans in the batch
// queue are lost, which during an incident is exactly the set of traces most wanted.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.shutdown == nil {
		return nil
	}
	return p.shutdown(ctx)
}

// tracerName is the instrumentation scope for this service's own spans. It appears on
// every span, so it is worth choosing rather than defaulting.
const tracerName = "aggregator-service"

// New builds a provider.
//
// A disabled or unconfigured setup returns a working no-op provider rather than nil,
// so callers never have to branch and a missing collector degrades to "no traces"
// instead of to a panic. The global propagator is set either way, because a
// no-op tracer provider with a real propagator still propagates context, which is what
// lets a downstream service become instrumented without changing this service.
func New(cfg Config) (*Provider, error) {
	// Without this line, propagation is a silent no-op rather than an error: traces do
	// not cross the process boundary and the trace IDs do not match anywhere, with no
	// warning to say so. It is one function, and it is easy to omit.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // W3C traceparent
		propagation.Baggage{},
	))

	if !cfg.Enabled {
		return noopProvider(), nil
	}

	exporter, err := newExporter(cfg)
	if err != nil {
		return nil, err
	}

	res, err := newResource(cfg)
	if err != nil {
		return nil, err
	}

	// A negative ratio is nonsense and is treated as unset. Zero is not: it means
	// record nothing, which is a legitimate choice for a collector that is being
	// reconfigured, and silently turning it into "record everything" would dump the
	// full trace volume into a collector that an operator just scaled down.
	sampleRatio := cfg.SampleRatio
	if sampleRatio < 0 {
		sampleRatio = 1
	}
	if sampleRatio > 1 {
		sampleRatio = 1
	}

	batchTimeout := cfg.BatchTimeout
	if batchTimeout <= 0 {
		batchTimeout = 5 * time.Second
	}
	exportTimeout := cfg.ExportTimeout
	if exportTimeout <= 0 {
		// Shorter than the batch timeout, so one hung export cannot delay the next
		// flush indefinitely.
		exportTimeout = 10 * time.Second
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(batchTimeout),
			sdktrace.WithExportTimeout(exportTimeout),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRatio))),
	)

	otel.SetTracerProvider(provider)

	return &Provider{
		tracerProvider: provider,
		tracer:         provider.Tracer(tracerName),
		shutdown:       provider.Shutdown,
	}, nil
}

// newExporter builds the OTLP exporter.
func newExporter(cfg Config) (sdktrace.SpanExporter, error) {
	opts := []otlptracegrpc.Option{}
	if cfg.Endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
	}
	if cfg.Insecure {
		// Only correct where the OTLP traffic does not leave a trusted network. The
		// alternative, TLS to the collector, is the exporter's own default when this
		// flag is absent.
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
	}

	// The exporter connects lazily, so an unreachable collector is not an error here —
	// it shows up as failed exports in the logs later, which is the right place for it.
	// What can fail is a malformed endpoint or header, and that is worth refusing at
	// startup rather than on every export.
	exporter, err := otlptracegrpc.New(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("build OTLP exporter (endpoint %q): %w", cfg.Endpoint, err)
	}
	return exporter, nil
}

// newResource describes this service to the backend.
func newResource(cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
	}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}
	if cfg.Environment != "" {
		// Keyed directly rather than through a constructor: unlike ServiceName and
		// ServiceVersion, this field has no helper in this semconv version, and using
		// the key is the documented way to set it.
		attrs = append(attrs, semconv.DeploymentEnvironmentNameKey.String(cfg.Environment))
	}

	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(attrs...),
		// Without the SDK defaults merged in, the resource carries only what is listed
		// here and loses the telemetry.sdk.* attributes a backend uses to identify the
		// emitting library.
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}
	return res, nil
}

// noopProvider is the disabled setup.
//
// It still returns a real tracer and a no-op shutdown, so the enabled and disabled
// paths are structurally identical and a caller cannot accidentally depend on one.
func noopProvider() *Provider {
	tracer := noop.NewTracerProvider().Tracer(tracerName)
	return &Provider{
		tracerProvider: nil,
		tracer:         tracer,
		shutdown:       func(context.Context) error { return nil },
	}
}

// ShutdownTimeout bounds the trace flush on the way out.
//
// The value is short on purpose. A collector that is slow to accept the final batch
// should not hold a container open past its termination grace period, because the
// spans in that batch are worth less than the pod being allowed to exit on schedule.
const ShutdownTimeout = 5 * time.Second

// Shutdown flushes a provider with the bounded timeout.
//
// It tolerates a nil provider so main can call it unconditionally on the shutdown
// path, including when startup failed partway.
func Shutdown(ctx context.Context, p *Provider) error {
	if p == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, ShutdownTimeout)
	defer cancel()
	return p.Shutdown(shutdownCtx)
}

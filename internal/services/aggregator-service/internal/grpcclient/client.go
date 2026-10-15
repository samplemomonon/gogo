// Package grpcclient dials the downstream services and adapts their protobuf
// contracts into the aggregator's domain types.
//
// Every method follows the same three rules, and they are the reason this package
// exists rather than resolvers calling generated stubs directly:
//
//  1. One deadline per call, applied here rather than trusted to the resolver.
//     A resolver that forgets a timeout holds a request open until the client's
//     own budget expires, which turns one slow dependency into a saturated
//     aggregator.
//
//  2. Status codes map to the aggregator's small error vocabulary. A resolver
//     then reports failures in domain terms, and the GraphQL layer decides how to
//     present them, without every resolver re-deriving that NotFound is 404 and
//     Unavailable is 503.
//
//  3. A read contract that answers 404 for one of several requested ids is not an
//     error for the batch. The media contract already distinguishes this by
//     omitting absent ids from its map, and the category contract answers with an
//     explicit missing list.
package grpcclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/stats"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/breaker"
	"aggregator-service/internal/graph/model"
	"aggregator-service/internal/retry"
)

// Classify converts a downstream error into the aggregator's error vocabulary.
//
// The mapping is deliberately coarse. A caller needs to know whether to retry,
// not which of a dozen codes came back, so anything that is clearly a client
// mistake collapses to ErrInvalidInput and anything ambiguous becomes
// ErrUnavailable. Treating an unmapped code as invalid input would be the
// dangerous default: it tells a caller to stop retrying a failure that may well be
// transient.
func Classify(err error) error {
	if err == nil {
		return nil
	}
	// Already classified by an inner layer; re-wrapping would lose the sentinel.
	if errors.Is(err, apperror.ErrNotFound) ||
		errors.Is(err, apperror.ErrInvalidInput) ||
		errors.Is(err, apperror.ErrConflict) ||
		errors.Is(err, apperror.ErrUnavailable) {
		return err
	}

	// A refused call is not a transport failure. It carries no gRPC status, so it
	// would otherwise fall through to the default branch below and be reported as the
	// dependency being unavailable — which is the opposite of the truth and would tell
	// a caller to retry a call that was refused precisely because retrying is pointless.
	if errors.Is(err, breaker.ErrOpenCircuit) {
		return err
	}

	switch status.Code(err) {
	case codes.OK:
		return nil

	case codes.NotFound:
		return fmt.Errorf("%w: %s", apperror.ErrNotFound, message(err))

	// InvalidArgument, OutOfRange, FailedPrecondition and AlreadyExists are all
	// decisions the downstream service made about what it was sent. Retrying the
	// same payload gets the same answer, so they are client errors.
	case codes.InvalidArgument, codes.OutOfRange, codes.FailedPrecondition, codes.AlreadyExists:
		return fmt.Errorf("%w: %s", apperror.ErrInvalidInput, message(err))

	// PermissionDenied and Unauthenticated are the client's credentials, not its
	// input. They are still not retryable, so ErrInvalidInput is the honest
	// bucket for what the aggregator can act on.
	case codes.PermissionDenied, codes.Unauthenticated:
		return fmt.Errorf("%w: %s", apperror.ErrInvalidInput, message(err))

	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return fmt.Errorf("%w: %s", apperror.ErrUnavailable, message(err))

	default:
		// An unknown code, or a transport error with no code at all, gets the
		// retryable bucket on purpose. Guessing "invalid" would suppress retries
		// for a fault nobody has characterised yet.
		return fmt.Errorf("%w: %s", apperror.ErrUnavailable, message(err))
	}
}

// message extracts the downstream detail, falling back to a generic description
// when there is none.
func message(err error) string {
	if st, ok := status.FromError(err); ok && st.Message() != "" {
		return st.Message()
	}
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// IsNotFound reports whether err is, or wraps, the not-found sentinel. The read
// contracts answer 404 per id, so a batch caller needs this to distinguish
// "absent" from "broken" without unwrapping by hand.
func IsNotFound(err error) bool {
	return errors.Is(err, apperror.ErrNotFound) || status.Code(err) == codes.NotFound
}

// Clients holds one connection per downstream service.
//
// The connections are created eagerly but dial lazily: grpc.NewClient does not block,
// so this service starts and reports ready even if a dependency is still coming up.
// That matches product-service, which dials its category and media clients the same
// way. Readiness is reported through the dependency check in the HTTP probe, where a
// failure can actually describe what is broken.
type Clients struct {
	Category CategoryAPI
	Media    MediaAPI
	Product  ProductAPI
	Listing  ListingAPI
	Search   SearchAPI

	closers []func() error

	// breakers maps service name to its breaker, so the readiness probe and a debug
	// endpoint can report circuit state without this package having to expose a
	// Breaker accessor on every client.
	breakers map[string]*breaker.Breaker
}

// Breakers returns each dependency's circuit breaker, keyed by service name.
func (c *Clients) Breakers() map[string]*breaker.Breaker { return c.breakers }

// CategoryAPI is the read side of category-service.
type CategoryAPI interface {
	GetCategory(ctx context.Context, id string) (model.Category, error)
	ListCategories(ctx context.Context, limit, offset int32) ([]model.Category, int64, error)
	CategoryTree(ctx context.Context) ([]model.Category, error)
	// CategoriesByIDs resolves several categories in one round trip. An id with
	// no category is absent from the result rather than an error, because a page
	// of products legitimately references categories the caller cannot see.
	CategoriesByIDs(ctx context.Context, ids []string) (map[string]model.Category, error)
	// DeleteImpact reports how many descendants a category has, so a client can
	// be warned before a delete cascades.
	DeleteImpact(ctx context.Context, id string) (descendants int64, err error)

	CreateCategory(ctx context.Context, in model.CreateCategoryInput) (model.Category, error)
	UpdateCategory(ctx context.Context, id string, in model.UpdateCategoryInput) (model.Category, error)
	DeleteCategory(ctx context.Context, id string) error
}

// MediaAPI is media-service.
type MediaAPI interface {
	GetMedia(ctx context.Context, id string) (model.Media, error)
	ListMedia(ctx context.Context, ownerID string, status model.MediaStatus, limit, offset int32) ([]model.Media, int64, error)
	// DownloadURLs resolves several ids to presigned URLs in one round trip.
	// Ids whose object is not READY are omitted: there is no URL to hand back,
	// and an empty string would be indistinguishable from a real one.
	DownloadURLs(ctx context.Context, ids []string, ttlSeconds int64) (map[string]string, error)
	// MediaByIDs fetches metadata for several ids in one round trip.
	MediaByIDs(ctx context.Context, ids []string) (map[string]model.Media, error)

	RegisterMedia(ctx context.Context, in model.RegisterMediaInput) (model.MediaUpload, error)
	CompleteUpload(ctx context.Context, id string) (model.Media, error)
	DeleteMedia(ctx context.Context, id string) error
}

// ProductAPI is the write side of product-service. It has no read methods, and
// that is the point: the aggregator cannot serve a read from here.
type ProductAPI interface {
	CreateProduct(ctx context.Context, in model.CreateProductInput) (model.Product, error)
	UpdateProduct(ctx context.Context, id string, in model.UpdateProductInput) (model.Product, error)
	DeleteProduct(ctx context.Context, id string) error
}

// ListingAPI is the browse read model.
type ListingAPI interface {
	GetProduct(ctx context.Context, id string, includeVariants bool) (model.Product, error)
	ListProducts(ctx context.Context, filter ProductFilter) (model.ProductPage, error)
}

// SearchAPI is the search read model.
type SearchAPI interface {
	SearchProducts(ctx context.Context, f SearchFilter) (model.SearchResult, error)
}

// ProductFilter is a browse filter, already validated.
type ProductFilter struct {
	CategoryID      string
	Status          model.Status
	MinPriceCents   int64
	MaxPriceCents   int64
	Limit           int32
	Offset          int32
	IncludeVariants bool
}

// SearchFilter is a search filter, already validated.
type SearchFilter struct {
	Query         string
	CategoryID    string
	Status        model.Status
	MinPriceCents int64
	MaxPriceCents int64
	Limit         int32
	Offset        int32
}

// Close releases every connection.
func (c *Clients) Close() error {
	var errs []error
	for _, close := range c.closers {
		if err := close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// callTimeout is the default bound applied to a single downstream call when the
// resolver did not set one.
const callTimeout = 3 * time.Second

// call is the single path every downstream attempt goes through.
//
// It applies, in order: the circuit breaker, then the retry policy, then the
// per-attempt deadline. That order is not arbitrary.
//
// The breaker is outermost because its whole purpose is to avoid the attempt
// entirely. Retrying first would spend the attempt budget against a dependency that
// has already been judged unable to take traffic, which is the load amplification the
// two mechanisms exist to prevent between them.
//
// The retry policy wraps the deadline rather than the other way round, so each
// attempt gets a fresh timeout and the policy can see how much of the request budget
// remains before deciding whether another attempt fits. A deadline outside the retry
// loop would divide one timeout between all attempts, which makes a retry nearly
// pointless.
//
// The breaker is reserved once and each attempt is reported, so one user request is
// one failure to the breaker regardless of how many attempts it took. Counting inside
// the retry loop would trip the breaker three times faster than the evidence supports.
func call(
	ctx context.Context,
	guard *breaker.Breaker,
	policy retry.Policy,
	timeout time.Duration,
	rawObserver AttemptObserver,
	invoke func(context.Context) error,
) error {
	observer := observerFor(rawObserver)
	// The span wraps the whole operation, not each attempt. A span per attempt would
	// make a retried call look like three independent calls in the trace, and the
	// relationship between them — that they were retries of one operation against one
	// dependency — is exactly what an operator is looking at.
	ctx, span := observer.Start(ctx, dependencyName(guard), policy)
	defer span.End()

	attempt := 0
	attemptCall := func(attemptCtx context.Context) error {
		attempt++
		attemptCtx, cancel := withTimeout(attemptCtx, timeout)
		defer cancel()

		// Classified here rather than by the caller, because the retry decision is
		// made on this error and the policy's predicate is defined in terms of the
		// internal error vocabulary. Handing it a raw status error would make every
		// failure look non-retryable and silently disable retrying.
		//
		// The callers' own Classify calls are unaffected: it returns an
		// already-classified error unchanged rather than re-wrapping it.
		err := Classify(invoke(attemptCtx))
		observer.ReportAttempt(ctx, span, attempt, policy, err)
		return err
	}

	if guard == nil {
		// A nil guard means the client was built without resilience wiring, which
		// happens in tests. Retrying and timing out still apply.
		return policy.Do(ctx, attemptCall)
	}

	if err := guard.Allow(); err != nil {
		// Refused without an attempt. Recorded as an event on the operation span
		// because there is no RPC to hang a child span on, and the absence of a child
		// is otherwise something a reader has to infer from a gap.
		observer.ReportRefused(ctx, span, guard.Name(), guard.State(), err)
		span.RecordError(err)
		return err
	}

	err := policy.Do(ctx, attemptCall)
	guard.Report(retry.Outcome(ctx, err))
	observer.ReportOutcome(ctx, span, err)
	return err
}

// AttemptObserver receives the events of one downstream operation, for tracing.
//
// It is an interface rather than a direct dependency on the telemetry package so that
// internal/telemetry stays a leaf: the gRPC adapter does not import OpenTelemetry, and
// the tracing behaviour can be replaced or disabled without touching the transport.
type AttemptObserver interface {
	// Start opens a span for one downstream operation. The returned context carries
	// the span, so anything the operation does nests under it.
	Start(ctx context.Context, dependency string, policy retry.Policy) (context.Context, trace.Span)
	// ReportAttempt records the outcome of one attempt, including the retry decision
	// that followed it.
	ReportAttempt(ctx context.Context, span trace.Span, attempt int, policy retry.Policy, err error)
	// ReportRefused records that the breaker refused the call without attempting it.
	ReportRefused(ctx context.Context, span trace.Span, dependency string, state breaker.State, err error)
	// ReportOutcome records the final result of the whole operation.
	ReportOutcome(ctx context.Context, span trace.Span, err error)
}

// NoopObserver is the observer used when tracing is disabled.
//
// It is a value rather than a nil check at every call site: a nil interface would need
// guarding in four places, and forgetting one would panic on the request path rather
// than at startup.
type NoopObserver struct{}

func (NoopObserver) Start(ctx context.Context, _ string, _ retry.Policy) (context.Context, trace.Span) {
	return ctx, trace.SpanFromContext(ctx)
}

func (NoopObserver) ReportAttempt(context.Context, trace.Span, int, retry.Policy, error) {}

func (NoopObserver) ReportRefused(context.Context, trace.Span, string, breaker.State, error) {}

func (NoopObserver) ReportOutcome(context.Context, trace.Span, error) {}

// observerFor returns the configured observer or the no-op one.
func observerFor(observer AttemptObserver) AttemptObserver {
	if observer == nil {
		return NoopObserver{}
	}
	return observer
}

// withTimeout bounds a downstream call. An existing earlier deadline wins, so a
// tighter budget set further up is never loosened here.
func withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = callTimeout
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			// The request budget is already spent. Returning an already-cancelled
			// context is more honest than starting a call that cannot finish.
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx, func() {}
		}
		if remaining < d {
			d = remaining
		}
	}
	return context.WithTimeout(ctx, d)
}

// grpcStatsHandler is grpc's stats.Handler, aliased so this package's exported surface
// does not force callers to import the stats package to hold the value.
type grpcStatsHandler = stats.Handler

// dependencyName is the breaker's dependency name, or a placeholder when there is no
// breaker. The span needs a name even for an unwired client, and "unprotected" is
// more useful to a reader than an empty attribute.
func dependencyName(guard *breaker.Breaker) string {
	if guard == nil {
		return "unprotected"
	}
	return guard.Name()
}

// Compile-time check that the no-op observer satisfies the interface.
var _ AttemptObserver = NoopObserver{}

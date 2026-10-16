package graph

import (
	"context"
	"net/http"
	"sort"
	"sync"

	"github.com/99designs/gqlgen/graphql"
	"go.opentelemetry.io/otel/trace"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/graph/model"
)

// gqlgen's optional-input unwrappers.
//
// A nullable input field is a pointer, and a resolver wants the value or an empty
// string. These are the nil-to-zero conversions that every mutation resolver needs.
// They live here rather than in the generated file because gqlgen only emits them
// alongside its own stubs, and a hand-written call site should not depend on a
// generated helper continuing to exist.
func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func int32Value(v *int) int32 {
	if v == nil {
		return 0
	}
	return int32(*v)
}

// mediaIDsOfProducts collects the distinct primary media ids from a page of products.
//
// Deduplicating here rather than at the call site means the batch resolver sends one id
// per distinct image, so twenty products sharing one image is a one-element request.
func mediaIDsOfProducts(products []*model.Product) []string {
	ids := make([]string, 0, len(products))
	for _, p := range products {
		if p == nil || p.PrimaryMediaID == "" {
			continue
		}
		ids = append(ids, p.PrimaryMediaID)
	}
	return uniqueMediaIDs(ids)
}

// mediaIDsOfHits is mediaIDsOfProducts for search results, which carry the same field on
// an embedded Product.
func mediaIDsOfHits(hits []*model.SearchHit) []string {
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		if h == nil || h.PrimaryMediaID == "" {
			continue
		}
		ids = append(ids, h.PrimaryMediaID)
	}
	return uniqueMediaIDs(ids)
}

// childrenIndex builds a parent-id to child-ids index over a hierarchy.
//
// Three resolvers need the same index over the same tree, and rebuilding it per
// resolver is O(resolvers x categories). Worse, rebuilding it per *parent* inside one
// resolver is O(parents x categories): for a page of 20 categories over a 500-category
// tree that is 10,000 map operations on the request path of a field most clients
// select.
func childrenIndex(all []model.Category) map[string][]string {
	index := make(map[string][]string, len(all))
	for _, c := range all {
		if c.ParentID != "" {
			index[c.ParentID] = append(index[c.ParentID], c.ID)
		}
	}
	return index
}

// subtreeCount sums the direct counts of a category and everything below it.
//
// The children index is passed in rather than rebuilt here, so the whole batch pays for
// one index.
//
// The walk is cycle-guarded. The write path rejects cyclic moves, so this is defence in
// depth against data restored from before that check, and unbounded recursion on
// malformed data is a stack exhaustion rather than a wrong number.
func subtreeCount(children map[string][]string, root string, direct map[string]int64) int64 {
	var total int64
	seen := make(map[string]struct{})
	var walk func(id string)
	walk = func(id string) {
		if _, cycle := seen[id]; cycle {
			return
		}
		seen[id] = struct{}{}
		total += direct[id]
		for _, child := range children[id] {
			walk(child)
		}
	}
	walk(root)
	return total
}

// Observer returns the configured span observer, never nil.
func (r *Resolver) Observer() SpanObserver { return observerFor(r.observer) }

// traceField opens the span for one field resolution and returns the context carrying
// it.
//
// It lives on *Resolver rather than on a per-type receiver type because gqlgen generates
// those types and rewrites them on every codegen run. Anything hand-written on them is
// lost; anything on the root is promoted through the embedded pointer and survives.
//
// The three batch resolvers and every root resolver go through here, which is what
// guarantees they all name their spans identically. A resolver that opened its own span
// with a slightly different name would show up in a backend as a separate series for
// the same field.
func (r *Resolver) traceField(ctx context.Context, fieldType, field string, dependents int) (context.Context, trace.Span) {
	observer := r.Observer()
	ctx, span := observer.StartResolver(ctx, fieldType, field, dependents)
	// The observer travels on the context because recordError is reached from helpers
	// with no receiver, and reaching the root from there would couple the error path to
	// the resolver tree. Attaching it here keeps the span and the observer that opened
	// it as the same pair.
	return withObserver(ctx, observer), span
}

// traceRoot is traceField for a root field, which has no parent list to count.
func (r *Resolver) traceRoot(ctx context.Context, fieldType, field string) (context.Context, trace.Span) {
	return r.traceField(ctx, fieldType, field, 0)
}

// signURLs stamps presigned download URLs onto a batch of media records.
//
// This replaces a resolver on Media.downloadUrl, and the reason is structural rather
// than a matter of taste. gqlgen coalesces a batch resolver only while it marshals a
// real list of parents. A product's primaryMedia is one object per product, so each of
// those Media records is marshalled singly and a field resolver on it runs once per
// object: a page of twenty products with image URLs cost twenty calls to media-service
// where one would do. Signing here, inside the batch that already fetched the metadata,
// makes it one call for the whole page.
//
// The degradation is unchanged. A failure leaves every URL nil and records one error,
// exactly as the per-instance resolver did, so a dead media-service nulls the URLs and
// leaves the products themselves intact.
//
// Only READY objects are signed. Presigning a pending upload hands back a URL that
// 404s, which is worse than null.
func (r *Resolver) signURLs(ctx context.Context, field string, media []*model.Media) {
	ids := make([]string, 0, len(media))
	for _, m := range media {
		if m == nil || m.Status != model.MediaStatusReady {
			continue
		}
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return
	}

	urls, err := r.MediaStore.DownloadURLs(ctx, uniqueMediaIDs(ids))
	if err != nil {
		recordError(ctx, err, field, "media-service")
		return
	}

	for _, m := range media {
		if m == nil || m.Status != model.MediaStatusReady {
			continue
		}
		url, ok := urls[m.ID]
		if !ok || url == "" {
			continue
		}
		signed := url
		m.DownloadURL = &signed
	}
}

// The helpers in this file implement one policy that every batch resolver needs
// and none should re-derive: how a downstream failure becomes a GraphQL response.
//
// The policy has three parts, and the third is the reason this file exists.
//
//  1. A failure on an optional field must not fail the request. Product.category
//     is nullable in the schema precisely so that category-service being down
//     degrades one field rather than one page. Returning the error from the
//     resolver would null the whole product, and since Product appears inside a
//     non-null list element, it would propagate to null the entire products list.
//     One unavailable dependency would erase the page.
//
//  2. The failure is still reported, in errors, with the path of the field it
//     affected. A silently null field is indistinguishable from a genuinely absent
//     one, which is the failure mode the read-side split in this platform already
//     had to work around: a listing service that silently ignored a search term
//     answered a different question than the one asked, and nobody could tell.
//
//  3. Everything not on the error path is left nil rather than zero. gqlgen
//     requires the returned slice to be exactly len(objs) long; a short slice
//     panics inside the generated marshaler.

// recordError attaches a non-fatal field failure to the response.
//
// This is the one place the aggregator decides what a client sees when a
// dependency is unreachable, so the wording matters. It names the capability that
// failed, never an internal address, and it says "temporarily" because the
// circuit breaker will keep failing fast until the dependency recovers.
//
// A not-found is suppressed. It is not a fault: a product may reference a category
// that has since been deleted, and a null field is the correct answer. Reporting it
// would train clients to ignore the errors array, which is the failure mode this
// whole design works to avoid.
func recordError(ctx context.Context, err error, field, source string) {
	if err == nil {
		return
	}
	if apperror.IsNotFound(err) {
		return
	}
	// Recorded on whatever span is current, which is the field's own span opened by the
	// resolver. A degradation is not an error state: the request succeeded and the
	// caller got data, so the span is left OK and carries an event instead. Marking it
	// Error would make a degraded-but-useful response indistinguishable from a failed
	// one in an error-rate query.
	recordSpanDegraded(ctx, field, source, err)
	addError(ctx, &fieldError{
		Message: source + " is temporarily unavailable; " + field + " could not be resolved",
		Path:    field,
		Source:  source,
		cause:   err,
	})
}

// spanDegrader is the seam for reporting a degradation to the span observer.
//
// A package-level function rather than a method, because the error reporting is
// reached from helpers that have no receiver. Tests substitute it for the same reason
// they substitute addError.
var spanDegrader = func(ctx context.Context, field, source string, err error) {
	if observer := spanObserverFromContext(ctx); observer != nil {
		observer.ReportDegraded(ctx, trace.SpanFromContext(ctx), field, source, err)
	}
}

func recordSpanDegraded(ctx context.Context, field, source string, err error) {
	spanDegrader(ctx, field, source, err)
}

// spanObserverFromContext finds the observer for the current resolution.
//
// It travels on the context rather than through the Resolver because recordError is
// called from places with no receiver, and reaching the root from there would couple
// the error path to the resolver tree.
func spanObserverFromContext(ctx context.Context) SpanObserver {
	observer, _ := ctx.Value(observerContextKey{}).(SpanObserver)
	return observer
}

// observerContextKey is a private key type so nothing outside this package can collide
// with it or read the observer back out.
type observerContextKey struct{}

// withObserver attaches an observer to the context for the duration of a resolution.
func withObserver(ctx context.Context, observer SpanObserver) context.Context {
	return context.WithValue(ctx, observerContextKey{}, observer)
}

// fieldError is a GraphQL error with a machine-readable classification attached.
//
// GraphQL's errors array carries a message and an optional path. Clients that
// branch on error handling need to distinguish "this field is missing because the
// upstream is down" from "you asked for something wrong", so the code travels in
// extensions rather than being encoded in prose that every client has to parse.
type fieldError struct {
	Message string
	Path    string
	Source  string
	cause   error
}

func (e *fieldError) Error() string { return e.Message }

func (e *fieldError) Unwrap() error { return e.cause }

// Extensions satisfies the gqlgen error interface, placing a stable code and the
// failing source in the response.
func (e *fieldError) Extensions() map[string]any {
	return map[string]any{
		"code":   "DEPENDENCY_UNAVAILABLE",
		"source": e.Source,
		"path":   e.Path,
		// Retryable is explicit so a client does not have to infer intent from the
		// code string.
		"retryable": true,
	}
}

// addError attaches a field failure to the response's errors array.
//
// gqlgen collects errors on the operation context rather than returning them, so
// a resolver that wants to degrade a single field has nowhere to put the error
// except here. The seam is a variable rather than a direct call so that tests can
// substitute a collector without building a full execution context.
var addError = func(ctx context.Context, err error) {
	graphql.AddError(ctx, err)
}

// SpanObserver receives resolver tracing events.
//
// The same shape as the transport's seam, and for the same reason: graph imports only
// this interface, so the resolver layer has no dependency on the tracing library and
// disabling tracing removes every span without touching a resolver signature.
//
// Start returns a context carrying the span. A batch resolver starts one span for the
// whole batch rather than one per parent, because the downstream call it makes is one:
// twenty products resolved through twenty spans would describe a fan-out that does not
// happen.
type SpanObserver interface {
	// StartResolver opens a span for one field resolution.
	StartResolver(ctx context.Context, fieldType, field string, dependents int) (context.Context, trace.Span)
	// ReportDependents records how many parents a batch resolver served, which is the
	// number that makes the batching visible in a trace rather than something a reader
	// has to infer from a span's children.
	ReportDependents(span trace.Span, dependents int)
	// ReportDegraded records a field-level failure that did not fail the request.
	ReportDegraded(ctx context.Context, span trace.Span, field, source string, err error)
	// ReportSuccess marks a resolution that completed without degradation.
	ReportSuccess(span trace.Span)
}

// noopSpanObserver is used when tracing is disabled. A value rather than a nil check at
// every call site, so a forgotten wiring produces no spans instead of a panic.
type noopSpanObserver struct{}

func (noopSpanObserver) StartResolver(ctx context.Context, _, _ string, _ int) (context.Context, trace.Span) {
	return ctx, trace.SpanFromContext(ctx)
}
func (noopSpanObserver) ReportDependents(trace.Span, int)                                  {}
func (noopSpanObserver) ReportDegraded(context.Context, trace.Span, string, string, error) {}
func (noopSpanObserver) ReportSuccess(trace.Span)                                          {}

// observerFor returns the configured observer or the no-op one.
func observerFor(observer SpanObserver) SpanObserver {
	if observer == nil {
		return noopSpanObserver{}
	}
	return observer
}

// uniqueIDs collects the distinct non-empty ids from a set of products.
//
// Deduplicating before the downstream call matters more here than it would in a
// single-object resolver: a page of 20 products in one category sends one id,
// not twenty. The category service's flat listing is fetched wholesale in either
// case, but the intent is to send the smallest possible request, and a repeated id
// in the request is a sign that the caller does not understand the cardinality.
func uniqueProductCategoryIDs(products []*model.Product) []string {
	seen := make(map[string]struct{}, len(products))
	ids := make([]string, 0, len(products))
	for _, p := range products {
		if p == nil || p.CategoryID == "" {
			continue
		}
		if _, dup := seen[p.CategoryID]; dup {
			continue
		}
		seen[p.CategoryID] = struct{}{}
		ids = append(ids, p.CategoryID)
	}
	return ids
}

// uniqueHitCategoryIDs is uniqueProductCategoryIDs for search hits.
func uniqueHitCategoryIDs(hits []*model.SearchHit) []string {
	seen := make(map[string]struct{}, len(hits))
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		if h == nil || h.CategoryID == "" {
			continue
		}
		if _, dup := seen[h.CategoryID]; dup {
			continue
		}
		seen[h.CategoryID] = struct{}{}
		ids = append(ids, h.CategoryID)
	}
	return ids
}

// uniqueMediaIDs collects distinct non-empty media ids.
func uniqueMediaIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// sortCategories orders a hierarchy slice deterministically.
//
// The category service returns a name-ordered flat listing, but a tree flattened
// from nested nodes arrives in traversal order. Breadcrumbs and children are
// derived from this slice, so a non-deterministic order would make two identical
// GraphQL responses differ between calls for no reason a client could explain.
func sortCategories(cats []model.Category) {
	sort.SliceStable(cats, func(i, j int) bool {
		if cats[i].Name != cats[j].Name {
			return cats[i].Name < cats[j].Name
		}
		return cats[i].ID < cats[j].ID
	})
}

// indexCategories builds an id lookup.
func indexCategories(cats []model.Category) map[string]model.Category {
	out := make(map[string]model.Category, len(cats))
	for _, c := range cats {
		if c.ID != "" {
			out[c.ID] = c
		}
	}
	return out
}

// ancestorsOf walks parent links to build a breadcrumb.
//
// The walk is bounded by the number of distinct categories rather than a fixed
// constant. A cycle in the stored hierarchy would otherwise loop forever, and the
// category service rejects cyclic moves on write, so this is defence in depth
// against data that predates that check or arrived through a restore.
func ancestorsOf(byID map[string]model.Category, id string) []model.Category {
	var chain []model.Category
	seen := make(map[string]struct{})
	current := id

	for current != "" {
		if _, cycle := seen[current]; cycle {
			// Stop at the repeat rather than looping. A partial breadcrumb is
			// better than hanging a request goroutine.
			break
		}
		seen[current] = struct{}{}

		cat, ok := byID[current]
		if !ok {
			break
		}
		chain = append(chain, cat)
		current = cat.ParentID
	}

	// The walk collects leaf-first; a breadcrumb reads root-first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// RequestCache memoises derived data for the duration of one request.
//
// It exists for one call: the category tree. Three resolvers on Category — ancestors,
// children and productCount — each need the whole hierarchy, and each fetched it
// independently. That is not an N+1 in the parent-count sense, but it is the same class
// of waste: the cost scales with the shape of the selection set rather than with the
// data, so a client adding a sibling field pays for the same bytes again. A query
// selecting all three costs three tree fetches where one suffices.
//
// Single-flight, not just caching. gqlgen marshals sibling fields concurrently, so two
// resolvers asking for the tree at the same moment would both miss the cache and both
// fetch it. sync.Once closes that window.
//
// Lifetime is the request, which is the right scope: a category hierarchy is read-only
// here, the whole request lives about a hundred milliseconds, and a longer-lived cache
// would need invalidation this service has no way to do correctly — the hierarchy
// belongs to category-service, not to the aggregator.
type RequestCache struct {
	treeOnce sync.Once
	tree     []model.Category
	treeErr  error
}

// requestCacheKey is a private type so nothing outside this package can read the cache
// back out of a context or collide with the key.
type requestCacheKey struct{}

// WithRequestCache attaches a cache to ctx and returns the new context.
//
// Called once per request by the handler middleware. A context without one still works:
// cacheFromContext reports absent and the resolvers fetch directly, which is the correct
// behaviour for a caller that never installed one rather than a silent loss of caching.
func WithRequestCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestCacheKey{}, &RequestCache{})
}

// cacheFromContext returns the request's cache, if one was installed.
func cacheFromContext(ctx context.Context) (*RequestCache, bool) {
	cache, ok := ctx.Value(requestCacheKey{}).(*RequestCache)
	return cache, ok
}

// categoryTree returns the category hierarchy, at most once per request.
//
// The error is cached alongside the data. That is deliberate: within one request the
// dependency is either reachable or it is not, and a retry inside the same request
// would spend budget that the failing sibling fields have already spent. A cancelled
// first caller therefore poisons the tree for the rest of the request, which is correct
// — that request is already failing.
func (r *Resolver) categoryTree(ctx context.Context) ([]model.Category, error) {
	cache, ok := cacheFromContext(ctx)
	if !ok {
		return r.CategoryStore.CategoryTree(ctx)
	}

	cache.treeOnce.Do(func() {
		cache.tree, cache.treeErr = r.CategoryStore.CategoryTree(ctx)
	})
	return cache.tree, cache.treeErr
}

// RequestCacheMiddleware attaches a RequestCache to every request.
//
// It is exported because the wiring belongs in the composition root: internal/handler
// must not import this package, so the transport cannot install the cache itself.
//
// It sits outside the request-budget middleware in the chain rather than inside it.
// That is deliberate and harmless: the middleware attaches a value and does no work, and
// the budget middleware derives a child context that preserves values, so resolvers
// still see the cache and still see the deadline.
func RequestCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithRequestCache(r.Context())))
	})
}

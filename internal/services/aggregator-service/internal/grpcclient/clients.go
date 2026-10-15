package grpcclient

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"aggregator-service/gen/categorypb"
	"aggregator-service/gen/listingpb"
	"aggregator-service/gen/mediapb"
	"aggregator-service/gen/productpb"
	"aggregator-service/gen/searchpb"
	"aggregator-service/internal/breaker"
	"aggregator-service/internal/graph/model"
	"aggregator-service/internal/retry"
)

// Targets holds the downstream addresses and resilience settings, read from config.
type Targets struct {
	Category string
	Media    string
	Product  string
	Listing  string
	Search   string
	// Timeout bounds a single attempt at one downstream call.
	Timeout time.Duration
	// Breaker configures the circuit breaker guarding each dependency. One breaker
	// per service, never shared: a failing media-service must not open the gate for
	// category-service.
	Breaker breaker.Options
	// OnBreakerStateChange is called whenever a breaker transitions. Optional, and
	// wired to the logger in main so a flap is visible without reading traces.
	OnBreakerStateChange func(name string, from, to breaker.State)
	// Observer receives tracing events for every downstream call. Nil installs the
	// no-op observer, so tracing can be disabled without changing this package's shape.
	Observer AttemptObserver
	// StatsHandler instruments the connections themselves. Nil installs none, which is
	// the correct behaviour when tracing is disabled: the transport then creates no
	// spans and injects no traceparent.
	StatsHandler grpcStatsHandler
	// ReadPolicy is the retry policy for idempotent calls. Zero value means a single
	// attempt, so a caller that does not care about retries gets none by default rather
	// than an unconfigured loop.
	ReadPolicy retry.Policy
	// WritePolicy is the retry policy for mutations. Left at a single attempt on purpose:
	// product-service has no idempotency key, so a repeated CreateProduct is a second
	// product. A caller can supply a longer policy deliberately.
	WritePolicy retry.Policy
}

// New dials every downstream service and returns the adapters.
//
// grpc.NewClient dials lazily, so a dependency that is still starting does not block
// this service from coming up. Errors are returned only for configuration problems,
// not for reachability.
//
// The write side gets no retries. product-service has no idempotency key on
// CreateProduct, so a repeated call creates a second product; the cost of a duplicate
// is much higher than the cost of a failed write the client retries deliberately.
func New(targets Targets) (*Clients, error) {
	if targets.Timeout <= 0 {
		targets.Timeout = callTimeout
	}

	dial := func(target string) (*grpc.ClientConn, func() error, error) {
		if target == "" {
			return nil, nil, fmt.Errorf("empty grpc target")
		}
		opts := []grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			// WaitForReady is deliberately NOT set, which is worth explaining because
			// it is the setting a resilient client usually reaches for first.
			//
			// WaitForReady(true) holds a failing call pending until its context expires,
			// turning a refused connection into a full-timeout wait. Measured on a dead
			// peer with a 3s attempt budget: 3s with WaitForReady, 0s without. A
			// dependency that is down therefore makes every attempt cost the whole
			// timeout, and the retry loop then multiplies that by its attempt budget.
			//
			// It also defeats the circuit breaker. The breaker only sees a failure once
			// an attempt returns, so a 3s-per-attempt failure means it takes 15s of
			// traffic before a breaker set to open after five consecutive failures trips
			// — which is the whole incident. Without WaitForReady the same breaker opens
			// in well under a second, because each attempt fails immediately.
			//
			// What WaitForReady buys is tolerance of a brief reconnect during a rolling
			// restart, and the retry loop already provides that: Unavailable is
			// classified retryable, so a second attempt re-dials and succeeds once the
			// peer is back. Retry is the better mechanism here because it also records
			// what happened — a span event per attempt, a breaker report, a log line —
			// whereas WaitForReady hides the reconnect inside one opaque call.
		}
		if targets.StatsHandler != nil {
			// The stats handler creates a span per RPC and injects the W3C traceparent
			// into the outgoing metadata. It is a connection option rather than a
			// per-call interceptor so it applies to every call on the connection,
			// including the ones the fan-out in MediaByIDs makes concurrently.
			opts = append(opts, grpc.WithStatsHandler(targets.StatsHandler))
		}

		conn, err := grpc.NewClient(target, opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("dial %s: %w", target, err)
		}
		return conn, conn.Close, nil
	}

	categoryConn, closeCategory, err := dial(targets.Category)
	if err != nil {
		return nil, err
	}
	mediaConn, closeMedia, err := dial(targets.Media)
	if err != nil {
		_ = closeCategory()
		return nil, err
	}
	productConn, closeProduct, err := dial(targets.Product)
	if err != nil {
		_ = closeCategory()
		_ = closeMedia()
		return nil, err
	}
	listingConn, closeListing, err := dial(targets.Listing)
	if err != nil {
		_ = closeCategory()
		_ = closeMedia()
		_ = closeProduct()
		return nil, err
	}
	searchConn, closeSearch, err := dial(targets.Search)
	if err != nil {
		_ = closeCategory()
		_ = closeMedia()
		_ = closeProduct()
		_ = closeListing()
		return nil, err
	}

	// A zero-value policy already behaves as a single attempt, so the defaults need no
	// special handling here: a caller that configures nothing gets no retries.
	readPolicy, writePolicy := targets.ReadPolicy, targets.WritePolicy

	// One breaker per dependency. The options are shared but the Name is not, because
	// the name is what identifies the dependency in the error a caller sees and in the
	// transition log.
	breakerFor := func(name string) *breaker.Breaker {
		opts := targets.Breaker
		opts.Name = name
		opts.OnStateChange = targets.OnBreakerStateChange
		// A probe is bounded below the per-attempt timeout so a half-open breaker
		// notices a dependency that accepts connections and stalls, instead of wedging
		// on a call that will never return.
		if opts.ProbeTimeout <= 0 && targets.Timeout > 0 {
			opts.ProbeTimeout = targets.Timeout / 2
		}
		return breaker.New(opts)
	}

	guards := guardRegistry{}
	return &Clients{
		breakers: guards,
		Category: &categoryClient{
			api: categorypb.NewCategoryServiceClient(categoryConn),
			// Category reads are the highest-volume dependency in a browse query, so
			// they get the full attempt budget.
			timeout:  targets.Timeout,
			policy:   readPolicy,
			guard:    guards.register("category-service", breakerFor("category-service")),
			observer: observerFor(targets.Observer),
		},
		Media: &mediaClient{
			api:      mediapb.NewMediaServiceClient(mediaConn),
			timeout:  targets.Timeout,
			policy:   readPolicy,
			guard:    guards.register("media-service", breakerFor("media-service")),
			observer: observerFor(targets.Observer),
		},
		Product: &productClient{
			api:     productpb.NewProductServiceClient(productConn),
			timeout: targets.Timeout,
			// No retries unless the caller deliberately supplies a longer policy: a
			// repeated CreateProduct is a second product.
			policy:   writePolicy,
			guard:    guards.register("product-service", breakerFor("product-service")),
			observer: observerFor(targets.Observer),
		},
		Listing: &listingClient{
			api:      listingpb.NewProductListingServiceClient(listingConn),
			timeout:  targets.Timeout,
			policy:   readPolicy,
			guard:    guards.register("product-listing-service", breakerFor("product-listing-service")),
			observer: observerFor(targets.Observer),
		},
		Search: &searchClient{
			api:      searchpb.NewProductSearchServiceClient(searchConn),
			timeout:  targets.Timeout,
			policy:   readPolicy,
			guard:    guards.register("product-search-service", breakerFor("product-search-service")),
			observer: observerFor(targets.Observer),
		},
		closers: []func() error{
			closeCategory, closeMedia, closeProduct, closeListing, closeSearch,
		},
	}, nil
}

// guardRegistry collects the breakers so Clients can expose them by name.
//
// A small helper rather than building the map inline at each construction site, so
// adding a dependency cannot forget to register its breaker and leave it invisible to
// the readiness probe.
type guardRegistry map[string]*breaker.Breaker

// register records a breaker under its dependency name.
func (r guardRegistry) register(name string, b *breaker.Breaker) *breaker.Breaker {
	r[name] = b
	return b
}

// --- category -------------------------------------------------------------

// Each client below carries the same three resilience fields, and they are read on
// every call rather than captured in a closure:
//
//	timeout  bounds one attempt
//	policy   how many attempts are permitted, and how long to wait between them
//	guard    the circuit breaker for that one dependency
//
// Keeping them as fields rather than decorating the connection means the breaker is
// per service, not per connection: several clients share one grpc.ClientConn in a
// future refactor, and a connection-scoped breaker would conflate them.
type categoryClient struct {
	api     categorypb.CategoryServiceClient
	timeout time.Duration
	policy  retry.Policy
	guard   *breaker.Breaker
	// observer receives tracing events. Noop when tracing is disabled.
	observer AttemptObserver
}

func (c *categoryClient) GetCategory(ctx context.Context, id string) (model.Category, error) {
	parsed, err := parseID(id)
	if err != nil {
		return model.Category{}, err
	}
	var resp *categorypb.GetCategoryResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.GetCategory(ctx, &categorypb.GetCategoryRequest{Id: parsed})
		return callErr
	})
	if err != nil {
		return model.Category{}, Classify(err)
	}
	if resp.GetCategory() == nil {
		// A success carrying no category is a contract violation, not an absence.
		// Reporting it as unavailable says "retry", which is the safe reading of a
		// response we cannot interpret.
		return model.Category{}, fmt.Errorf("%w: category-service returned an empty category", unavailableSentinel)
	}
	return categoryFromProto(resp.GetCategory()), nil
}

func (c *categoryClient) ListCategories(ctx context.Context, limit, offset int32) ([]model.Category, int64, error) {
	var resp *categorypb.ListCategoriesResponse
	err := call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.ListCategories(ctx, &categorypb.ListCategoriesRequest{Limit: limit, Offset: offset})
		return callErr
	})
	if err != nil {
		return nil, 0, Classify(err)
	}
	out := make([]model.Category, 0, len(resp.GetCategories()))
	for _, cat := range resp.GetCategories() {
		out = append(out, categoryFromProto(cat))
	}
	return out, resp.GetTotal(), nil
}

func (c *categoryClient) CategoryTree(ctx context.Context) ([]model.Category, error) {
	var resp *categorypb.GetCategoryTreeResponse
	err := call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.GetCategoryTree(ctx, &categorypb.GetCategoryTreeRequest{})
		return callErr
	})
	if err != nil {
		return nil, Classify(err)
	}
	return flattenTree(resp.GetRoots()), nil
}

// CategoriesByIDs resolves a set of categories in a single round trip.
//
// The contract has no batch fetch, only CategoryExists, which answers presence
// without the data. Two options were available: call GetCategory per id, or fetch
// the flat listing once and index it. The listing is chosen because a page of
// products references a handful of categories while the flat list is bounded at
// 500 by the service, so one call serves any realistic page and the alternative
// would be N calls. The bound is why this is safe rather than a scan: if the
// category count ever exceeds the page limit, this would need to become a real
// batch RPC on the owning service.
func (c *categoryClient) CategoriesByIDs(ctx context.Context, ids []string) (map[string]model.Category, error) {
	if len(ids) == 0 {
		return map[string]model.Category{}, nil
	}

	parsed := make([]int64, 0, len(ids))
	for _, id := range ids {
		v, err := parseID(id)
		if err != nil {
			continue
		}
		parsed = append(parsed, v)
	}
	if len(parsed) == 0 {
		return map[string]model.Category{}, nil
	}

	ctx, cancel := withTimeout(ctx, c.timeout)
	defer cancel()

	// categoryPageLimit mirrors the listing service's own maximum page size. A
	// page of products cannot meaningfully reference more categories than this.
	var resp *categorypb.ListCategoriesResponse
	err := call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.ListCategories(ctx, &categorypb.ListCategoriesRequest{Limit: categoryPageLimit})
		return callErr
	})
	if err != nil {
		return nil, Classify(err)
	}

	want := make(map[int64]struct{}, len(parsed))
	for _, id := range parsed {
		want[id] = struct{}{}
	}

	out := make(map[string]model.Category, len(parsed))
	for _, cat := range resp.GetCategories() {
		if _, ok := want[cat.GetId()]; !ok {
			continue
		}
		out[strconv.FormatInt(cat.GetId(), 10)] = categoryFromProto(cat)
	}
	return out, nil
}

func (c *categoryClient) DeleteImpact(ctx context.Context, id string) (int64, error) {
	parsed, err := parseID(id)
	if err != nil {
		return 0, err
	}
	ctx, cancel := withTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.api.GetCategoryTree(ctx, &categorypb.GetCategoryTreeRequest{})
	if err != nil {
		return 0, Classify(err)
	}
	return countDescendants(resp.GetRoots(), parsed), nil
}

func (c *categoryClient) CreateCategory(ctx context.Context, in model.CreateCategoryInput) (model.Category, error) {
	// category-service exposes only reads over gRPC, by design: mutations stay on
	// its REST API so that only the owning service writes its own table. Validating
	// the input first still matters: an id that is not an integer is the caller's
	// mistake, and reporting it as such is more useful than reporting that the
	// capability is missing.
	if _, err := parseOptionalID(in.ParentID); err != nil {
		return model.Category{}, err
	}
	return model.Category{}, fmt.Errorf(
		"%w: category writes are not exposed over gRPC; use the category service's REST API",
		unavailableSentinel,
	)
}

func (c *categoryClient) UpdateCategory(ctx context.Context, id string, in model.UpdateCategoryInput) (model.Category, error) {
	return model.Category{}, fmt.Errorf(
		"%w: category writes are not exposed over gRPC; use the category service's REST API",
		unavailableSentinel,
	)
}

func (c *categoryClient) DeleteCategory(ctx context.Context, id string) error {
	return fmt.Errorf(
		"%w: category writes are not exposed over gRPC; use the category service's REST API",
		unavailableSentinel,
	)
}

// categoryPageLimit matches the category service's maximum page size.
const categoryPageLimit = 500

// --- media ----------------------------------------------------------------

type mediaClient struct {
	api     mediapb.MediaServiceClient
	timeout time.Duration
	policy  retry.Policy
	guard   *breaker.Breaker
	// observer receives tracing events. Noop when tracing is disabled.
	observer AttemptObserver
}

func (c *mediaClient) GetMedia(ctx context.Context, id string) (model.Media, error) {
	parsed, err := parseID(id)
	if err != nil {
		return model.Media{}, err
	}
	var resp *mediapb.GetMediaResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.GetMedia(ctx, &mediapb.GetMediaRequest{Id: parsed})
		return callErr
	})
	if err != nil {
		return model.Media{}, Classify(err)
	}
	if resp.GetMedia() == nil {
		return model.Media{}, fmt.Errorf("%w: media-service returned an empty media record", unavailableSentinel)
	}
	return mediaFromProto(resp.GetMedia()), nil
}

func (c *mediaClient) ListMedia(ctx context.Context, ownerID string, status model.MediaStatus, limit, offset int32) ([]model.Media, int64, error) {
	protoStatus, err := mediaStatusToProto(status)
	if err != nil {
		return nil, 0, err
	}
	var resp *mediapb.ListMediaResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.ListMedia(ctx, &mediapb.ListMediaRequest{
			OwnerId: ownerID,
			Status:  protoStatus,
			Limit:   limit,
			Offset:  offset,
		})
		return callErr
	})
	if err != nil {
		return nil, 0, Classify(err)
	}
	out := make([]model.Media, 0, len(resp.GetMedia()))
	for _, m := range resp.GetMedia() {
		out = append(out, mediaFromProto(m))
	}
	return out, resp.GetTotal(), nil
}

// DownloadURLs is the batch path a page of products needs: one round trip for
// every image on the page instead of one per image.
func (c *mediaClient) DownloadURLs(ctx context.Context, ids []string, ttlSeconds int64) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	parsed := make([]int64, 0, len(ids))
	for _, id := range ids {
		v, err := parseID(id)
		if err != nil {
			continue
		}
		parsed = append(parsed, v)
	}
	if len(parsed) == 0 {
		return map[string]string{}, nil
	}

	var resp *mediapb.BatchGetDownloadURLsResponse
	err := call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.BatchGetDownloadURLs(ctx, &mediapb.BatchGetDownloadURLsRequest{
			Ids:               parsed,
			PresignTtlSeconds: ttlSeconds,
		})
		return callErr
	})
	if err != nil {
		return nil, Classify(err)
	}

	// The contract keys this map by int64 and omits ids without a READY object.
	// Re-keying by string here keeps the rest of the aggregator working in the
	// same id representation as GraphQL.
	out := make(map[string]string, len(resp.GetDownloadUrls()))
	for id, url := range resp.GetDownloadUrls() {
		if url == "" {
			continue
		}
		out[strconv.FormatInt(id, 10)] = url
	}
	return out, nil
}

// MediaByIDs fetches metadata for several ids.
//
// The media contract has no batch metadata RPC, only a batch URL one. Fetching
// per id would reintroduce the N+1 this service exists to avoid, so this issues
// the calls concurrently instead. Concurrency is bounded because an unbounded
// fan-out is its own outage: one slow id would otherwise hold the whole page.
func (c *mediaClient) MediaByIDs(ctx context.Context, ids []string) (map[string]model.Media, error) {
	if len(ids) == 0 {
		return map[string]model.Media{}, nil
	}

	// Ids that are not integers are dropped before the fan-out. They are caller
	// mistakes, not outages, and reporting them as a dependency failure would be a
	// misdiagnosis. A product's media id came from a read model, so this is defensive
	// rather than a path a healthy caller takes.
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := parseID(id); err != nil {
			continue
		}
		valid = append(valid, id)
	}
	if len(valid) == 0 {
		return map[string]model.Media{}, nil
	}

	ctx, cancel := withTimeout(ctx, c.timeout)
	defer cancel()

	type result struct {
		media model.Media
		// missing is set for an absent record, which is an expected outcome rather
		// than a failure: a product can reference media that was since deleted.
		missing bool
		err     error
	}
	results := make([]result, len(valid))

	// Bounded concurrency, and not errgroup: this is a fan-out whose callers populate
	// optional fields, so one id failing must not discard the ids that succeeded. The
	// bound matters in the other direction too, because an unbounded fan-out would let
	// one slow id hold the whole page.
	sem := make(chan struct{}, maxConcurrentLookups)
	done := make(chan struct{}, len(valid))

	for i, id := range valid {
		go func(i int, id string) {
			defer func() { done <- struct{}{} }()
			sem <- struct{}{}
			defer func() { <-sem }()

			parsed, err := parseID(id)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			var resp *mediapb.GetMediaResponse
			err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
				var callErr error
				resp, callErr = c.api.GetMedia(ctx, &mediapb.GetMediaRequest{Id: parsed})
				return callErr
			})
			switch {
			case err != nil:
				if IsNotFound(err) {
					results[i] = result{missing: true}
					return
				}
				results[i] = result{err: Classify(err)}
			case resp.GetMedia() == nil:
				results[i] = result{err: fmt.Errorf("%w: media-service returned an empty record for %s", unavailableSentinel, id)}
			default:
				results[i] = result{media: mediaFromProto(resp.GetMedia())}
			}
		}(i, id)
	}
	for range valid {
		<-done
	}

	out := make(map[string]model.Media, len(valid))

	// Absences and failures are collected separately because they mean opposite things
	// to the caller. A missing record is absent from the map and produces no error; a
	// transport failure is returned so the resolver can report it and a client can
	// retry.
	//
	// Collapsing the two is the bug this shape exists to avoid: it would make a dead
	// media-service look exactly like a product with no image.
	var failures []error
	for i, id := range valid {
		switch {
		case results[i].missing:
			// Expected, and checked first: an absent record carries no error, so
			// testing err == nil ahead of this would insert the zero Media under that
			// id and make a missing image look like a present empty one.
		case results[i].err == nil:
			out[id] = results[i].media
		default:
			failures = append(failures, results[i].err)
		}
	}

	if len(failures) > 0 {
		// Some or all ids failed, so the caller cannot treat the map as complete. The
		// partial map is returned alongside the error and the resolver decides whether
		// to degrade the field or fail the request.
		return out, errors.Join(failures...)
	}
	return out, nil
}

func (c *mediaClient) RegisterMedia(ctx context.Context, in model.RegisterMediaInput) (model.MediaUpload, error) {
	kind, err := mediaKindToProto(in.Kind)
	if err != nil {
		return model.MediaUpload{}, err
	}
	var resp *mediapb.RegisterMediaResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.RegisterMedia(ctx, &mediapb.RegisterMediaRequest{
			OwnerId:     in.OwnerID,
			Filename:    in.Filename,
			ContentType: in.ContentType,
			Kind:        kind,
			SizeBytes:   in.SizeBytes,
		})
		return callErr
	})
	if err != nil {
		return model.MediaUpload{}, Classify(err)
	}
	if resp.GetMedia() == nil || resp.GetUpload() == nil {
		return model.MediaUpload{}, fmt.Errorf("%w: media-service returned an incomplete registration", unavailableSentinel)
	}

	upload := resp.GetUpload()
	headers := make([]Header, 0, len(upload.GetHeaders()))
	// Sorted so the GraphQL response is stable across calls. The map iteration
	// order is random, and an unstable header list makes clients diffing two
	// responses see spurious changes.
	names := make([]string, 0, len(upload.GetHeaders()))
	for name := range upload.GetHeaders() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		headers = append(headers, Header{Name: name, Value: upload.GetHeaders()[name]})
	}

	return MediaUpload{
		Media:  mediaFromProto(resp.GetMedia()),
		Upload: uploadTargetFromProto(upload, headers),
	}, nil
}

func (c *mediaClient) CompleteUpload(ctx context.Context, id string) (model.Media, error) {
	parsed, err := parseID(id)
	if err != nil {
		return model.Media{}, err
	}
	var resp *mediapb.CompleteUploadResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.CompleteUpload(ctx, &mediapb.CompleteUploadRequest{Id: parsed})
		return callErr
	})
	if err != nil {
		return model.Media{}, Classify(err)
	}
	if resp.GetMedia() == nil {
		return model.Media{}, fmt.Errorf("%w: media-service returned an empty media record", unavailableSentinel)
	}
	return mediaFromProto(resp.GetMedia()), nil
}

func (c *mediaClient) DeleteMedia(ctx context.Context, id string) error {
	parsed, err := parseID(id)
	if err != nil {
		return err
	}
	return call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		_, err := c.api.DeleteMedia(ctx, &mediapb.DeleteMediaRequest{Id: parsed})
		return err
	})
}

// maxConcurrentLookups bounds the per-id fan-out in MediaByIDs.
const maxConcurrentLookups = 8

// --- product (write side) -------------------------------------------------

type productClient struct {
	api     productpb.ProductServiceClient
	timeout time.Duration
	policy  retry.Policy
	guard   *breaker.Breaker
	// observer receives tracing events. Noop when tracing is disabled.
	observer AttemptObserver
}

func (c *productClient) CreateProduct(ctx context.Context, in model.CreateProductInput) (model.Product, error) {
	categoryID, err := parseID(in.CategoryID)
	if err != nil {
		return model.Product{}, err
	}
	primaryMediaID, err := parseOptionalID(in.PrimaryMediaID)
	if err != nil {
		return model.Product{}, err
	}
	status, err := statusToProto(in.Status)
	if err != nil {
		return model.Product{}, err
	}

	variants := make([]*productpb.VariantSpec, 0, len(in.Variants))
	for _, v := range in.Variants {
		mediaID, err := parseOptionalID(v.MediaID)
		if err != nil {
			return model.Product{}, err
		}
		variants = append(variants, &productpb.VariantSpec{
			Sku:        v.SKU,
			Name:       v.Name,
			PriceCents: v.PriceCents,
			Currency:   v.Currency,
			Stock:      v.Stock,
			MediaId:    mediaID,
		})
	}

	var resp *productpb.CreateProductResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.CreateProduct(ctx, &productpb.CreateProductRequest{
			CategoryId:     categoryID,
			Name:           in.Name,
			Slug:           in.Slug,
			Description:    in.Description,
			Brand:          in.Brand,
			PriceCents:     in.PriceCents,
			Currency:       in.Currency,
			Stock:          in.Stock,
			Status:         status,
			PrimaryMediaId: primaryMediaID,
			Variants:       variants,
		})
		return callErr
	})
	if err != nil {
		return model.Product{}, Classify(err)
	}
	if resp.GetProduct() == nil {
		return model.Product{}, fmt.Errorf("%w: product-service returned an empty product", unavailableSentinel)
	}
	return productFromProto(resp.GetProduct()), nil
}

func (c *productClient) UpdateProduct(ctx context.Context, id string, in model.UpdateProductInput) (model.Product, error) {
	parsed, err := parseID(id)
	if err != nil {
		return model.Product{}, err
	}

	// Every field is optional in the contract, so presence is the signal. A nil
	// pointer here means "leave the column alone"; a pointer to the zero value
	// means "set it to zero". Conflating the two would make it impossible to clear
	// a field, and to distinguish an intentional clear from an omission.
	req := &productpb.UpdateProductRequest{Id: parsed}
	if in.CategoryID != nil {
		v, err := parseID(*in.CategoryID)
		if err != nil {
			return model.Product{}, err
		}
		req.CategoryId = &v
	}
	if in.Name != nil {
		req.Name = in.Name
	}
	if in.Slug != nil {
		req.Slug = in.Slug
	}
	if in.Description != nil {
		req.Description = in.Description
	}
	if in.Brand != nil {
		req.Brand = in.Brand
	}
	if in.PriceCents != nil {
		req.PriceCents = in.PriceCents
	}
	if in.Currency != nil {
		req.Currency = in.Currency
	}
	if in.Stock != nil {
		req.Stock = in.Stock
	}
	if in.Status != nil {
		v, err := statusToProto(*in.Status)
		if err != nil {
			return model.Product{}, err
		}
		req.Status = &v
	}
	if in.PrimaryMediaID != nil {
		v, err := parseID(*in.PrimaryMediaID)
		if err != nil {
			return model.Product{}, err
		}
		req.PrimaryMediaId = &v
	}

	var resp *productpb.UpdateProductResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.UpdateProduct(ctx, req)
		return callErr
	})
	if err != nil {
		return model.Product{}, Classify(err)
	}
	if resp.GetProduct() == nil {
		return model.Product{}, fmt.Errorf("%w: product-service returned an empty product", unavailableSentinel)
	}
	return productFromProto(resp.GetProduct()), nil
}

func (c *productClient) DeleteProduct(ctx context.Context, id string) error {
	parsed, err := parseID(id)
	if err != nil {
		return err
	}
	return call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		_, err := c.api.DeleteProduct(ctx, &productpb.DeleteProductRequest{Id: parsed})
		return err
	})
}

// --- listing (browse read model) ------------------------------------------

type listingClient struct {
	api     listingpb.ProductListingServiceClient
	timeout time.Duration
	policy  retry.Policy
	guard   *breaker.Breaker
	// observer receives tracing events. Noop when tracing is disabled.
	observer AttemptObserver
}

func (c *listingClient) GetProduct(ctx context.Context, id string, includeVariants bool) (model.Product, error) {
	parsed, err := parseID(id)
	if err != nil {
		return model.Product{}, err
	}
	var resp *listingpb.GetProductResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.GetProduct(ctx, &listingpb.GetProductRequest{
			Id:              parsed,
			IncludeVariants: includeVariants,
		})
		return callErr
	})
	if err != nil {
		return model.Product{}, Classify(err)
	}
	if resp.GetProduct() == nil {
		return model.Product{}, fmt.Errorf("%w: listing-service returned an empty product", unavailableSentinel)
	}
	p := listingProductFromProto(resp.GetProduct())
	p.HasVariants = includeVariants
	return p, nil
}

func (c *listingClient) ListProducts(ctx context.Context, filter ProductFilter) (model.ProductPage, error) {
	categoryID, err := parseOptionalID(filter.CategoryID)
	if err != nil {
		return model.ProductPage{}, err
	}
	status, err := listingStatusToProto(filter.Status)
	if err != nil {
		return model.ProductPage{}, err
	}

	var resp *listingpb.ListProductsResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.ListProducts(ctx, &listingpb.ListProductsRequest{
			CategoryId:      categoryID,
			Status:          status,
			MinPriceCents:   filter.MinPriceCents,
			MaxPriceCents:   filter.MaxPriceCents,
			Limit:           filter.Limit,
			Offset:          filter.Offset,
			IncludeVariants: filter.IncludeVariants,
		})
		return callErr
	})
	if err != nil {
		return model.ProductPage{}, Classify(err)
	}

	page := model.ProductPage{
		Products: make([]model.Product, 0, len(resp.GetProducts())),
		Total:    resp.GetTotal(),
		Limit:    filter.Limit,
		Offset:   filter.Offset,
	}
	for _, p := range resp.GetProducts() {
		product := listingProductFromProto(p)
		product.HasVariants = filter.IncludeVariants
		page.Products = append(page.Products, product)
	}
	return page, nil
}

// --- search ---------------------------------------------------------------

type searchClient struct {
	api     searchpb.ProductSearchServiceClient
	timeout time.Duration
	policy  retry.Policy
	guard   *breaker.Breaker
	// observer receives tracing events. Noop when tracing is disabled.
	observer AttemptObserver
}

func (c *searchClient) SearchProducts(ctx context.Context, f SearchFilter) (model.SearchResult, error) {
	categoryID, err := parseOptionalID(f.CategoryID)
	if err != nil {
		return model.SearchResult{}, err
	}
	status, err := searchStatusToProto(f.Status)
	if err != nil {
		return model.SearchResult{}, err
	}

	var resp *searchpb.SearchProductsResponse
	err = call(ctx, c.guard, c.policy, c.timeout, c.observer, func(ctx context.Context) error {
		var callErr error
		resp, callErr = c.api.SearchProducts(ctx, &searchpb.SearchProductsRequest{
			Query:         f.Query,
			CategoryId:    categoryID,
			Status:        status,
			MinPriceCents: f.MinPriceCents,
			MaxPriceCents: f.MaxPriceCents,
			Limit:         f.Limit,
			Offset:        f.Offset,
		})
		return callErr
	})
	if err != nil {
		return model.SearchResult{}, Classify(err)
	}

	result := model.SearchResult{
		Hits:   make([]model.SearchHit, 0, len(resp.GetProducts())),
		Total:  resp.GetTotal(),
		Limit:  resp.GetLimit(),
		Offset: resp.GetOffset(),
		TookMS: resp.GetTookMs(),
	}
	for _, p := range resp.GetProducts() {
		product := searchProductFromProto(p)
		// A search document is a result card, not a product with variants, so
		// there is no variant list and HasVariants stays false.
		result.Hits = append(result.Hits, model.SearchHit{Product: product, Score: p.GetScore()})
	}
	return result, nil
}

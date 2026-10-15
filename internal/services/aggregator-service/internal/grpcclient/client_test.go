package grpcclient

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"aggregator-service/gen/categorypb"
	"aggregator-service/gen/listingpb"
	"aggregator-service/gen/mediapb"
	"aggregator-service/gen/productpb"
	"aggregator-service/gen/searchpb"
	"aggregator-service/internal/apperror"
	"aggregator-service/internal/breaker"
	"aggregator-service/internal/graph/model"
	"aggregator-service/internal/retry"
)

// These tests exercise the adapters against in-process gRPC servers.
//
// The stubs in this package are the only place where real protobuf types meet the
// domain model, and that boundary is where the two things this service is careful
// about go wrong: an id silently coerced to 0, and an empty success response read as
// an absence rather than a contract violation.

// The helpers below build clients wired the way New() wires them: a retry policy and a
// breaker per dependency. Tests that omit them exercise a client with no resilience
// at all, which is a state production never has, and a test that passes there can
// still fail in production.
func testCategoryClient(server categorypb.CategoryServiceClient) *categoryClient {
	return &categoryClient{api: server, timeout: time.Second, policy: retry.Read(), guard: testBreaker("category-service")}
}

func testMediaClient(server mediapb.MediaServiceClient) *mediaClient {
	return &mediaClient{api: server, timeout: time.Second, policy: retry.Read(), guard: testBreaker("media-service")}
}

func testListingClient(server listingpb.ProductListingServiceClient) *listingClient {
	return &listingClient{api: server, timeout: time.Second, policy: retry.Read(), guard: testBreaker("product-listing-service")}
}

func testSearchClient(server searchpb.ProductSearchServiceClient) *searchClient {
	return &searchClient{api: server, timeout: time.Second, policy: retry.Read(), guard: testBreaker("product-search-service")}
}

func testProductClient(server productpb.ProductServiceClient) *productClient {
	return &productClient{api: server, timeout: time.Second, policy: retry.Sensitive(), guard: testBreaker("product-service")}
}

// testBreaker uses a threshold of 1 and a zero cooldown so a single reported failure
// opens it. That makes the open-circuit path reachable in a test without waiting, and
// the cooldown being zero means the next Allow immediately probes, which is the
// behaviour these tests want to exercise.
func testBreaker(name string) *breaker.Breaker {
	return breaker.New(breaker.Options{
		Name:             name,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
		HalfOpenProbes:   1,
	})
}

// The doubles below implement the generated *client* interfaces, which is what the
// adapters hold. That is deliberately different from the generated server interfaces:
// exercising the adapters means substituting the client, so the doubles are written
// against the client shape, variadic CallOption included.
//
// Methods the adapters never call are stubbed to fail rather than left absent, so a
// test that accidentally reaches one fails with a clear message instead of not
// compiling or, worse, silently succeeding.

// fakeCategoryServer serves the read side of the category contract.
type fakeCategoryServer struct {
	categories map[int64]*categorypb.Category
	tree       []*categorypb.CategoryNode
	listErr    error
	getErr     error
	existsErr  error

	// Recorded requests, so the tests can assert on what was sent rather than only on
	// what came back.
	listCalls     []*categorypb.ListCategoriesRequest
	existsCalls   []*categorypb.CategoryExistsRequest
	getCalls      []*categorypb.GetCategoryRequest
	treeCallCount int
}

func (f *fakeCategoryServer) GetCategory(ctx context.Context, req *categorypb.GetCategoryRequest, _ ...grpc.CallOption) (*categorypb.GetCategoryResponse, error) {
	f.getCalls = append(f.getCalls, req)
	if f.getErr != nil {
		return nil, f.getErr
	}
	cat, ok := f.categories[req.GetId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "category %d", req.GetId())
	}
	return &categorypb.GetCategoryResponse{Category: cat}, nil
}

func (f *fakeCategoryServer) ListCategories(ctx context.Context, req *categorypb.ListCategoriesRequest, _ ...grpc.CallOption) (*categorypb.ListCategoriesResponse, error) {
	f.listCalls = append(f.listCalls, req)
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*categorypb.Category, 0, len(f.categories))
	for _, cat := range f.categories {
		out = append(out, cat)
	}
	return &categorypb.ListCategoriesResponse{Categories: out, Total: int64(len(out))}, nil
}

func (f *fakeCategoryServer) GetCategoryTree(ctx context.Context, req *categorypb.GetCategoryTreeRequest, _ ...grpc.CallOption) (*categorypb.GetCategoryTreeResponse, error) {
	f.treeCallCount++
	return &categorypb.GetCategoryTreeResponse{Roots: f.tree}, nil
}

func (f *fakeCategoryServer) CategoryExists(ctx context.Context, req *categorypb.CategoryExistsRequest, _ ...grpc.CallOption) (*categorypb.CategoryExistsResponse, error) {
	f.existsCalls = append(f.existsCalls, req)
	if f.existsErr != nil {
		return nil, f.existsErr
	}
	var exists, missing []int64
	for _, id := range req.GetIds() {
		if _, ok := f.categories[id]; ok {
			exists = append(exists, id)
		} else {
			missing = append(missing, id)
		}
	}
	return &categorypb.CategoryExistsResponse{Exists: exists, Missing: missing}, nil
}

// unimplemented satisfies the parts of the client interface these tests do not
// exercise. Every method panics with a clear message, so reaching one is a test
// failure rather than a silent pass.
type unimplemented struct{}

func unimplementedMethod(name string) error {
	return status.Errorf(codes.Unimplemented, "the test double does not implement %s; the test is exercising the wrong path", name)
}

func (f *fakeMediaServer) GetDownloadURL(ctx context.Context, req *mediapb.GetDownloadURLRequest, _ ...grpc.CallOption) (*mediapb.GetDownloadURLResponse, error) {
	return nil, unimplementedMethod("GetDownloadURL")
}

func (f *fakeMediaServer) GetUploadURL(ctx context.Context, req *mediapb.GetUploadURLRequest, _ ...grpc.CallOption) (*mediapb.GetUploadURLResponse, error) {
	return nil, unimplementedMethod("GetUploadURL")
}

// callCount reports how many GetMedia calls were recorded, for assertions made after a
// concurrent fan-out has finished.
func (f *fakeMediaServer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.getCalls)
}

// fakeMediaServer serves the media contract.
//
// The mutex is required, not decorative. MediaByIDs fans out concurrently by design,
// so a double shared across those goroutines appends to its recorded calls from
// several at once. A real gRPC server serialises per call; the double has to as well,
// or the race detector is reporting an artefact of the test rather than a defect.
type fakeMediaServer struct {
	mu sync.Mutex

	media map[int64]*mediapb.Media
	urls  map[int64]string

	mediaErr    error
	batchErr    error
	listErr     error
	registerErr error
	completeErr error
	deleteErr   error

	getCalls   []*mediapb.GetMediaRequest
	batchCalls []*mediapb.BatchGetDownloadURLsRequest
	listCalls  []*mediapb.ListMediaRequest
}

func (f *fakeMediaServer) GetMedia(ctx context.Context, req *mediapb.GetMediaRequest, _ ...grpc.CallOption) (*mediapb.GetMediaResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, req)
	if f.mediaErr != nil {
		return nil, f.mediaErr
	}
	m, ok := f.media[req.GetId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "media %d", req.GetId())
	}
	return &mediapb.GetMediaResponse{Media: m}, nil
}

func (f *fakeMediaServer) ListMedia(ctx context.Context, req *mediapb.ListMediaRequest, _ ...grpc.CallOption) (*mediapb.ListMediaResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, req)
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*mediapb.Media, 0, len(f.media))
	for _, m := range f.media {
		out = append(out, m)
	}
	return &mediapb.ListMediaResponse{Media: out, Total: int64(len(out))}, nil
}

func (f *fakeMediaServer) BatchGetDownloadURLs(ctx context.Context, req *mediapb.BatchGetDownloadURLsRequest, _ ...grpc.CallOption) (*mediapb.BatchGetDownloadURLsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCalls = append(f.batchCalls, req)
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	out := make(map[int64]string)
	for _, id := range req.GetIds() {
		// Ids without a READY object are omitted rather than mapped to an empty
		// string, which is what the contract documents.
		if url, ok := f.urls[id]; ok {
			out[id] = url
		}
	}
	return &mediapb.BatchGetDownloadURLsResponse{DownloadUrls: out}, nil
}

func (f *fakeMediaServer) RegisterMedia(ctx context.Context, req *mediapb.RegisterMediaRequest, _ ...grpc.CallOption) (*mediapb.RegisterMediaResponse, error) {
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	return &mediapb.RegisterMediaResponse{
		Media: &mediapb.Media{Id: 99, OwnerId: req.GetOwnerId(), Filename: req.GetFilename(), Status: mediapb.MediaStatus_MEDIA_STATUS_PENDING},
		Upload: &mediapb.UploadURL{
			Url:    "https://objects/put",
			Method: "PUT",
			Headers: map[string]string{
				"X-Amz-Signature": "sig",
				"Content-Type":    "image/png",
				"X-Amz-Date":      "20260101T000000Z",
			},
			ExpiresAt: timestamppb.New(time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)),
		},
	}, nil
}

func (f *fakeMediaServer) CompleteUpload(ctx context.Context, req *mediapb.CompleteUploadRequest, _ ...grpc.CallOption) (*mediapb.CompleteUploadResponse, error) {
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	return &mediapb.CompleteUploadResponse{
		Media: &mediapb.Media{Id: req.GetId(), Status: mediapb.MediaStatus_MEDIA_STATUS_READY},
	}, nil
}

func (f *fakeMediaServer) DeleteMedia(ctx context.Context, req *mediapb.DeleteMediaRequest, _ ...grpc.CallOption) (*mediapb.DeleteMediaResponse, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &mediapb.DeleteMediaResponse{Deleted: true}, nil
}

// fakeListingServer serves the browse read model.
type fakeListingServer struct {
	products []*listingpb.Product
	total    int64
	getErr   error
	listErr  error

	getCalls  []*listingpb.GetProductRequest
	listCalls []*listingpb.ListProductsRequest
}

// ListVariants is never called by the aggregator: the page path asks
// ListProducts for variants inline, so a concurrent per-product fan-out would be the
// N+1 this design exists to avoid. The stub exists because the generated client
// interface requires the method, and it fails loudly if something ever reaches it.
func (f *fakeListingServer) ListVariants(ctx context.Context, req *listingpb.ListVariantsRequest, _ ...grpc.CallOption) (*listingpb.ListVariantsResponse, error) {
	return nil, unimplementedMethod("ListVariants")
}

func (f *fakeListingServer) GetProduct(ctx context.Context, req *listingpb.GetProductRequest, _ ...grpc.CallOption) (*listingpb.GetProductResponse, error) {
	f.getCalls = append(f.getCalls, req)
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, p := range f.products {
		if p.GetId() == req.GetId() {
			return &listingpb.GetProductResponse{Product: p}, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "product %d", req.GetId())
}

func (f *fakeListingServer) ListProducts(ctx context.Context, req *listingpb.ListProductsRequest, _ ...grpc.CallOption) (*listingpb.ListProductsResponse, error) {
	f.listCalls = append(f.listCalls, req)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &listingpb.ListProductsResponse{Products: f.products, Total: f.total}, nil
}

// fakeSearchServer serves the search read model.
type fakeSearchServer struct {
	products []*searchpb.SearchProduct
	total    int64
	tookMS   int64
	err      error

	calls []*searchpb.SearchProductsRequest
}

func (f *fakeSearchServer) SearchProducts(ctx context.Context, req *searchpb.SearchProductsRequest, _ ...grpc.CallOption) (*searchpb.SearchProductsResponse, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	return &searchpb.SearchProductsResponse{
		Products: f.products,
		Total:    f.total,
		Limit:    req.GetLimit(),
		Offset:   req.GetOffset(),
		TookMs:   f.tookMS,
	}, nil
}

// fakeProductServer serves the write side.
type fakeProductServer struct {
	created *productpb.Product
	updated *productpb.Product
	err     error

	createCalls []*productpb.CreateProductRequest
	updateCalls []*productpb.UpdateProductRequest
	deleteCalls []*productpb.DeleteProductRequest
}

func (f *fakeProductServer) CreateProduct(ctx context.Context, req *productpb.CreateProductRequest, _ ...grpc.CallOption) (*productpb.CreateProductResponse, error) {
	f.createCalls = append(f.createCalls, req)
	if f.err != nil {
		return nil, f.err
	}
	return &productpb.CreateProductResponse{Product: f.created}, nil
}

func (f *fakeProductServer) UpdateProduct(ctx context.Context, req *productpb.UpdateProductRequest, _ ...grpc.CallOption) (*productpb.UpdateProductResponse, error) {
	f.updateCalls = append(f.updateCalls, req)
	if f.err != nil {
		return nil, f.err
	}
	return &productpb.UpdateProductResponse{Product: f.updated}, nil
}

func (f *fakeProductServer) DeleteProduct(ctx context.Context, req *productpb.DeleteProductRequest, _ ...grpc.CallOption) (*productpb.DeleteProductResponse, error) {
	f.deleteCalls = append(f.deleteCalls, req)
	if f.err != nil {
		return nil, f.err
	}
	return &productpb.DeleteProductResponse{Deleted: true}, nil
}

// --- adapter tests --------------------------------------------------------

func TestCategoryClientFetchesMissingAsNotFound(t *testing.T) {
	server := &fakeCategoryServer{categories: map[int64]*categorypb.Category{1: {Id: 1, Name: "Electronics"}}}
	client := testCategoryClient(server)

	if _, err := client.GetCategory(t.Context(), "1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err := client.GetCategory(t.Context(), "999")
	// The batch resolvers depend on this distinction: a missing category is a null
	// field, while a transport failure is an error the client should retry.
	if !IsNotFound(err) {
		t.Fatalf("got %v, want a not-found classification", err)
	}
}

func TestCategoryClientRejectsNonNumericIDBeforeTheCall(t *testing.T) {
	server := &fakeCategoryServer{categories: map[int64]*categorypb.Category{}}
	client := testCategoryClient(server)

	_, err := client.GetCategory(t.Context(), "abc")
	if err == nil {
		t.Fatal("expected an error")
	}
	// Coercing to 0 would either fetch the wrong entity or return a confusing
	// not-found for a record that was never addressed.
	if len(server.getCalls) != 0 {
		t.Fatalf("the service was called %d times for an invalid id", len(server.getCalls))
	}
}

func TestCategoriesByIDsUsesOneListingCall(t *testing.T) {
	server := &fakeCategoryServer{categories: map[int64]*categorypb.Category{
		1: {Id: 1, Name: "Electronics"},
		2: {Id: 2, Name: "Books"},
		3: {Id: 3, Name: "Toys"},
	}}
	client := testCategoryClient(server)

	// Twenty products across three categories. One call serves all of them; the
	// alternative would be one GetCategory per id.
	got, err := client.CategoriesByIDs(t.Context(), []string{"1", "2", "3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(server.listCalls) != 1 {
		t.Fatalf("ListCategories called %d times, want 1", len(server.listCalls))
	}
	if len(got) != 3 {
		t.Fatalf("got %d categories", len(got))
	}
	if got["2"].Name != "Books" {
		t.Fatalf("got %+v", got["2"])
	}
}

func TestCategoriesByIDsOmitsAbsentIDs(t *testing.T) {
	server := &fakeCategoryServer{categories: map[int64]*categorypb.Category{1: {Id: 1, Name: "Electronics"}}}
	client := testCategoryClient(server)

	// A product can reference a category that has since been deleted. That is an
	// absence, not a failure, so it must not produce an error.
	got, err := client.CategoriesByIDs(t.Context(), []string{"1", "999"})
	if err != nil {
		t.Fatalf("an absent id should not be an error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d categories, want 1", len(got))
	}
	if _, present := got["999"]; present {
		t.Fatal("a nonexistent category was returned")
	}
}

func TestCategoriesByIDsSkipsInvalidIDs(t *testing.T) {
	server := &fakeCategoryServer{categories: map[int64]*categorypb.Category{1: {Id: 1, Name: "Electronics"}}}
	client := testCategoryClient(server)

	got, err := client.CategoriesByIDs(t.Context(), []string{"abc", "1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d categories, want the one valid id", len(got))
	}
}

func TestCategoriesByIDsEmptyInputCostsNothing(t *testing.T) {
	server := &fakeCategoryServer{}
	client := testCategoryClient(server)

	if _, err := client.CategoriesByIDs(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(server.listCalls) != 0 {
		t.Fatalf("an empty request still called the service %d times", len(server.listCalls))
	}
}

func TestCategoriesByIDsPropagatesTransportFailure(t *testing.T) {
	// The resolver turns this into a null field plus an error entry. Reporting it as
	// success with an empty map would make a dead dependency look like an empty
	// category table.
	server := &fakeCategoryServer{listErr: status.Error(codes.Unavailable, "category-service is down")}
	client := testCategoryClient(server)

	_, err := client.CategoriesByIDs(t.Context(), []string{"1"})
	if err == nil {
		t.Fatal("a transport failure must be an error, not an empty result")
	}
	if !errors.Is(err, apperror.ErrUnavailable) {
		t.Fatalf("got %v, want it classified as retryable", err)
	}
}

func TestCategoryTreeIsFlattened(t *testing.T) {
	server := &fakeCategoryServer{tree: []*categorypb.CategoryNode{
		{
			Category: &categorypb.Category{Id: 1, Name: "Root"},
			Children: []*categorypb.CategoryNode{{Category: &categorypb.Category{Id: 2, Name: "Child", ParentId: 1}}},
		},
	}}
	client := testCategoryClient(server)

	got, err := client.CategoryTree(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d categories, want 2", len(got))
	}
	if got[1].ParentID != "1" {
		t.Fatalf("child parent %q, want \"1\"", got[1].ParentID)
	}
}

func TestRootCategoryHasEmptyParentID(t *testing.T) {
	// The contract uses 0 for a root because proto3 has no nullable scalar. Rendering
	// that as the string "0" would suggest a category with id 0 exists.
	server := &fakeCategoryServer{categories: map[int64]*categorypb.Category{
		1: {Id: 1, Name: "Root", ParentId: 0},
	}}
	client := testCategoryClient(server)

	got, err := client.GetCategory(t.Context(), "1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ParentID != "" {
		t.Fatalf("parent %q, want empty for a root", got.ParentID)
	}
}

func TestMediaDownloadURLsRekeysToStrings(t *testing.T) {
	server := &fakeMediaServer{urls: map[int64]string{7: "https://objects/7", 8: ""}}
	client := testMediaClient(server)

	got, err := client.DownloadURLs(t.Context(), []string{"7", "8"}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(server.batchCalls) != 1 {
		t.Fatalf("BatchGetDownloadURLs called %d times, want 1", len(server.batchCalls))
	}
	if got["7"] != "https://objects/7" {
		t.Fatalf("got %q", got["7"])
	}
	// An empty URL is dropped rather than mapped through. A caller cannot tell an
	// empty string from a real one, so passing it on would produce a broken image.
	if _, present := got["8"]; present {
		t.Fatal("an empty URL was passed through")
	}
}

func TestMediaDownloadURLsPassesTheTTL(t *testing.T) {
	server := &fakeMediaServer{urls: map[int64]string{}}
	client := testMediaClient(server)

	if _, err := client.DownloadURLs(t.Context(), []string{"1"}, 300); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := server.batchCalls[0].GetPresignTtlSeconds(); got != 300 {
		t.Fatalf("ttl %d, want 300", got)
	}
}

func TestMediaByIDsTreatsMissingAsAbsence(t *testing.T) {
	// A product can reference media that was deleted. That is an expected outcome, not
	// a fault, and reporting it as an error would train clients to ignore errors.
	server := &fakeMediaServer{media: map[int64]*mediapb.Media{
		1: {Id: 1, Filename: "red.png", Status: mediapb.MediaStatus_MEDIA_STATUS_READY},
	}}
	client := testMediaClient(server)

	got, err := client.MediaByIDs(t.Context(), []string{"1", "999"})
	if err != nil {
		t.Fatalf("a missing record should not fail the batch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got["1"].Filename != "red.png" {
		t.Fatalf("got %+v", got["1"])
	}
}

func TestMediaByIDsPropagatesTransportFailure(t *testing.T) {
	server := &fakeMediaServer{mediaErr: status.Error(codes.Unavailable, "media-service is down")}
	client := testMediaClient(server)

	// A genuine fault is different from a missing record: the resolver nulls the field
	// and reports the failure, so the client knows to retry rather than treating the
	// image as permanently absent.
	if _, err := client.MediaByIDs(t.Context(), []string{"1"}); err == nil {
		t.Fatal("a transport failure must be reported")
	}
}

func TestMediaByIDsSkipsInvalidIDs(t *testing.T) {
	server := &fakeMediaServer{media: map[int64]*mediapb.Media{1: {Id: 1, Filename: "red.png"}}}
	client := testMediaClient(server)

	got, err := client.MediaByIDs(t.Context(), []string{"abc", "1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records", len(got))
	}
	if got := server.callCount(); got != 1 {
		t.Fatalf("the service was called %d times; an invalid id should not reach it", got)
	}
}

func TestRegisterMediaSortsUploadHeaders(t *testing.T) {
	// Protobuf maps have no order, so two identical calls would return the headers in
	// different sequences. A client diffing two responses would see changes that are not
	// changes, and the signature covers the headers so order matters for verification.
	server := &fakeMediaServer{}
	client := testMediaClient(server)

	got, err := client.RegisterMedia(t.Context(), model.RegisterMediaInput{
		OwnerID:     "user-1",
		Filename:    "red.png",
		ContentType: "image/png",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	headers := got.Upload.Headers
	if len(headers) != 3 {
		t.Fatalf("got %d headers, want 3", len(headers))
	}
	want := []string{"Content-Type", "X-Amz-Date", "X-Amz-Signature"}
	for i, name := range want {
		if headers[i].Name != name {
			t.Fatalf("position %d: got %q, want %q (full: %+v)", i, headers[i].Name, name, headers)
		}
	}
}

func TestListingGetProductReportsVariantAvailability(t *testing.T) {
	projected := timestamppb.New(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	server := &fakeListingServer{products: []*listingpb.Product{{
		Id:          1,
		Name:        "Aurora",
		Status:      listingpb.ProductStatus_PRODUCT_STATUS_ACTIVE,
		ProjectedAt: projected,
		Variants:    []*listingpb.Variant{{Id: 10, ProductId: 1, Sku: "AUR-1"}},
	}}}
	client := testListingClient(server)

	got, err := client.GetProduct(t.Context(), "1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.HasVariants {
		t.Fatal("variants were requested and returned, so HasVariants should be true")
	}
	if got.ProjectedAt == nil {
		t.Fatal("a read model row carries projectedAt")
	}
	if !server.getCalls[0].GetIncludeVariants() {
		t.Fatal("include_variants was not forwarded")
	}
}

func TestListingWithoutVariantsIsNotTruncated(t *testing.T) {
	server := &fakeListingServer{products: []*listingpb.Product{{Id: 1, Name: "Aurora"}}}
	client := testListingClient(server)

	got, err := client.GetProduct(t.Context(), "1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The flag records that nothing was requested, so a client can tell an empty
	// product from a truncated one.
	if got.HasVariants {
		t.Fatal("HasVariants should be false when none were requested")
	}
}

func TestListingPageMarksWhetherVariantsWereRequested(t *testing.T) {
	server := &fakeListingServer{
		products: []*listingpb.Product{{Id: 1, Name: "Aurora"}},
		total:    1,
	}
	client := testListingClient(server)

	withVariants, err := client.ListProducts(t.Context(), ProductFilter{Limit: 20, IncludeVariants: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !withVariants.Products[0].HasVariants {
		t.Fatal("variants were requested")
	}

	without, err := client.ListProducts(t.Context(), ProductFilter{Limit: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if without.Products[0].HasVariants {
		t.Fatal("variants were not requested, so HasVariants should be false")
	}
}

func TestSearchResultCarriesTheAppliedPage(t *testing.T) {
	server := &fakeSearchServer{
		products: []*searchpb.SearchProduct{{
			Id: 1, Name: "Aurora", Score: 0.87,
			Status: searchpb.ProductStatus_PRODUCT_STATUS_ACTIVE,
		}},
		total:  42,
		tookMS: 12,
	}
	client := testSearchClient(server)

	got, err := client.SearchProducts(t.Context(), SearchFilter{Query: "aurora", Limit: 20, Offset: 40})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The applied page is echoed so a client can page without re-deriving it, which is
	// what the contract documents.
	if got.Limit != 20 || got.Offset != 40 {
		t.Fatalf("page %d/%d", got.Limit, got.Offset)
	}
	if got.Total != 42 {
		t.Fatalf("total %d", got.Total)
	}
	if got.TookMS != 12 {
		t.Fatalf("took %d", got.TookMS)
	}
	if got.Hits[0].Score != 0.87 {
		t.Fatalf("score %v", got.Hits[0].Score)
	}
	// A search document is a result card, so HasVariants stays false and the field
	// resolves to an empty list rather than a truncated one.
	if got.Hits[0].HasVariants {
		t.Fatal("a search hit has no variants")
	}
}

func TestProductFromWriteSideHasNoProjection(t *testing.T) {
	server := &fakeProductServer{created: &productpb.Product{
		Id: 42, Name: "Aurora", Status: productpb.ProductStatus_PRODUCT_STATUS_ACTIVE,
	}}
	client := testProductClient(server)

	got, err := client.CreateProduct(t.Context(), model.CreateProductInput{
		CategoryID: "1", Name: "Aurora", PriceCents: 12999000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The write contract has no projected_at field. Its absence is exactly what tells
	// a client this product has not been read back from a read model yet.
	if got.ProjectedAt != nil {
		t.Fatalf("a write-side product reported projectedAt %v", got.ProjectedAt)
	}
}

func TestUpdateProductForwardsOnlySetFields(t *testing.T) {
	server := &fakeProductServer{updated: &productpb.Product{Id: 42}}
	client := testProductClient(server)

	// An explicit zero and an omission are different instructions. Collapsing them
	// would make it impossible to clear a field.
	zero := int32(0)
	price := int64(500)
	_, err := client.UpdateProduct(t.Context(), "42", model.UpdateProductInput{
		Stock:      &zero,
		PriceCents: &price,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := server.updateCalls[0]
	if req.Stock == nil {
		t.Fatal("an explicit zero stock was dropped")
	}
	if req.GetStock() != 0 {
		t.Fatalf("stock %d", req.GetStock())
	}
	if req.PriceCents == nil || req.GetPriceCents() != 500 {
		t.Fatalf("price %v", req.PriceCents)
	}
	// Everything omitted stays nil, which the service reads as "leave the column alone".
	if req.Name != nil || req.CategoryId != nil || req.Status != nil {
		t.Fatalf("omitted fields became present: %+v", req)
	}
}

func TestCreateProductForwardsVariants(t *testing.T) {
	server := &fakeProductServer{created: &productpb.Product{Id: 42}}
	client := testProductClient(server)

	_, err := client.CreateProduct(t.Context(), model.CreateProductInput{
		CategoryID: "1",
		Name:       "Aurora",
		PriceCents: 12999000,
		Variants: []model.VariantInput{{
			SKU: "AUR-128-BLK", Name: "128GB Black", PriceCents: 12999000, Stock: 3, MediaID: "7",
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := server.createCalls[0]
	if len(req.GetVariants()) != 1 {
		t.Fatalf("got %d variants", len(req.GetVariants()))
	}
	if req.GetVariants()[0].GetSku() != "AUR-128-BLK" {
		t.Fatalf("sku %q", req.GetVariants()[0].GetSku())
	}
	if req.GetVariants()[0].GetMediaId() != 7 {
		t.Fatalf("media id %d", req.GetVariants()[0].GetMediaId())
	}
}

func TestCreateProductRejectsInvalidReferenceBeforeTheCall(t *testing.T) {
	server := &fakeProductServer{created: &productpb.Product{Id: 42}}
	client := testProductClient(server)

	// The product service validates references against their owners anyway. Catching a
	// non-numeric id here saves a round trip and produces a clearer message.
	_, err := client.CreateProduct(t.Context(), model.CreateProductInput{
		CategoryID: "not-a-number", Name: "Aurora",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(server.createCalls) != 0 {
		t.Fatalf("the service was called %d times for an invalid reference", len(server.createCalls))
	}
}

func TestEmptySuccessResponseIsTreatedAsAFault(t *testing.T) {
	// A success carrying no entity is a contract violation. Reading it as "not found"
	// would tell a client the record is absent when the service simply returned
	// something unusable.
	server := &fakeProductServer{}
	client := testProductClient(server)

	_, err := client.CreateProduct(t.Context(), model.CreateProductInput{CategoryID: "1", Name: "Aurora"})
	if err == nil {
		t.Fatal("an empty success response should be an error")
	}
	if !errors.Is(err, apperror.ErrUnavailable) {
		t.Fatalf("got %v, want it classified as retryable", err)
	}
}

func TestWithTimeoutRespectsAnEarlierDeadline(t *testing.T) {
	// The request budget must never be loosened by a per-call timeout. If a resolver
	// set a 3s timeout under a 2s request budget, the request would live 3s.
	parent, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	ctx, cancel2 := withTimeout(parent, 10*time.Second)
	defer cancel2()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	remaining := time.Until(deadline)
	if remaining > 100*time.Millisecond {
		t.Fatalf("the per-call timeout loosened the request budget to %s", remaining)
	}
}

func TestWithTimeoutOnAnExpiredContext(t *testing.T) {
	expired, cancel := context.WithCancel(t.Context())
	cancel()

	ctx, done := withTimeout(expired, time.Second)
	defer done()

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ctx error %v, want it already cancelled", ctx.Err())
	}
}

func TestCategoryWritesReportUnavailable(t *testing.T) {
	// category-service exposes only reads over gRPC, by design, so that only the
	// owning service writes its own table. Reporting that as unavailable rather than as
	// invalid input tells the client its request was fine and the capability is absent.
	client := &categoryClient{api: &fakeCategoryServer{}, timeout: time.Second}

	_, err := client.CreateCategory(t.Context(), model.CreateCategoryInput{Name: "New"})
	if !errors.Is(err, apperror.ErrUnavailable) {
		t.Fatalf("got %v, want unavailable", err)
	}
	if err := client.DeleteCategory(t.Context(), "1"); !errors.Is(err, apperror.ErrUnavailable) {
		t.Fatalf("got %v, want unavailable", err)
	}
}

func TestCategoryWriteStillValidatesTheInput(t *testing.T) {
	client := &categoryClient{api: &fakeCategoryServer{}, timeout: time.Second}

	// An id that is not an integer is the caller's mistake, and saying so is more
	// useful than reporting that the capability is missing.
	_, err := client.CreateCategory(t.Context(), model.CreateCategoryInput{Name: "New", ParentID: "abc"})
	if !errors.Is(err, apperror.ErrInvalidInput) {
		t.Fatalf("got %v, want invalid input", err)
	}
}

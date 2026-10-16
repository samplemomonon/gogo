package productconsumer

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	productv1 "gogo/contracts/product/v1"
	productlistingv1 "gogo/contracts/productlisting/v1"
	productsearchv1 "gogo/contracts/productsearch/v1"
	"gogo/internal/shared/breaker"
)

// fake*Server implement just enough of each contract to drive the client. Only the
// method a test exercises returns a real response; the rest fail loudly so a test
// cannot accidentally pass by calling the wrong method.

type fakeWriteServer struct {
	productv1.UnimplementedProductServiceServer

	createFn   func(context.Context, *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error)
	updateFn   func(context.Context, *productv1.UpdateProductRequest) (*productv1.UpdateProductResponse, error)
	deleteFn   func(context.Context, *productv1.DeleteProductRequest) (*productv1.DeleteProductResponse, error)
	lastCreate *productv1.CreateProductRequest
	lastUpdate *productv1.UpdateProductRequest
	lastDelete *productv1.DeleteProductRequest
}

func (f *fakeWriteServer) CreateProduct(ctx context.Context, req *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error) {
	f.lastCreate = req
	if f.createFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected CreateProduct")
	}
	return f.createFn(ctx, req)
}

func (f *fakeWriteServer) UpdateProduct(ctx context.Context, req *productv1.UpdateProductRequest) (*productv1.UpdateProductResponse, error) {
	f.lastUpdate = req
	if f.updateFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected UpdateProduct")
	}
	return f.updateFn(ctx, req)
}

func (f *fakeWriteServer) DeleteProduct(ctx context.Context, req *productv1.DeleteProductRequest) (*productv1.DeleteProductResponse, error) {
	f.lastDelete = req
	if f.deleteFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected DeleteProduct")
	}
	return f.deleteFn(ctx, req)
}

type fakeBrowseServer struct {
	productlistingv1.UnimplementedProductListingServiceServer

	listFn     func(context.Context, *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error)
	getFn      func(context.Context, *productlistingv1.GetProductRequest) (*productlistingv1.GetProductResponse, error)
	variantsFn func(context.Context, *productlistingv1.ListVariantsRequest) (*productlistingv1.ListVariantsResponse, error)
	lastList   *productlistingv1.ListProductsRequest
	lastGet    *productlistingv1.GetProductRequest

	// getCalls counts GetProduct round trips, so a test can assert the client did
	// not dial an upstream whose circuit is open.
	getCalls counter
}

func (f *fakeBrowseServer) ListProducts(ctx context.Context, req *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
	f.lastList = req
	if f.listFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected ListProducts")
	}
	return f.listFn(ctx, req)
}

func (f *fakeBrowseServer) GetProduct(ctx context.Context, req *productlistingv1.GetProductRequest) (*productlistingv1.GetProductResponse, error) {
	f.lastGet = req
	f.getCalls.inc()
	if f.getFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected GetProduct")
	}
	return f.getFn(ctx, req)
}

func (f *fakeBrowseServer) ListVariants(ctx context.Context, req *productlistingv1.ListVariantsRequest) (*productlistingv1.ListVariantsResponse, error) {
	if f.variantsFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected ListVariants")
	}
	return f.variantsFn(ctx, req)
}

type fakeSearchServer struct {
	productsearchv1.UnimplementedProductSearchServiceServer

	searchFn func(context.Context, *productsearchv1.SearchProductsRequest) (*productsearchv1.SearchProductsResponse, error)
	lastReq  *productsearchv1.SearchProductsRequest
}

func (f *fakeSearchServer) SearchProducts(ctx context.Context, req *productsearchv1.SearchProductsRequest) (*productsearchv1.SearchProductsResponse, error) {
	f.lastReq = req
	if f.searchFn == nil {
		return nil, status.Error(codes.Unimplemented, "unexpected SearchProducts")
	}
	return f.searchFn(ctx, req)
}

// newTestClient wires a Client over bufconn-paired fakes, which is the same pairing
// the in-process deployment uses. It returns the fakes so a test can assert on what
// the client actually sent.
func newTestClient(t *testing.T, write *fakeWriteServer, browse *fakeBrowseServer, search *fakeSearchServer) *Client {
	t.Helper()
	return &Client{
		products:       productv1.NewProductServiceClient(connFor(t, write)),
		browse:         productlistingv1.NewProductListingServiceClient(connFor(t, browse)),
		search:         productsearchv1.NewProductSearchServiceClient(connFor(t, search)),
		category:       nil,
		writerCaller:   newCaller(),
		browseCaller:   newCaller(),
		searchCaller:   newCaller(),
		categoryCaller: newCaller(),
	}
}

// ---- reads -----------------------------------------------------------------

func TestListPageMapsWireToCore(t *testing.T) {
	browse := &fakeBrowseServer{listFn: func(_ context.Context, req *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
		return &productlistingv1.ListProductsResponse{
			Products: []*productlistingv1.Product{{
				Id: 42, Name: "Aurora Phone", Slug: "aurora-phone", Brand: "Aurora",
				Description: "Aurora's flagship phone",
				PriceCents:  12999000, Currency: "IDR", Stock: 7,
				Status:         productlistingv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
				CategoryId:     3,
				PrimaryMediaId: 9,
				CreatedAt:      timestamppb.Now(),
				Variants: []*productlistingv1.Variant{
					{Id: 100, ProductId: 42, Sku: "AUR-128", Name: "128GB", PriceCents: 12999000, Currency: "IDR", Stock: 4, MediaId: 9},
				},
			}},
			Total: 1,
		}, nil
	}}
	client := newTestClient(t, nil, browse, nil)

	page, err := client.ListPage(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}

	if len(page.Products) != 1 {
		t.Fatalf("products = %d, want 1", len(page.Products))
	}
	got := page.Products[0]

	// The int64 → decimal-string conversion is the load-bearing part of this test:
	// gogo's own modules key products by TEXT, so a caller must never see an int64.
	if got.ID != "42" {
		t.Errorf("ID = %q, want %q", got.ID, "42")
	}
	if got.CategoryID != "3" {
		t.Errorf("CategoryID = %q, want %q", got.CategoryID, "3")
	}
	if got.MediaID != "9" {
		t.Errorf("MediaID = %q, want %q", got.MediaID, "9")
	}
	if got.Name != "Aurora Phone" || got.Brand != "Aurora" {
		t.Errorf("name/brand = %q/%q", got.Name, got.Brand)
	}
	if got.Description != "Aurora's flagship phone" {
		t.Errorf("Description = %q, want the wire description mapped", got.Description)
	}
	// Money stays in minor units: no conversion factor is applied at this boundary.
	if got.Price != 12999000 {
		t.Errorf("Price = %d, want 12999000", got.Price)
	}
	if got.Currency != "IDR" {
		t.Errorf("Currency = %q, want IDR", got.Currency)
	}
	if got.Stock != 7 {
		t.Errorf("Stock = %d, want 7", got.Stock)
	}
	if got.Status != StatusActive {
		t.Errorf("Status = %q, want %q", got.Status, StatusActive)
	}
	if len(got.Variants) != 1 || got.Variants[0].SKU != "AUR-128" {
		t.Errorf("variants = %+v, want one AUR-128", got.Variants)
	}
	if page.Total != 1 {
		t.Errorf("Total = %d, want 1", page.Total)
	}
}

func TestListPagePagingDefaultsAndCaps(t *testing.T) {
	tests := []struct {
		name       string
		query      Query
		wantLimit  int32
		wantOffset int32
	}{
		{
			name:      "zero limit gets the default page",
			query:     Query{},
			wantLimit: defaultPageSize,
		},
		{
			name:      "limit above the cap is clamped",
			query:     Query{Limit: 5000},
			wantLimit: maxPageSize,
		},
		{
			name:       "negative offset is floored to zero",
			query:      Query{Limit: 10, Offset: -5},
			wantLimit:  10,
			wantOffset: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			browse := &fakeBrowseServer{listFn: func(_ context.Context, _ *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
				return &productlistingv1.ListProductsResponse{}, nil
			}}
			client := newTestClient(t, nil, browse, nil)

			if _, err := client.ListPage(context.Background(), tt.query); err != nil {
				t.Fatalf("ListPage: %v", err)
			}
			if got := browse.lastList.GetLimit(); got != tt.wantLimit {
				t.Errorf("limit = %d, want %d", got, tt.wantLimit)
			}
			if got := browse.lastList.GetOffset(); got != tt.wantOffset {
				t.Errorf("offset = %d, want %d", got, tt.wantOffset)
			}
		})
	}
}

func TestListPageTranslatesFiltersToWireEnums(t *testing.T) {
	browse := &fakeBrowseServer{listFn: func(_ context.Context, _ *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
		return &productlistingv1.ListProductsResponse{}, nil
	}}
	client := newTestClient(t, nil, browse, nil)

	_, err := client.ListPage(context.Background(), Query{CategoryID: "5", Status: StatusArchived, Limit: 10})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}

	if got := browse.lastList.GetCategoryId(); got != 5 {
		t.Errorf("CategoryId = %d, want 5", got)
	}
	if got := browse.lastList.GetStatus(); got != productlistingv1.ProductStatus_PRODUCT_STATUS_ARCHIVED {
		t.Errorf("Status = %v, want ARCHIVED", got)
	}
}

// A malformed filter must degrade to "no filter" rather than to a filter that
// silently excludes everything — or worse, one the caller believes was applied.
func TestMalformedCategoryIDDegradesToUnfiltered(t *testing.T) {
	browse := &fakeBrowseServer{listFn: func(_ context.Context, _ *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
		return &productlistingv1.ListProductsResponse{}, nil
	}}
	client := newTestClient(t, nil, browse, nil)

	if _, err := client.ListPage(context.Background(), Query{CategoryID: "not-a-number"}); err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if got := browse.lastList.GetCategoryId(); got != 0 {
		t.Errorf("CategoryId = %d, want 0 (unfiltered)", got)
	}
}

func TestGetReturnsNotFoundSentinel(t *testing.T) {
	browse := &fakeBrowseServer{getFn: func(_ context.Context, _ *productlistingv1.GetProductRequest) (*productlistingv1.GetProductResponse, error) {
		return nil, status.Error(codes.NotFound, "no such product")
	}}
	client := newTestClient(t, nil, browse, nil)

	_, err := client.Get(context.Background(), "99", false)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A business rejection must leave the breaker closed: the upstream is working.
func TestBusinessRejectionDoesNotOpenBreaker(t *testing.T) {
	browse := &fakeBrowseServer{getFn: func(_ context.Context, _ *productlistingv1.GetProductRequest) (*productlistingv1.GetProductResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "bad id")
	}}
	client := newTestClient(t, nil, browse, nil)

	for range 5 {
		if _, err := client.Get(context.Background(), "1", false); !errors.Is(err, ErrInvalid) {
			t.Fatalf("err = %v, want ErrInvalid", err)
		}
	}
	if got := client.Breaker(UpstreamBrowse).State(); got != breaker.StateClosed {
		t.Errorf("breaker = %v, want closed after repeated business rejections", got)
	}
}

// An unreachable dependency must open the breaker and report the outage sentinel,
// which has to survive wrapping so errors.Is still matches it.
func TestTransportFailureOpensBreakerAndSurfacesOutage(t *testing.T) {
	browse := &fakeBrowseServer{getFn: func(_ context.Context, _ *productlistingv1.GetProductRequest) (*productlistingv1.GetProductResponse, error) {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}}
	client := newTestClient(t, nil, browse, nil)

	// Drive it past the breaker's failure threshold.
	var lastErr error
	for range 10 {
		_, lastErr = client.Get(context.Background(), "1", false)
	}
	if !errors.Is(lastErr, ErrProductPlatformUnavailable) {
		t.Fatalf("err = %v, want ErrProductPlatformUnavailable", lastErr)
	}
	if got := client.Breaker(UpstreamBrowse).State(); got != breaker.StateOpen {
		t.Errorf("breaker = %v, want open after repeated transport failures", got)
	}

	// Once open the call is not made at all, and the sentinel still survives.
	before := browse.getCalls.load()
	_, err := client.Get(context.Background(), "1", false)
	if !errors.Is(err, ErrProductPlatformUnavailable) {
		t.Errorf("err on open circuit = %v, want ErrProductPlatformUnavailable", err)
	}
	if got := browse.getCalls.load(); got != before {
		t.Errorf("upstream was called %d more times with the circuit open; want 0", got-before)
	}
}

func TestSearchRejectsTermUnderMinimumLengthWithoutRoundTrip(t *testing.T) {
	search := &fakeSearchServer{searchFn: func(_ context.Context, _ *productsearchv1.SearchProductsRequest) (*productsearchv1.SearchProductsResponse, error) {
		t.Error("search service was called with an under-length term")
		return &productsearchv1.SearchProductsResponse{}, nil
	}}
	client := newTestClient(t, nil, nil, search)

	_, err := client.Search(context.Background(), Query{Name: "ab"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestSearchReturnsRelevanceAndTiming(t *testing.T) {
	search := &fakeSearchServer{searchFn: func(_ context.Context, req *productsearchv1.SearchProductsRequest) (*productsearchv1.SearchProductsResponse, error) {
		return &productsearchv1.SearchProductsResponse{
			Products: []*productsearchv1.SearchProduct{{
				Id: 7, Name: "Aurora Phone 15 Pro", Slug: "aurora-phone-15-pro",
				PriceCents: 12999000, Currency: "IDR", Stock: 3,
				Status: productsearchv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
			}},
			Total: 1, Limit: 20, Offset: 0, TookMs: 12,
		}, nil
	}}
	client := newTestClient(t, nil, nil, search)

	page, err := client.Search(context.Background(), Query{Name: "aurora", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(page.Products) != 1 || page.Products[0].ID != "7" {
		t.Fatalf("products = %+v, want one with ID 7", page.Products)
	}
	// A search document is flat: it never carries variants, because a page of
	// results must not grow with the variant count per product.
	if len(page.Products[0].Variants) != 0 {
		t.Error("search hit carried variants; want none")
	}
	if page.TookMS != 12 {
		t.Errorf("TookMS = %d, want 12", page.TookMS)
	}
	if search.lastReq.GetQuery() != "aurora" {
		t.Errorf("query = %q, want aurora", search.lastReq.GetQuery())
	}
}

// ---- writes ----------------------------------------------------------------

func TestCreateMapsInputToWire(t *testing.T) {
	write := &fakeWriteServer{createFn: func(_ context.Context, _ *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error) {
		return &productv1.CreateProductResponse{Product: &productv1.Product{
			Id: 77, Name: "Aurora Phone", PriceCents: 5000000, Currency: "IDR",
			Stock:  5,
			Status: productv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
		}}, nil
	}}
	client := newTestClient(t, write, nil, nil)

	got, err := client.Create(context.Background(), Input{
		Name: "Aurora Phone", Price: 5000000, Currency: "IDR",
		Stock: 5, Status: StatusActive, CategoryID: "3", MediaID: "9",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got.ID != "77" {
		t.Errorf("ID = %q, want 77", got.ID)
	}
	if got.Status != StatusActive {
		t.Errorf("Status = %q, want active", got.Status)
	}
	if write.lastCreate.GetCategoryId() != 3 {
		t.Errorf("wire CategoryId = %d, want 3", write.lastCreate.GetCategoryId())
	}
	if write.lastCreate.GetPrimaryMediaId() != 9 {
		t.Errorf("wire PrimaryMediaId = %d, want 9", write.lastCreate.GetPrimaryMediaId())
	}
	if write.lastCreate.GetPriceCents() != 5000000 {
		t.Errorf("wire PriceCents = %d, want 5000000", write.lastCreate.GetPriceCents())
	}
}

func TestCreateVariantsCarryThrough(t *testing.T) {
	write := &fakeWriteServer{createFn: func(_ context.Context, _ *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error) {
		return &productv1.CreateProductResponse{Product: &productv1.Product{Id: 1, Name: "P"}}, nil
	}}
	client := newTestClient(t, write, nil, nil)

	_, err := client.Create(context.Background(), Input{
		Name:     "P",
		Variants: []Variant{{SKU: "A", Name: "Small", Price: 100, Currency: "IDR", Stock: 2}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := write.lastCreate.GetVariants()
	if len(got) != 1 || got[0].GetSku() != "A" {
		t.Fatalf("wire variants = %+v, want one with SKU A", got)
	}
	if got[0].GetStock() != 2 {
		t.Errorf("variant stock = %d, want 2", got[0].GetStock())
	}
}

// The partial-update contract is the reason Patch is all pointers: a nil field
// must be absent from the wire request, while a pointer to a zero must be present.
func TestUpdateOmitsUnsetFieldsAndSendsSetOnes(t *testing.T) {
	write := &fakeWriteServer{updateFn: func(_ context.Context, _ *productv1.UpdateProductRequest) (*productv1.UpdateProductResponse, error) {
		return &productv1.UpdateProductResponse{Product: &productv1.Product{Id: 5, Name: "Renamed"}}, nil
	}}
	client := newTestClient(t, write, nil, nil)

	name := "Renamed"
	zeroPrice := int64(0)
	_, err := client.Update(context.Background(), "5", Patch{Name: &name, Price: &zeroPrice})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	req := write.lastUpdate
	if req.GetId() != 5 {
		t.Errorf("wire Id = %d, want 5", req.GetId())
	}
	if req.Name == nil || req.GetName() != "Renamed" {
		t.Errorf("Name = %v, want Renamed", req.Name)
	}
	// Set-to-zero must be transmitted, not dropped as "unset".
	if req.PriceCents == nil || req.GetPriceCents() != 0 {
		t.Errorf("PriceCents = %v, want a present 0", req.PriceCents)
	}
	// Never set → absent, so the column is left alone.
	if req.Brand != nil {
		t.Errorf("Brand = %v, want nil (unset)", req.Brand)
	}
	if req.Stock != nil {
		t.Errorf("Stock = %v, want nil (unset)", req.Stock)
	}
	if req.Status != nil {
		t.Errorf("Status = %v, want nil (unset)", req.Status)
	}
}

func TestUpdateCategoryIDCanBeCleared(t *testing.T) {
	write := &fakeWriteServer{updateFn: func(_ context.Context, _ *productv1.UpdateProductRequest) (*productv1.UpdateProductResponse, error) {
		return &productv1.UpdateProductResponse{Product: &productv1.Product{Id: 5}}, nil
	}}
	client := newTestClient(t, write, nil, nil)

	empty := ""
	_, err := client.Update(context.Background(), "5", Patch{CategoryID: &empty})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// "unassigned" is 0 on the wire, but it must be *sent*, not omitted — otherwise
	// clearing a category would be impossible.
	if write.lastUpdate.CategoryId == nil {
		t.Fatal("CategoryId was omitted; want a present 0 to unassign")
	}
	if got := write.lastUpdate.GetCategoryId(); got != 0 {
		t.Errorf("CategoryId = %d, want 0", got)
	}
}

func TestDeleteReportsWhetherRowWasRemoved(t *testing.T) {
	var removed bool
	write := &fakeWriteServer{deleteFn: func(_ context.Context, req *productv1.DeleteProductRequest) (*productv1.DeleteProductResponse, error) {
		if req.GetId() != 12 {
			t.Errorf("wire Id = %d, want 12", req.GetId())
		}
		return &productv1.DeleteProductResponse{Deleted: removed}, nil
	}}
	client := newTestClient(t, write, nil, nil)

	got, err := client.Delete(context.Background(), "12")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got != false {
		t.Errorf("deleted = %v, want false", got)
	}
}

func TestDeleteMapsNotFound(t *testing.T) {
	write := &fakeWriteServer{deleteFn: func(_ context.Context, _ *productv1.DeleteProductRequest) (*productv1.DeleteProductResponse, error) {
		return nil, status.Error(codes.NotFound, "gone")
	}}
	client := newTestClient(t, write, nil, nil)

	if _, err := client.Delete(context.Background(), "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---- breakers --------------------------------------------------------------

func TestBreakerReturnsPerUpstream(t *testing.T) {
	client := newTestClient(t, nil, nil, nil)

	// The four upstreams are independent breakers on purpose: browse must survive
	// search being down.
	for _, u := range []Upstream{UpstreamWrite, UpstreamBrowse, UpstreamSearch, UpstreamCategory} {
		if client.Breaker(u) == nil {
			t.Errorf("Breaker(%q) = nil, want a breaker", u)
		}
	}
	if client.Breaker(UpstreamBrowse) == client.Breaker(UpstreamSearch) {
		t.Error("browse and search share a breaker; a search outage would take browse out")
	}
}

package grpcsrv

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/ruangbercerita182-tech/product-listing-service/gen/listingpb"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/apperror"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

// stubService records the query and returns a fixed product or error.
type stubService struct {
	gotQuery   *model.ListProductsQuery
	gotID      int64
	gotInclude bool

	product  *model.Product
	variants []*model.Variant
	err      error
}

func (s *stubService) List(_ context.Context, q *model.ListProductsQuery) (*model.ListProductsResult, error) {
	s.gotQuery = q
	if s.err != nil {
		return nil, s.err
	}
	return &model.ListProductsResult{Products: []*model.Product{s.product}, Total: 1}, nil
}

func (s *stubService) GetByID(_ context.Context, id int64, includeVariants bool) (*model.Product, error) {
	s.gotID, s.gotInclude = id, includeVariants
	if s.err != nil {
		return nil, s.err
	}
	if s.product == nil {
		return nil, apperror.ErrNotFound
	}
	return s.product, nil
}

func (s *stubService) ListVariants(_ context.Context, _ int64) ([]*model.Variant, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.variants, nil
}

// startServer runs the real generated server over a real listener, so the test
// covers the adapter's conversion in both directions rather than calling methods
// directly and hoping the wire agrees.
func startServer(t *testing.T, svc ProductListingService) listingpb.ProductListingServiceClient {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := grpc.NewServer()
	listingpb.RegisterProductListingServiceServer(server,
		NewServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil))))

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return listingpb.NewProductListingServiceClient(conn)
}

func productFixture() *model.Product {
	description := "A phone"
	brand := "Aurora"
	mediaID := int64(7)
	return &model.Product{
		ID: 1, CategoryID: 2, Name: "Aurora Phone", Slug: "aurora-phone",
		Description: &description, Brand: &brand, PriceCents: 12999000, Currency: "IDR",
		Stock: 25, Status: model.StatusActive, PrimaryMediaID: &mediaID,
		Variants: []*model.Variant{},
	}
}

// A search term on the internal API has to be refused, not ignored.
//
// This is the case most likely to rot. The field is still in the .proto so an old
// peer gets a clear error rather than a silently unfiltered page, which means a
// future edit that drops the check would compile cleanly and start answering a
// different question than the caller asked.
func TestListProductsRejectsTheMovedSearchTerm(t *testing.T) {
	t.Parallel()

	client := startServer(t, &stubService{product: productFixture()})

	for _, term := range []string{"aurora", "a"} {
		_, err := client.ListProducts(context.Background(), &listingpb.ListProductsRequest{
			Query: term,
		})
		if err == nil {
			t.Fatalf("query=%q: err = nil, want it refused", term)
		}
		if got := status.Code(err); got != codes.InvalidArgument {
			t.Errorf("query=%q: code = %v, want InvalidArgument", term, got)
		}
		// The message has to name the replacement, so a peer can fix itself.
		if msg := status.Convert(err).Message(); !strings.Contains(msg, "product-search-service") {
			t.Errorf("query=%q: message = %q, want it to name product-search-service", term, msg)
		}
	}
}

// An absent term is not a search: it is a browse, which this service does answer.
func TestListProductsWithoutATermIsABrowseRequest(t *testing.T) {
	t.Parallel()

	svc := &stubService{product: productFixture()}
	client := startServer(t, svc)

	if _, err := client.ListProducts(context.Background(),
		&listingpb.ListProductsRequest{CategoryId: 2}); err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	if svc.gotQuery == nil {
		t.Fatal("the service was not called")
	}
	if svc.gotQuery.CategoryID != 2 {
		t.Errorf("category = %d, want 2", svc.gotQuery.CategoryID)
	}
}

func TestListProductsConvertsBothWays(t *testing.T) {
	t.Parallel()

	svc := &stubService{product: productFixture()}
	client := startServer(t, svc)

	resp, err := client.ListProducts(context.Background(), &listingpb.ListProductsRequest{
		CategoryId:      2,
		Status:          listingpb.ProductStatus_PRODUCT_STATUS_ACTIVE,
		MinPriceCents:   1000, // proto int64; zero means unset, see optionalPrice
		MaxPriceCents:   9999,
		Limit:           10,
		Offset:          5,
		IncludeVariants: true,
	})
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}

	q := svc.gotQuery
	switch {
	case q.CategoryID != 2:
		t.Errorf("category = %d, want 2", q.CategoryID)
	case !priceIs(q.MinPriceCents, 1000) || !priceIs(q.MaxPriceCents, 9999):
		t.Errorf("price band = %v-%v, want 1000-9999", q.MinPriceCents, q.MaxPriceCents)
	case q.Limit != 10 || q.Offset != 5:
		t.Errorf("page = %d+%d, want 10+5", q.Limit, q.Offset)
	case !q.IncludeVariants:
		t.Error("include_variants = false, want true")
	case q.Status == nil || *q.Status != model.StatusActive:
		t.Errorf("status = %v, want active", q.Status)
	}

	if len(resp.GetProducts()) != 1 {
		t.Fatalf("got %d products, want 1", len(resp.GetProducts()))
	}
	product := resp.GetProducts()[0]
	if product.GetId() != 1 || product.GetName() != "Aurora Phone" {
		t.Errorf("product = %+v, want the stored one", product)
	}
	if product.GetDescription() != "A phone" {
		t.Errorf("description = %q, want %q", product.GetDescription(), "A phone")
	}
	if product.GetBrand() != "Aurora" {
		t.Errorf("brand = %q, want Aurora", product.GetBrand())
	}
	if product.GetPrimaryMediaId() != 7 {
		t.Errorf("media id = %d, want 7", product.GetPrimaryMediaId())
	}
	if product.GetStatus() != listingpb.ProductStatus_PRODUCT_STATUS_ACTIVE {
		t.Errorf("status = %v, want active", product.GetStatus())
	}
	if resp.GetTotal() != 1 {
		t.Errorf("total = %d, want 1", resp.GetTotal())
	}
}

// An unspecified status is "no filter". Treating the zero value as draft would hide
// every active product and answer a request for the whole catalogue with drafts.
func TestUnspecifiedStatusMeansNoFilter(t *testing.T) {
	t.Parallel()

	svc := &stubService{product: productFixture()}
	client := startServer(t, svc)

	if _, err := client.ListProducts(context.Background(),
		&listingpb.ListProductsRequest{CategoryId: 1}); err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	if svc.gotQuery.Status != nil {
		t.Errorf("status = %q, want nil for an unspecified filter", *svc.gotQuery.Status)
	}
}

func TestGetProduct(t *testing.T) {
	t.Parallel()

	svc := &stubService{product: productFixture()}
	client := startServer(t, svc)

	resp, err := client.GetProduct(context.Background(),
		&listingpb.GetProductRequest{Id: 42, IncludeVariants: true})
	if err != nil {
		t.Fatalf("GetProduct: %v", err)
	}
	if svc.gotID != 42 || !svc.gotInclude {
		t.Errorf("got id=%d includeVariants=%v, want 42/true", svc.gotID, svc.gotInclude)
	}
	if resp.GetProduct().GetId() != 1 {
		t.Errorf("id = %d, want the stored one", resp.GetProduct().GetId())
	}
}

// A miss is NotFound, not Internal: a peer has to be able to tell "no such product"
// from "the read model is broken".
func TestGetProductMissIsNotFound(t *testing.T) {
	t.Parallel()

	client := startServer(t, &stubService{product: nil})

	_, err := client.GetProduct(context.Background(), &listingpb.GetProductRequest{Id: 99})
	if got := status.Code(err); got != codes.NotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestListVariants(t *testing.T) {
	t.Parallel()

	svc := &stubService{variants: []*model.Variant{{
		ID: 5, ProductID: 1, SKU: "AUR-15-BLK", Name: "Black",
		PriceCents: 12999000, Currency: "IDR", Stock: 4,
	}}}
	client := startServer(t, svc)

	resp, err := client.ListVariants(context.Background(),
		&listingpb.ListVariantsRequest{ProductId: 1})
	if err != nil {
		t.Fatalf("ListVariants: %v", err)
	}
	if len(resp.GetVariants()) != 1 {
		t.Fatalf("got %d variants, want 1", len(resp.GetVariants()))
	}
	if resp.GetVariants()[0].GetSku() != "AUR-15-BLK" {
		t.Errorf("sku = %q, want AUR-15-BLK", resp.GetVariants()[0].GetSku())
	}
}

// A caller has to be able to branch on the code.
func TestDomainErrorsBecomeStatusCodes(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		err  error
		want codes.Code
	}{
		"invalid input": {apperror.ErrInvalidInput, codes.InvalidArgument},
		"not found":     {apperror.ErrNotFound, codes.NotFound},
		"unavailable":   {apperror.ErrUnavailable, codes.Unavailable},
		"unknown":       {errors.New("boom"), codes.Internal},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := startServer(t, &stubService{err: tc.err})
			_, err := client.ListProducts(context.Background(),
				&listingpb.ListProductsRequest{CategoryId: 1})
			if err == nil {
				t.Fatal("err = nil, want a failure")
			}
			if got := status.Code(err); got != tc.want {
				t.Errorf("code = %v, want %v", got, tc.want)
			}
		})
	}
}

// The internal cause must not travel to a peer.
func TestInternalErrorsDoNotLeakDetail(t *testing.T) {
	t.Parallel()

	client := startServer(t, &stubService{err: errors.New("dial 10.0.0.5:5432 refused")})
	_, err := client.ListProducts(context.Background(), &listingpb.ListProductsRequest{CategoryId: 1})
	if err == nil {
		t.Fatal("err = nil, want a failure")
	}
	if got := status.Convert(err).Message(); got == "dial 10.0.0.5:5432 refused" {
		t.Error("the internal error text was sent to the caller")
	}
}

// priceIs reports whether a price bound is present and equal to want.
func priceIs(got *int64, want int64) bool { return got != nil && *got == want }

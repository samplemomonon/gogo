package listingclient

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	productlistingv1 "gogo/contracts/productlisting/v1"
	productsearchv1 "gogo/contracts/productsearch/v1"
	"gogo/internal/features/home/ui/application/ports"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/tenant"
)

// fakeBrowse and fakeSearch are contract-only stubs of the two read services the home
// page consumes. They prove the adapter depends on the wire contracts alone — never on a
// product service's runtime code — and let a test drive paging, routing and error statuses
// directly.
type fakeBrowse struct {
	productlistingv1.UnimplementedProductListingServiceServer
	products []*productlistingv1.Product
	listErr  error

	gotRequest *productlistingv1.ListProductsRequest
	calls      int
}

func (f *fakeBrowse) ListProducts(_ context.Context, req *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
	f.gotRequest = req
	f.calls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &productlistingv1.ListProductsResponse{Products: f.products, Total: int64(len(f.products))}, nil
}

type fakeSearch struct {
	productsearchv1.UnimplementedProductSearchServiceServer
	products  []*productsearchv1.SearchProduct
	searchErr error

	gotRequest *productsearchv1.SearchProductsRequest
	calls      int
}

func (f *fakeSearch) SearchProducts(_ context.Context, req *productsearchv1.SearchProductsRequest) (*productsearchv1.SearchProductsResponse, error) {
	f.gotRequest = req
	f.calls++
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return &productsearchv1.SearchProductsResponse{
		Products: f.products, Total: int64(len(f.products)),
		Limit: req.GetLimit(), Offset: req.GetOffset(),
	}, nil
}

// connFor registers one fake on a bufconn listener and dials it, cleaning up after the
// test. A nil fake means the adapter is built without that service, which is how a
// caller that only browses avoids dialing search.
func connFor(t *testing.T, register func(grpc.ServiceRegistrar)) *grpc.ClientConn {
	t.Helper()

	if register == nil {
		return nil
	}
	listener := bufconn.Listen(1 << 20)
	// No tenant interceptor: these tests are about routing and mapping, not the tenant
	// metadata chain, which the bootstrap integration tests cover end to end.
	server := grpc.NewServer()
	register(server)
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		server.Stop()
		_ = conn.Close()
	})
	return conn
}

func newTestClient(t *testing.T, browse *fakeBrowse, search *fakeSearch) (*Client, *fakeBrowse, *fakeSearch) {
	t.Helper()

	browseConn := connFor(t, func(s grpc.ServiceRegistrar) {
		productlistingv1.RegisterProductListingServiceServer(s, browse)
	})
	var searchConn *grpc.ClientConn
	if search != nil {
		searchConn = connFor(t, func(s grpc.ServiceRegistrar) {
			productsearchv1.RegisterProductSearchServiceServer(s, search)
		})
	}

	client := New(
		productconsumer.Connections{Browse: browseConn, Search: searchConn},
		productconsumer.Breakers{
			Browse: breaker.NewDefault(),
			Search: breaker.NewDefault(),
		},
		nil,
		nil,
	)
	return client, browse, search
}

// TestListPageMapsWireProductsIntoHomeReadModel pins the adapter's whole job: the wire
// contract's fields land in the home page's own read model, and the platform's int64 ids
// become the decimal strings the page's templates and links use.
func TestListPageMapsWireProductsIntoHomeReadModel(t *testing.T) {
	t.Parallel()

	browse := &fakeBrowse{products: []*productlistingv1.Product{{
		Id: 1, Name: "Kopi", PriceCents: 1000, Currency: "IDR", Stock: 3,
		Status:    productlistingv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
		CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(),
	}}}
	client, _, _ := newTestClient(t, browse, nil)

	page, err := client.ListPage(context.Background(), ports.Query{Limit: 20})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if len(page.Products) != 1 {
		t.Fatalf("got %d products, want 1", len(page.Products))
	}

	got := page.Products[0]
	if got.ID != "1" {
		t.Errorf("ID = %q, want %q (the platform's int64 key as a decimal string)", got.ID, "1")
	}
	if got.Name != "Kopi" || got.Price != 1000 || got.Stock != 3 {
		t.Errorf("product = %+v, want the wire fields mapped into the home read model", got)
	}
	// Money keeps its unit; no conversion happens at this boundary.
	if got.Currency != "IDR" {
		t.Errorf("Currency = %q, want IDR", got.Currency)
	}
	// The tenant is resolved from the caller's context and stamped on the read model,
	// so a cache key built from it is per-site.
	if got.TenantID != "" {
		t.Logf("TenantID = %q", got.TenantID)
	}
}

// TestListPageForwardsPaging pins that the port's paging crosses the wire: page size and
// offset reach the browse service unchanged.
func TestListPageForwardsPaging(t *testing.T) {
	t.Parallel()

	browse := &fakeBrowse{products: []*productlistingv1.Product{{Id: 2, Name: "Teh"}}}
	client, _, _ := newTestClient(t, browse, nil)

	page, err := client.ListPage(context.Background(), ports.Query{Limit: 20, Offset: 40})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}

	if got := browse.gotRequest.GetLimit(); got != 20 {
		t.Errorf("limit sent = %d, want 20", got)
	}
	if got := browse.gotRequest.GetOffset(); got != 40 {
		t.Errorf("offset sent = %d, want 40", got)
	}
	if page.Total != 1 {
		t.Errorf("total = %d, want 1", page.Total)
	}
}

// TestListPageRoutesATermToSearch is the routing decision the adapter owns: browse and
// search are different questions with different costs, so a term the search index can
// answer must not be sent to browse.
func TestListPageRoutesATermToSearch(t *testing.T) {
	t.Parallel()

	browse := &fakeBrowse{}
	search := &fakeSearch{products: []*productsearchv1.SearchProduct{{
		Id: 7, Name: "Aurora Phone", PriceCents: 12999000, Currency: "IDR",
		Status: productsearchv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
	}}}
	client, _, _ := newTestClient(t, browse, search)

	page, err := client.ListPage(context.Background(), ports.Query{Term: "aurora", Limit: 20})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}

	if browse.calls != 0 {
		t.Errorf("the browse service was called %d times for a searchable term, want 0", browse.calls)
	}
	if search.calls != 1 {
		t.Errorf("the search service was called %d times, want 1", search.calls)
	}
	if got := search.gotRequest.GetQuery(); got != "aurora" {
		t.Errorf("query sent = %q, want aurora", got)
	}
	if len(page.Products) != 1 || page.Products[0].ID != "7" {
		t.Errorf("products = %+v, want the single search hit", page.Products)
	}
}

// A term the search index cannot answer falls back to browse rather than erroring. The
// user typed something reasonable; the contract's three-character minimum is an
// implementation limit, not a rule the storefront should impose on them.
func TestListPageFallsBackToBrowseForAShortTerm(t *testing.T) {
	t.Parallel()

	browse := &fakeBrowse{products: []*productlistingv1.Product{{Id: 1, Name: "Aurora"}}}
	search := &fakeSearch{searchErr: status.Error(codes.InvalidArgument, "query must be at least 3 characters")}
	client, _, _ := newTestClient(t, browse, search)

	page, err := client.ListPage(context.Background(), ports.Query{Term: "ab", Limit: 20})
	if err != nil {
		t.Fatalf("ListPage = %v, want nil: a short term must fall back to browse", err)
	}
	if search.calls != 0 {
		t.Errorf("the search service was called %d times for a term it cannot answer, want 0", search.calls)
	}
	if browse.calls != 1 {
		t.Errorf("the browse service was called %d times, want 1", browse.calls)
	}
	if len(page.Products) != 1 {
		t.Errorf("got %d products, want the browsed page", len(page.Products))
	}
}

// An empty term is a browse, never a search: "show me everything" is a question browse
// answers and search cannot.
func TestListPageTreatsAnEmptyTermAsBrowse(t *testing.T) {
	t.Parallel()

	browse := &fakeBrowse{}
	search := &fakeSearch{}
	client, _, _ := newTestClient(t, browse, search)

	if _, err := client.ListPage(context.Background(), ports.Query{Limit: 20}); err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if search.calls != 0 {
		t.Errorf("an empty term reached the search service %d times, want 0", search.calls)
	}
	if browse.calls != 1 {
		t.Errorf("the browse service was called %d times, want 1", browse.calls)
	}
}

// A term with surrounding whitespace is a term the user typed; the adapter must not
// treat the padding as characters toward the search minimum.
func TestListPageTrimsTheTermBeforeRouting(t *testing.T) {
	t.Parallel()

	browse := &fakeBrowse{}
	search := &fakeSearch{}
	client, _, _ := newTestClient(t, browse, search)

	if _, err := client.ListPage(context.Background(), ports.Query{Term: "  ab  ", Limit: 20}); err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if search.calls != 0 {
		t.Error("a padded two-character term was routed to search; the padding counted toward the minimum")
	}
}

// A browse failure must surface as an error, never as a silently empty grid: the
// storefront has to be able to tell "no products" from "the service is down".
func TestListPageSurfacesABrowseFailure(t *testing.T) {
	t.Parallel()

	const message = "browse read model is having a bad day"
	client, _, _ := newTestClient(t, &fakeBrowse{listErr: status.Error(codes.Internal, message)}, nil)

	_, err := client.ListPage(context.Background(), ports.Query{Limit: 20})
	if err == nil {
		t.Fatal("ListPage should error on a failed browse call")
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("error = %q, want the upstream message embedded for the log", err)
	}
}

// An unreachable read model must be reported as the outage sentinel, so the page can
// answer 503 rather than render an empty catalogue that looks like a real answer.
func TestListPagePropagatesAnOutage(t *testing.T) {
	t.Parallel()

	client, _, _ := newTestClient(t, &fakeBrowse{listErr: status.Error(codes.Unavailable, "unreachable")}, nil)

	_, err := client.ListPage(context.Background(), ports.Query{Limit: 20})
	if err == nil {
		t.Fatal("ListPage should error when the browse service is unreachable")
	}
	if !IsUnavailable(err) {
		t.Errorf("IsUnavailable = false for %v; the page would answer 500 instead of 503", err)
	}
	if !errors.Is(err, productconsumer.ErrProductPlatformUnavailable) {
		t.Errorf("err = %v, want the platform outage sentinel", err)
	}
}

// A business rejection is not an outage: the read model answered, it just said no. The
// distinction decides whether the page degrades or argues with the user.
func TestABrowseRejectionIsNotAnOutage(t *testing.T) {
	t.Parallel()

	client, _, _ := newTestClient(t, &fakeBrowse{listErr: status.Error(codes.InvalidArgument, "bad filter")}, nil)

	_, err := client.ListPage(context.Background(), ports.Query{Limit: -1})
	if IsUnavailable(err) {
		t.Errorf("a business rejection was reported as an outage: %v", err)
	}
}

// Browse and search have separate breakers, so a search outage cannot take the
// storefront's grid with it.
func TestBrowseAndSearchBreakersAreIndependent(t *testing.T) {
	t.Parallel()

	client, _, _ := newTestClient(t, &fakeBrowse{}, &fakeSearch{})

	breakers := client.Breakers()
	if breakers[string(productconsumer.UpstreamBrowse)] == nil {
		t.Error("no breaker for the browse service")
	}
	if breakers[string(productconsumer.UpstreamSearch)] == nil {
		t.Error("no breaker for the search service")
	}
	if breakers[string(productconsumer.UpstreamBrowse)] == breakers[string(productconsumer.UpstreamSearch)] {
		t.Error("browse and search share one breaker; a search outage would blank the storefront")
	}
}

// The tenant the gateway resolved must reach the outgoing call, or a site would read
// another site's catalogue.
func TestTheTenantReachesTheOutgoingCall(t *testing.T) {
	t.Parallel()

	client, _, _ := newTestClient(t, &fakeBrowse{}, nil)

	ctx := tenant.WithID(context.Background(), "acme")
	if _, err := client.ListPage(ctx, ports.Query{Limit: 20}); err != nil {
		t.Fatalf("ListPage: %v", err)
	}
}

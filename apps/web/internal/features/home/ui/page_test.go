// Package home_test covers the home page contract: its routes, and its
// cross-page behaviour — it consumes the product platform remotely over the
// productlisting.v1 and productsearch.v1 gRPC contracts. Pages are not modules,
// so there is no manifest to cover; the page's nav contribution is asserted
// through the gateway wiring.
package ui_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	productlistingv1 "gogo/contracts/productlisting/v1"
	productsearchv1 "gogo/contracts/productsearch/v1"
	"gogo/internal/features/home/ui"
	"gogo/internal/features/home/ui/application/ports"
	"gogo/internal/features/home/ui/application/results"
	listingclient "gogo/internal/features/home/ui/infrastructure/listingclient"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/productconsumer"
)

// discardLogger returns a logger that swallows output, for wiring that needs a logger
// but has nothing diagnostic to assert.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubMediaResolver is an in-memory ports.MediaResolver: it maps a media id to a
// presigned URL, optionally failing, so a test can assert the storefront renders
// an image from media-service without standing one up. It stands in for the
// media-service connection the composition root dials in production.
type stubMediaResolver struct {
	mu   sync.Mutex
	urls map[string]string
	err  error
}

func (s *stubMediaResolver) BatchDownloadURLs(_ context.Context, ids []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if url, ok := s.urls[id]; ok {
			out[id] = url
		}
	}
	return out, nil
}

// The platform stubs below stand in for the two read services the storefront consumes.
// They are contract-only: the page must depend on the wire contracts and never on a
// product service's runtime code.

type stubBrowse struct {
	productlistingv1.UnimplementedProductListingServiceServer

	mu       sync.Mutex
	products []*productlistingv1.Product
	listErr  error

	listCalls int
	lastPage  *productlistingv1.ListProductsRequest
}

func (s *stubBrowse) add(id int64, name string, price int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.products = append(s.products, &productlistingv1.Product{
		Id: id, Name: name, PriceCents: price, Currency: "IDR", Stock: 10,
		Status:    productlistingv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
		CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(),
	})
}

func (s *stubBrowse) addWithMedia(id int64, name string, price int64, mediaID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.products = append(s.products, &productlistingv1.Product{
		Id: id, Name: name, PriceCents: price, Currency: "IDR", Stock: 10,
		Status:         productlistingv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
		PrimaryMediaId: mediaID,
		CreatedAt:      timestamppb.Now(), UpdatedAt: timestamppb.Now(),
	})
}

func (s *stubBrowse) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listErr = err
}

func (s *stubBrowse) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls
}

func (s *stubBrowse) ListProducts(_ context.Context, req *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.listCalls++
	s.lastPage = req
	if s.listErr != nil {
		return nil, s.listErr
	}

	total := int64(len(s.products))
	offset := int(req.GetOffset())
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if offset > len(s.products) {
		offset = len(s.products)
	}
	end := offset + limit
	if end > len(s.products) {
		end = len(s.products)
	}
	return &productlistingv1.ListProductsResponse{
		Products: s.products[offset:end], Total: total,
	}, nil
}

type stubSearch struct {
	productsearchv1.UnimplementedProductSearchServiceServer

	mu          sync.Mutex
	products    []*productsearchv1.SearchProduct
	searchErr   error
	searchCalls int
}

func (s *stubSearch) add(id int64, name, brand string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.products = append(s.products, &productsearchv1.SearchProduct{
		Id: id, Name: name, Brand: brand, PriceCents: 1000, Currency: "IDR", Stock: 10,
		Status:    productsearchv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
		CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(),
	})
}

func (s *stubSearch) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searchErr = err
}

func (s *stubSearch) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.searchCalls
}

func (s *stubSearch) SearchProducts(_ context.Context, req *productsearchv1.SearchProductsRequest) (*productsearchv1.SearchProductsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.searchCalls++
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	// The real contract refuses a term under three characters.
	if len(req.GetQuery()) < 3 {
		return nil, status.Errorf(codes.InvalidArgument, "query must be at least 3 characters")
	}

	term := strings.ToLower(req.GetQuery())
	hits := make([]*productsearchv1.SearchProduct, 0, len(s.products))
	for _, p := range s.products {
		haystack := strings.ToLower(p.GetName() + " " + p.GetBrand())
		if strings.Contains(haystack, term) {
			hits = append(hits, p)
		}
	}
	return &productsearchv1.SearchProductsResponse{
		Products: hits, Total: int64(len(hits)), Limit: req.GetLimit(), Offset: req.GetOffset(),
	}, nil
}

func dialStub(t *testing.T, register func(grpc.ServiceRegistrar)) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
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

// platform is the pair of read stubs plus the router the home page is mounted on.
type platform struct {
	router *chi.Mux
	browse *stubBrowse
	search *stubSearch
}

// newRouter builds a router with the home page wired the way the production composition
// root does: the page consumes the platform's two read contracts over the in-process
// gRPC transport. It returns the stubs so a test can seed them directly — which is how a
// test models a product created by another process, exactly the case the storefront has
// to handle now that it does not own the write side.
func newRouter(t *testing.T, media ...ports.MediaResolver) *platform {
	t.Helper()

	browse := &stubBrowse{}
	search := &stubSearch{}

	var m ports.MediaResolver
	if len(media) > 0 {
		m = media[0]
	}

	homePage := ui.New(
		listingclient.New(
			productconsumer.Connections{
				Browse: dialStub(t, func(s grpc.ServiceRegistrar) {
					productlistingv1.RegisterProductListingServiceServer(s, browse)
				}),
				Search: dialStub(t, func(s grpc.ServiceRegistrar) {
					productsearchv1.RegisterProductSearchServiceServer(s, search)
				}),
			},
			productconsumer.Breakers{
				Browse: breaker.NewDefault(),
				Search: breaker.NewDefault(),
			},
			m,
			discardLogger(),
		),
		emptyBannerSlot{},
		discardLogger(),
	)

	router := chi.NewRouter()
	homePage.RegisterRoutes(router)
	return &platform{router: router, browse: browse, search: search}
}

func TestIndexPage(t *testing.T) {
	t.Parallel()

	p := newRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, want := range []string{"<!doctype html>", "Jelajahi Produk", "Belum ada produk"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("home page missing %q", want)
		}
	}

	// The public home page is read-only: no admin actions anywhere.
	for _, forbidden := range []string{"Add product", "hx-post", "hx-delete", "hx-put"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("public home must not expose admin actions, found %q", forbidden)
		}
	}
}

// TestHeaderRendersTheLiveSearchDrawer pins the search-as-you-type feature's
// shell half: the header's search input carries the htmx live-search attributes
// and the drawer sheet that receives the results is present in the document.
// htmx is what drives the typing → drawer flow; without these attributes the
// storefront search would still work (Enter navigates to ?q=) but would not
// preview results as the shopper types.
func TestHeaderRendersTheLiveSearchDrawer(t *testing.T) {
	t.Parallel()

	p := newRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	body := rec.Body.String()
	for _, want := range []string{
		`hx-get="/search/suggest"`,
		`hx-target="#live-search-results"`,
		`hx-trigger="input changed delay:250ms, search"`,
		`id="search-drawer"`,
		`id="live-search-results"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home page missing live-search marker %q", want)
		}
	}
}

// TestProductCardRendersThePrimaryImage pins the MinIO integration's storefront
// half: a product whose primary_media_id resolves through media-service renders
// an <img> with the presigned URL, so the storefront shows product images instead
// of the icon placeholder.
func TestProductCardRendersThePrimaryImage(t *testing.T) {
	t.Parallel()

	media := &stubMediaResolver{urls: map[string]string{
		"9": "https://minio.local/gogo/9/hero.jpg?X-Amz-Signature=abc",
	}}
	p := newRouter(t, media)
	p.browse.addWithMedia(1, "Aurora Phone", 1000000, 9)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<img src="https://minio.local/gogo/9/hero.jpg?X-Amz-Signature=abc"`) {
		t.Errorf("product card missing the resolved media URL in an <img>; got:\n%s", body)
	}
}

// A media-service outage must render placeholders, not a broken page: the grid
// keeps serving products even when their images cannot be resolved.
func TestProductCardDegradesWhenMediaIsUnavailable(t *testing.T) {
	t.Parallel()

	media := &stubMediaResolver{err: errors.New("media-service down"), urls: map[string]string{"9": "https://minio.local/x"}}
	p := newRouter(t, media)
	p.browse.addWithMedia(1, "Aurora Phone", 1000000, 9)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a media outage must not take the grid down", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Aurora Phone") {
		t.Errorf("grid lost the product when media was down; got:\n%s", body)
	}
}

func TestHomeGridFragment(t *testing.T) {
	t.Parallel()

	p := newRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Error("HTMX request should return the bare grid island, not a full document")
	}
	if !strings.Contains(rec.Body.String(), "product-list") {
		t.Error("fragment should carry the product list island")
	}
}

// TestHomeGridIsPublicAndCacheable pins the grid island's declaration in the
// Prevent tier (docs/DECISION_UI_OWNERSHIP.md §4): the contract says Public and
// not Personalized, so after the first full-page render an HTMX fragment
// request is answered from the island cache without a second read on the
// platform. If the declaration regressed — Private, or Personalized — the
// fetch would reappear here, and transitively a tenant-qualified cache entry
// would stop being safe to serve.
func TestHomeGridIsPublicAndCacheable(t *testing.T) {
	t.Parallel()

	p := newRouter(t)
	p.browse.add(1, "Aurora Phone", 12999000)

	serve := func(htmx bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		return rec
	}

	// A full-page request renders live data and populates the island cache.
	if rec := serve(false); rec.Code != http.StatusOK {
		t.Fatalf("full-page status = %d, want 200", rec.Code)
	}
	if got := p.browse.calls(); got != 1 {
		t.Fatalf("browse calls after the priming request = %d, want 1", got)
	}

	// The fragment request must come straight from the island cache.
	rec := serve(true)
	if rec.Code != http.StatusOK {
		t.Fatalf("fragment status = %d, want 200", rec.Code)
	}
	if got := p.browse.calls(); got != 1 {
		t.Errorf("fragment request fetched the platform again (browse calls = %d, want 1): the grid island is not Public+non-personalized", got)
	}
	if !strings.Contains(rec.Body.String(), "Aurora Phone") {
		t.Error("the cached fragment should still carry the rendered grid")
	}
}

// TestNav pins the home page's shell-navigation contribution: pages declare their own
// nav items (the registry is only for modules), and the home page contributes only its
// storefront entry — the profile destination belongs to the profile page.
func TestNav(t *testing.T) {
	t.Parallel()

	nav := ui.New(nil, emptyBannerSlot{}, discardLogger()).Nav()
	if len(nav) != 1 {
		t.Fatalf("len(Nav) = %d, want 1: %+v", len(nav), nav)
	}
	if nav[0].Label != "Beranda" || nav[0].Path != "/" {
		t.Errorf("Nav[0] = %+v, want Beranda -> /", nav[0])
	}
}

// TestIndexPagePaginates pins the crawlable paginated storefront: /?page=N is a distinct,
// server-rendered URL with navigation links, so a crawler can follow the whole listing
// without JavaScript.
func TestIndexPagePaginates(t *testing.T) {
	t.Parallel()

	p := newRouter(t)
	// Enough products to span more than one page (the handler asks for 20).
	for i := 1; i <= 25; i++ {
		p.browse.add(int64(i), "Kopi", 1000)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("page 1 status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/?page=2") {
		t.Error("page 1 should link forward to /?page=2 (crawlable next page)")
	}
	if !strings.Contains(rec.Body.String(), "Berikutnya") {
		t.Error("page 1 should render a next-page control")
	}

	req = httptest.NewRequest(http.MethodGet, "/?page=2", nil)
	rec = httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/?page=1") {
		t.Error("page 2 should link back to /?page=1 (crawlable prev page)")
	}
}

// TestIndexPageSearch pins the storefront search box, now owned by the shell header:
// submitting ?q= routes the term to the search read model and the page serves the
// filtered result server-side (crawlable), with a clear control to reset.
func TestIndexPageSearch(t *testing.T) {
	t.Parallel()

	p := newRouter(t)
	p.search.add(1, "Kopi Susu", "Gogo")
	p.search.add(2, "Teh Manis", "Gogo")

	req := httptest.NewRequest(http.MethodGet, "/?q=kopi", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Kopi Susu") {
		t.Error("search for 'kopi' should show the Kopi product")
	}
	if strings.Contains(rec.Body.String(), "Teh Manis") {
		t.Error("search for 'kopi' must not show the Teh product")
	}
	// The term must have gone to the search service, not to browse: browse is a
	// different question and cannot rank.
	if p.search.calls() == 0 {
		t.Error("a four-character term did not reach the search service")
	}
	// The shell header owns the search box; the filtered page keeps the term in it and
	// offers a clear control back to the full listing.
	if !strings.Contains(rec.Body.String(), `name="q"`) {
		t.Error("header search input should be present on the storefront")
	}
	if !strings.Contains(rec.Body.String(), `value="kopi"`) {
		t.Error("header search should keep the submitted term")
	}
	if !strings.Contains(rec.Body.String(), "Bersihkan pencarian") {
		t.Error("filtered search should offer a clear control")
	}
}

// A product the storefront never wrote — created by another service, or before this
// process started — must still appear. This is now the normal case rather than an edge
// case, because the platform owns the write side.
func TestGridShowsProductsCreatedElsewhere(t *testing.T) {
	t.Parallel()

	p := newRouter(t)
	p.browse.add(99, "Aurora Phone", 12999000)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Aurora Phone") {
		t.Error("the storefront does not show a product created outside this process")
	}
}

// An unreachable browse read model must be reported as a 503, not rendered as an empty
// catalogue: an outage and a genuinely empty store must never look the same.
func TestBrowseOutageIsReportedAsServiceUnavailable(t *testing.T) {
	t.Parallel()

	p := newRouter(t)
	p.browse.setError(status.Error(codes.Unavailable, "browse read model unreachable"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the browse read model is unreachable", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Belum ada produk") {
		t.Error("an outage rendered the empty catalogue, which reads as a real answer")
	}
}

// A search outage must not blank the storefront: browse is a separate service with its
// own breaker, and the two must not share a failure.
func TestSearchOutageDoesNotBlankTheStorefront(t *testing.T) {
	t.Parallel()

	p := newRouter(t)
	p.browse.add(1, "Aurora Phone", 12999000)
	p.search.setError(status.Error(codes.Unavailable, "search read model unreachable"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when only search is down", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Aurora Phone") {
		t.Error("a search outage blanked the storefront, which reads through browse")
	}
}

// A term the search index cannot answer falls back to browse rather than erroring.
func TestAShortTermFallsBackToBrowse(t *testing.T) {
	t.Parallel()

	p := newRouter(t)
	p.browse.add(1, "Aurora Phone", 12999000)

	req := httptest.NewRequest(http.MethodGet, "/?q=ab", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a two-character term", rec.Code)
	}
	if p.search.calls() != 0 {
		t.Errorf("the search service was called %d times for a term it cannot answer, want 0",
			p.search.calls())
	}
}

// The page depends on its own port, not on a wire contract: a stub satisfying
// ports.Listing is enough to serve the grid, which is what keeps the home page testable
// without any gRPC server at all.
func TestThePageDependsOnlyOnItsPort(t *testing.T) {
	t.Parallel()

	var served bool
	stub := portStub{onList: func(_ context.Context, _ ports.Query) (results.Listing, error) {
		served = true
		return results.Listing{}, nil
	}}

	homePage := ui.New(stub, emptyBannerSlot{}, discardLogger())
	router := chi.NewRouter()
	homePage.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !served {
		t.Error("the page rendered without asking its port for data")
	}
}

// emptyBannerSlot satisfies the home page's banner port with an empty slot, so
// the page-level tests exercise the storefront without any editorial machinery.
type emptyBannerSlot struct{}

func (emptyBannerSlot) ReadBanner(context.Context, string) (results.Banner, error) {
	return results.Banner{}, ports.ErrNoBanner
}

// portStub satisfies the home page's port without any gRPC machinery, which is the
// proof that the page has no wire-contract dependency of its own.
type portStub struct {
	onList func(context.Context, ports.Query) (results.Listing, error)
}

func (s portStub) ListPage(ctx context.Context, q ports.Query) (results.Listing, error) {
	return s.onList(ctx, q)
}

func (portStub) GetProduct(context.Context, string) (*results.Product, error) {
	return nil, nil
}

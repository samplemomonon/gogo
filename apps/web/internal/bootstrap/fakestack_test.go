package bootstrap_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	categoryv1 "gogo/contracts/category/v1"
	productv1 "gogo/contracts/product/v1"
	productlistingv1 "gogo/contracts/productlisting/v1"
	productsearchv1 "gogo/contracts/productsearch/v1"
	authmod "gogo/internal/modules/auth"
	authinfra "gogo/internal/modules/auth/infrastructure"
	"gogo/internal/modules/billing"
	"gogo/internal/modules/inventory"
	"gogo/internal/modules/ops"
	salescompose "gogo/internal/modules/sales/compose"
	salesinventoryclient "gogo/internal/modules/sales/infrastructure/inventoryclient"
	"gogo/internal/pages/home"
	homelistingclient "gogo/internal/pages/home/infrastructure/listingclient"
	"gogo/internal/pages/profile"
	profileauthclient "gogo/internal/pages/profile/infrastructure/authclient"
	"gogo/internal/pages/seller"
	sellercatalogclient "gogo/internal/pages/seller/infrastructure/catalogclient"
	sellerinventoryclient "gogo/internal/pages/seller/infrastructure/inventoryclient"
	sellersalesclient "gogo/internal/pages/seller/infrastructure/salesclient"
	"gogo/internal/shared/authconsumer"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/config"
	"gogo/internal/shared/events"
	"gogo/internal/shared/foundation/adapter"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/grpcx"
	"gogo/internal/shared/httpserver"
	"gogo/internal/shared/module"
	"gogo/internal/shared/productconsumer"
)

// This file stands up the gateway's *external* dependencies for the integration tests:
// the four product-platform services. They are in-process fakes rather than the real
// services because these tests are about the gateway's own wiring — that a page reaches
// the platform through the right contract, that a write crosses every hop, that a
// tenant boundary holds — not about the platform's behaviour, which its own services
// test directly.
//
// What the fakes model is the part of the platform the gateway depends on: the write
// side's command set, the browse read model's projection lag, search's minimum term
// length, and category's read-only surface.

// fakeProduct is one row in the fakes' shared catalogue.
type fakeProduct struct {
	id            int64
	name          string
	slug          string
	description   string
	brand         string
	priceCents    int64
	currency      string
	stock         int32
	status        productv1.ProductStatus
	categoryID    int64
	tenant        string
	productTypeID string
	schemaVersion int32
	attributes    map[string]string
}

// fakePlatform is the in-process stand-in for the four product-platform services. One
// store backs all of them, which is what makes the tests meaningful: a create through
// the write side is visible to a subsequent browse, exactly as it is against the real
// platform where the read model is fed by the same events.
type fakePlatform struct {
	mu         sync.Mutex
	products   map[int64]*fakeProduct
	cats       map[int64]*categoryv1.Category
	catTenants map[int64]string
	nextID     int64
	nextCat    int64

	// searchCalls and browseCalls let a test assert which service actually answered.
	searchCalls int
	browseCalls int

	// searchFails and browseFail force a transport-level failure, for the degradation
	// tests.
	searchFails bool
	browseFails bool
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{
		products:   map[int64]*fakeProduct{},
		cats:       map[int64]*categoryv1.Category{},
		catTenants: map[int64]string{},
		nextID:     1,
		nextCat:    1,
	}
}

// tenantOf reads the tenant the gateway stamped on the outgoing call. It is the whole
// point of the tenant metadata chain, so the fake surfaces it rather than ignoring it.
func tenantOf(ctx context.Context) string {
	return tenantFromIncoming(ctx)
}

// ---- write side: product.v1 ------------------------------------------------

type fakeProductServer struct {
	productv1.UnimplementedProductServiceServer
	platform *fakePlatform
}

func (s *fakeProductServer) ListProductTypes(context.Context, *productv1.ListProductTypesRequest) (*productv1.ListProductTypesResponse, error) {
	return &productv1.ListProductTypesResponse{ProductTypes: []*productv1.ProductType{
		{Id: "general", Name: "Produk umum", SchemaVersion: 1},
	}}, nil
}

func (s *fakeProductServer) CreateProduct(ctx context.Context, req *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error) {
	// The real write side validates references over gRPC before storing. Rejecting an
	// unknown category is what a test that posts a bad category_id depends on.
	if req.GetCategoryId() != 0 {
		s.platform.mu.Lock()
		_, known := s.platform.cats[req.GetCategoryId()]
		s.platform.mu.Unlock()
		if !known {
			return nil, status.Errorf(codes.InvalidArgument, "category %d does not exist", req.GetCategoryId())
		}
	}
	if strings.TrimSpace(req.GetName()) == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	s.platform.mu.Lock()
	defer s.platform.mu.Unlock()

	id := s.platform.nextID
	s.platform.nextID++

	slug := req.GetSlug()
	if slug == "" {
		slug = strings.ToLower(strings.ReplaceAll(req.GetName(), " ", "-"))
	}
	statusValue := req.GetStatus()
	if statusValue == productv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED {
		// The real service defaults an unset status rather than storing UNSPECIFIED,
		// which no read model renders and no sale may include.
		statusValue = productv1.ProductStatus_PRODUCT_STATUS_ACTIVE
	}

	product := &fakeProduct{
		id: id, name: req.GetName(), slug: slug,
		description: req.GetDescription(), brand: req.GetBrand(),
		priceCents: req.GetPriceCents(), currency: req.GetCurrency(),
		stock: req.GetStock(), status: statusValue,
		categoryID: req.GetCategoryId(), tenant: tenantOf(ctx),
		productTypeID: req.GetProductTypeId(), schemaVersion: req.GetSchemaVersion(), attributes: req.GetAttributes(),
	}
	s.platform.products[id] = product
	return &productv1.CreateProductResponse{Product: toWireProduct(product)}, nil
}

func (s *fakeProductServer) UpdateProduct(ctx context.Context, req *productv1.UpdateProductRequest) (*productv1.UpdateProductResponse, error) {
	s.platform.mu.Lock()
	defer s.platform.mu.Unlock()

	product, ok := s.platform.products[req.GetId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such product")
	}
	if req.Name != nil {
		product.name = req.GetName()
	}
	if req.Slug != nil {
		product.slug = req.GetSlug()
	}
	if req.Description != nil {
		product.description = req.GetDescription()
	}
	if req.Brand != nil {
		product.brand = req.GetBrand()
	}
	if req.PriceCents != nil {
		product.priceCents = req.GetPriceCents()
	}
	if req.Currency != nil {
		product.currency = req.GetCurrency()
	}
	if req.Status != nil {
		product.status = req.GetStatus()
	}
	if req.CategoryId != nil {
		product.categoryID = req.GetCategoryId()
	}
	if req.ProductTypeId != nil {
		product.productTypeID = req.GetProductTypeId()
	}
	if req.SchemaVersion != nil {
		product.schemaVersion = req.GetSchemaVersion()
	}
	if req.Attributes != nil {
		product.attributes = req.GetAttributes()
	}
	// Stock is intentionally not updatable here, mirroring the real write side where
	// inventory is the only writer after creation.
	return &productv1.UpdateProductResponse{Product: toWireProduct(product)}, nil
}

func (s *fakeProductServer) DeleteProduct(ctx context.Context, req *productv1.DeleteProductRequest) (*productv1.DeleteProductResponse, error) {
	s.platform.mu.Lock()
	defer s.platform.mu.Unlock()

	product, ok := s.platform.products[req.GetId()]
	if !ok {
		return &productv1.DeleteProductResponse{Deleted: false}, nil
	}
	// Tenant scope. The real product-service has no tenant concept at all (see
	// ARCHITECTURE.md D-018), so this check is the fake standing in for the tenancy a
	// tenant-aware platform would need — it is what makes the isolation test below able
	// to prove the *gateway* threads the tenant correctly rather than prove the platform
	// enforces it.
	if tenantID := tenantOf(ctx); tenantID != "" && product.tenant != "" && product.tenant != tenantID {
		return &productv1.DeleteProductResponse{Deleted: false}, nil
	}
	delete(s.platform.products, req.GetId())
	return &productv1.DeleteProductResponse{Deleted: true}, nil
}

func toWireProduct(p *fakeProduct) *productv1.Product {
	if p == nil {
		return nil
	}
	return &productv1.Product{
		Id: p.id, CategoryId: p.categoryID, Name: p.name, Slug: p.slug,
		ProductTypeId: p.productTypeID, SchemaVersion: p.schemaVersion, Attributes: p.attributes,
		Description: p.description, Brand: p.brand,
		PriceCents: p.priceCents, Currency: p.currency,
		Stock: p.stock, Status: p.status,
		CreatedAt: timestamppb.Now(),
		UpdatedAt: timestamppb.Now(),
	}
}

// ---- read side: productlisting.v1 ------------------------------------------

type fakeListingServer struct {
	productlistingv1.UnimplementedProductListingServiceServer
	platform *fakePlatform
}

func (s *fakeListingServer) ListProducts(ctx context.Context, req *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
	s.platform.mu.Lock()
	s.platform.browseCalls++
	fails := s.platform.browseFails
	tenantID := tenantOf(ctx)
	matched := make([]*fakeProduct, 0, len(s.platform.products))
	for _, p := range s.platform.products {
		if tenantID != "" && p.tenant != "" && p.tenant != tenantID {
			continue
		}
		if req.GetCategoryId() != 0 && p.categoryID != req.GetCategoryId() {
			continue
		}
		if req.GetStatus() != productlistingv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED &&
			listStatusOf(p.status) != req.GetStatus() {
			continue
		}
		matched = append(matched, p)
	}
	s.platform.mu.Unlock()

	if fails {
		return nil, status.Error(codes.Unavailable, "listing read model unavailable")
	}

	// A real browse read model is eventual, so it can trail the write side by a moment.
	// The tests do not depend on the lag being visible; they depend on it not being
	// infinite.
	total := int64(len(matched))
	offset := int(req.GetOffset())
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	if offset > len(matched) {
		offset = len(matched)
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}

	page := make([]*productlistingv1.Product, 0, end-offset)
	for _, p := range matched[offset:end] {
		page = append(page, toWireListingProduct(p))
	}
	return &productlistingv1.ListProductsResponse{Products: page, Total: total}, nil
}

func (s *fakeListingServer) GetProduct(ctx context.Context, req *productlistingv1.GetProductRequest) (*productlistingv1.GetProductResponse, error) {
	s.platform.mu.Lock()
	tenantID := tenantOf(ctx)
	product, ok := s.platform.products[req.GetId()]
	s.platform.mu.Unlock()

	if !ok {
		return nil, status.Error(codes.NotFound, "no such product")
	}
	if tenantID != "" && product.tenant != "" && product.tenant != tenantID {
		return nil, status.Error(codes.NotFound, "no such product")
	}
	return &productlistingv1.GetProductResponse{Product: toWireListingProduct(product)}, nil
}

func (s *fakeListingServer) ListVariants(_ context.Context, _ *productlistingv1.ListVariantsRequest) (*productlistingv1.ListVariantsResponse, error) {
	return &productlistingv1.ListVariantsResponse{}, nil
}

func toWireListingProduct(p *fakeProduct) *productlistingv1.Product {
	if p == nil {
		return nil
	}
	return &productlistingv1.Product{
		Id: p.id, CategoryId: p.categoryID, Name: p.name, Slug: p.slug,
		ProductTypeId: p.productTypeID, SchemaVersion: p.schemaVersion, Attributes: p.attributes,
		Brand:      p.brand,
		PriceCents: p.priceCents, Currency: p.currency,
		Stock: p.stock, Status: listStatusOf(p.status),
		CreatedAt: timestamppb.Now(),
		UpdatedAt: timestamppb.Now(),
	}
}

func listStatusOf(s productv1.ProductStatus) productlistingv1.ProductStatus {
	switch s {
	case productv1.ProductStatus_PRODUCT_STATUS_DRAFT:
		return productlistingv1.ProductStatus_PRODUCT_STATUS_DRAFT
	case productv1.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return productlistingv1.ProductStatus_PRODUCT_STATUS_ARCHIVED
	case productv1.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return productlistingv1.ProductStatus_PRODUCT_STATUS_ACTIVE
	default:
		return productlistingv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}

// ---- search: productsearch.v1 ----------------------------------------------

type fakeSearchServer struct {
	productsearchv1.UnimplementedProductSearchServiceServer
	platform *fakePlatform
}

func (s *fakeSearchServer) SearchProducts(ctx context.Context, req *productsearchv1.SearchProductsRequest) (*productsearchv1.SearchProductsResponse, error) {
	s.platform.mu.Lock()
	s.platform.searchCalls++
	fails := s.platform.searchFails
	tenantID := tenantOf(ctx)
	term := strings.ToLower(req.GetQuery())
	matched := make([]*fakeProduct, 0, len(s.platform.products))
	for _, p := range s.platform.products {
		if tenantID != "" && p.tenant != "" && p.tenant != tenantID {
			continue
		}
		haystack := strings.ToLower(p.name + " " + p.brand + " " + p.slug)
		if term != "" && strings.Contains(haystack, term) {
			matched = append(matched, p)
		}
	}
	s.platform.mu.Unlock()

	if fails {
		return nil, status.Error(codes.Unavailable, "search read model unavailable")
	}
	// The real contract refuses a term under three characters rather than degrading
	// into a sequential scan.
	if len(req.GetQuery()) < 3 {
		return nil, status.Errorf(codes.InvalidArgument, "query must be at least 3 characters")
	}

	page := make([]*productsearchv1.SearchProduct, 0, len(matched))
	for _, p := range matched {
		page = append(page, &productsearchv1.SearchProduct{
			Id: p.id, CategoryId: p.categoryID, Name: p.name, Slug: p.slug,
			Brand:      p.brand,
			PriceCents: p.priceCents, Currency: p.currency,
			Stock: p.stock, Status: searchStatusOf(p.status),
			CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(),
		})
	}
	return &productsearchv1.SearchProductsResponse{
		Products: page, Total: int64(len(page)), Limit: req.GetLimit(), Offset: req.GetOffset(),
	}, nil
}

func searchStatusOf(s productv1.ProductStatus) productsearchv1.ProductStatus {
	switch s {
	case productv1.ProductStatus_PRODUCT_STATUS_DRAFT:
		return productsearchv1.ProductStatus_PRODUCT_STATUS_DRAFT
	case productv1.ProductStatus_PRODUCT_STATUS_ARCHIVED:
		return productsearchv1.ProductStatus_PRODUCT_STATUS_ARCHIVED
	case productv1.ProductStatus_PRODUCT_STATUS_ACTIVE:
		return productsearchv1.ProductStatus_PRODUCT_STATUS_ACTIVE
	default:
		return productsearchv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED
	}
}

// ---- categories: category.v1 (read-only) -----------------------------------

type fakeCategoryServer struct {
	categoryv1.UnimplementedCategoryServiceServer
	platform *fakePlatform
}

func (s *fakeCategoryServer) ListCategories(ctx context.Context, req *categoryv1.ListCategoriesRequest) (*categoryv1.ListCategoriesResponse, error) {
	s.platform.mu.Lock()
	defer s.platform.mu.Unlock()

	out := make([]*categoryv1.Category, 0, len(s.platform.cats))
	for _, c := range s.platform.cats {
		if !c.GetIsGlobal() && s.platform.catTenants[c.GetId()] != tenantOf(ctx) {
			continue
		}
		out = append(out, c)
	}
	return &categoryv1.ListCategoriesResponse{Categories: out, Total: int64(len(out))}, nil
}

func (s *fakeCategoryServer) CreateMerchantCategory(ctx context.Context, req *categoryv1.CreateMerchantCategoryRequest) (*categoryv1.CreateMerchantCategoryResponse, error) {
	merchantID := tenantOf(ctx)
	if merchantID == "" {
		return nil, status.Error(codes.InvalidArgument, "merchant scope is required")
	}
	s.platform.mu.Lock()
	defer s.platform.mu.Unlock()
	id := s.platform.nextCat
	s.platform.nextCat++
	category := &categoryv1.Category{
		Id: id, Name: req.GetName(), Description: req.GetDescription(), ParentId: req.GetParentId(),
		CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(), IsGlobal: false,
	}
	s.platform.cats[id] = category
	s.platform.catTenants[id] = merchantID
	return &categoryv1.CreateMerchantCategoryResponse{Category: category}, nil
}

func (s *fakeCategoryServer) GetCategory(_ context.Context, req *categoryv1.GetCategoryRequest) (*categoryv1.GetCategoryResponse, error) {
	s.platform.mu.Lock()
	defer s.platform.mu.Unlock()

	c, ok := s.platform.cats[req.GetId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such category")
	}
	return &categoryv1.GetCategoryResponse{Category: c}, nil
}

func (s *fakeCategoryServer) GetCategoryTree(_ context.Context, _ *categoryv1.GetCategoryTreeRequest) (*categoryv1.GetCategoryTreeResponse, error) {
	s.platform.mu.Lock()
	defer s.platform.mu.Unlock()

	roots := make([]*categoryv1.CategoryNode, 0, len(s.platform.cats))
	for _, c := range s.platform.cats {
		roots = append(roots, &categoryv1.CategoryNode{Category: c})
	}
	return &categoryv1.GetCategoryTreeResponse{Roots: roots}, nil
}

func (s *fakeCategoryServer) CategoryExists(_ context.Context, req *categoryv1.CategoryExistsRequest) (*categoryv1.CategoryExistsResponse, error) {
	s.platform.mu.Lock()
	defer s.platform.mu.Unlock()

	resp := &categoryv1.CategoryExistsResponse{}
	for _, id := range req.GetIds() {
		if _, ok := s.platform.cats[id]; ok {
			resp.Exists = append(resp.Exists, id)
		} else {
			resp.Missing = append(resp.Missing, id)
		}
	}
	return resp, nil
}

// addCategory seeds a category directly into the fake's store, standing in for the
// owning service's own REST write path (which the console deliberately does not call).
func (p *fakePlatform) addCategory(name string) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()

	id := p.nextCat
	p.nextCat++
	p.cats[id] = &categoryv1.Category{
		Id: id, Name: name, Description: name + " category",
		CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(), IsGlobal: true,
	}
	return id
}

// ---- transport --------------------------------------------------------------

const testBufSize = 1 << 20

// serve registers one fake over a bufconn listener and returns a dialed connection. The
// server is stopped when the test finishes.
func serve(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(testBufSize)
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

// platformConns stands up all four services and returns the connections plus their
// breakers, in the shape the production composition root builds.
func platformConns(t *testing.T) (*fakePlatform, productconsumer.Connections, productconsumer.Breakers) {
	t.Helper()

	platform := newFakePlatform()
	// The interceptor is what makes tenant metadata visible to the fakes, exactly as
	// grpcx.Dial's tenantOutgoing does in production.
	writeConn := serveWithTenant(t, func(s *grpc.Server) {
		productv1.RegisterProductServiceServer(s, &fakeProductServer{platform: platform})
	})
	browseConn := serveWithTenant(t, func(s *grpc.Server) {
		productlistingv1.RegisterProductListingServiceServer(s, &fakeListingServer{platform: platform})
	})
	searchConn := serveWithTenant(t, func(s *grpc.Server) {
		productsearchv1.RegisterProductSearchServiceServer(s, &fakeSearchServer{platform: platform})
	})
	categoryConn := serveWithTenant(t, func(s *grpc.Server) {
		categoryv1.RegisterCategoryServiceServer(s, &fakeCategoryServer{platform: platform})
	})

	return platform,
		productconsumer.Connections{
			Write: writeConn, Browse: browseConn, Search: searchConn, Category: categoryConn,
		},
		productconsumer.Breakers{
			Write: breaker.NewDefault(), Browse: breaker.NewDefault(),
			Search: breaker.NewDefault(), Category: breaker.NewDefault(),
		}
}

// serveWithTenant pairs one fake with the full tenant chain: the client interceptor that
// stamps the tenant (exactly what grpcx.Dial installs) and the server interceptor that
// reads it back. Both halves matter — without the client one, every call arrives with no
// tenant and the isolation tests prove nothing.
func serveWithTenant(t *testing.T, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(testBufSize)
	server := grpc.NewServer(grpc.UnaryInterceptor(tenantIncoming()))
	register(server)
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(tenantOutgoing()),
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

// ---- the gateway under test -------------------------------------------------

// testGateway is the assembled gateway plus the fakes behind it, so a test can seed the
// platform and assert on what the pages then render.
type testGateway struct {
	server   *httptest.Server
	platform *fakePlatform
	// breakers exposes the platform breakers so a test can open one and assert the
	// readiness probe reacts.
	breakers productconsumer.Breakers
	cfg      *config.AppConfig
}

// productForm builds a seller-console product form body from the given fields, so a
// test states only what it cares about.
func productForm(fields map[string]string) url.Values {
	form := url.Values{}
	form.Set("productTypeId", "general")
	form.Set("schemaVersion", "1")
	for key, value := range fields {
		form.Set(key, value)
	}
	return form
}

// newTestGateway builds the gateway the way the production composition root does, with
// the product platform replaced by in-process fakes: the pages consume it over the same
// gRPC contracts, on the same bufconn transport, and the modules that remain (auth,
// inventory, sales, billing, ops) are composed in-process exactly as in production.
func newTestGateway(t *testing.T, tenantCfg config.TenantConfig) *testGateway {
	t.Helper()

	platform, conns, breakers := platformConns(t)

	cfg := &config.AppConfig{
		AppEnv:         "test",
		Host:           "127.0.0.1",
		Port:           "0",
		ServiceName:    "gogo",
		ServiceVersion: "test",
		LogLevel:       slog.LevelError,
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   10 * time.Second,
		IdleTimeout:    60 * time.Second,
		RequestTimeout: 15 * time.Second,
		Tenant:         tenantCfg,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.NewMemoryBus()

	inventoryModule := inventory.NewModule(logger)
	inventoryConn, closeInventoryServer, err := grpcx.InProcess(inventoryModule.RegisterGRPC)
	if err != nil {
		t.Fatalf("grpcx.InProcess (inventory) returned error: %v", err)
	}
	t.Cleanup(closeInventoryServer)
	inventoryBreaker := breaker.NewDefault()

	salesModule, closeSales, err := salescompose.Build(
		t.Context(), cfg, salesinventoryclient.New(inventoryConn, inventoryBreaker), logger,
	)
	if err != nil {
		t.Fatalf("salescompose.Build returned error: %v", err)
	}
	t.Cleanup(closeSales)
	salesConn, closeSalesServer, err := grpcx.InProcess(salesModule.RegisterGRPC)
	if err != nil {
		t.Fatalf("grpcx.InProcess (sales) returned error: %v", err)
	}
	t.Cleanup(closeSalesServer)
	salesBreaker := breaker.NewDefault()

	authStore := authinfra.NewStore()
	authModule := authmod.NewModule(authStore, authStore, nil, logger)
	authConn, closeAuthServer, err := grpcx.InProcess(authModule.RegisterGRPC)
	if err != nil {
		t.Fatalf("grpcx.InProcess (auth) returned error: %v", err)
	}
	t.Cleanup(closeAuthServer)
	authBreaker := breaker.NewDefault()

	billingModule, err := billing.NewModule(adapter.NewEventBusAdapter(bus), logger)
	if err != nil {
		t.Fatalf("billing.NewModule returned error: %v", err)
	}

	registry := module.New(cfg.ServiceName, cfg.ServiceVersion,
		inventoryModule, salesModule, authModule, billingModule)

	upstreamBreakers := map[string]*breaker.Breaker{
		string(productconsumer.UpstreamWrite):    breakers.Write,
		string(productconsumer.UpstreamBrowse):   breakers.Browse,
		string(productconsumer.UpstreamSearch):   breakers.Search,
		string(productconsumer.UpstreamCategory): breakers.Category,
		"inventory":                              inventoryBreaker,
		"sales":                                  salesBreaker,
		"auth":                                   authBreaker,
	}

	// Readiness aggregates the registry's health plus every upstream breaker — the same
	// probe the production composition root builds from the contributed breaker group.
	// Wiring it here is what makes the degradation tests meaningful: without a probe,
	// ops would report ready no matter what the platform was doing.
	registry.AddAll(ops.NewModuleWithProbe(ops.Readiness(registry, breakerGroup(upstreamBreakers))))

	app := httpserver.NewWithRegistry(cfg, logger, registry, upstreamBreakers,
		home.New(homelistingclient.New(conns, breakers), logger),
		seller.New(
			sellercatalogclient.New(conns, breakers),
			sellerinventoryclient.New(inventoryConn, inventoryBreaker),
			sellersalesclient.New(salesConn, salesBreaker),
			logger,
		),
		profile.New(
			profileauthclient.New(authconsumer.New(authConn, authBreaker)),
			logger,
		),
	)

	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)

	return &testGateway{server: server, platform: platform, breakers: breakers, cfg: cfg}
}

// newTestServer is the tenant-less gateway, kept under the old name so the existing
// tests read unchanged.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newTestGateway(t, config.TenantConfig{}).server
}

func newTestServerWithTenants(t *testing.T, tenantCfg config.TenantConfig) *httptest.Server {
	t.Helper()
	return newTestGateway(t, tenantCfg).server
}

// request issues an HTTP request against the gateway without following redirects, so a
// test can assert on the raw status a Post/Redirect/Get produced.
func request(t *testing.T, method, baseURL, path, contentType string, form url.Values, headers map[string]string) (*http.Response, string) {
	t.Helper()

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequest(method, baseURL+path, body)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s failed: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	return resp, string(raw)
}

// itoa64 renders an int64 as a decimal string, which is how the console and the fake
// platform exchange product and category ids.
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// breakerGroup projects a name→breaker map into the contributed-group shape ops.Readiness
// takes, so the test gateway wires the same readiness probe the production composition
// root does rather than a probe that always passes.
func breakerGroup(breakers map[string]*breaker.Breaker) []fdi.Member[*breaker.Breaker] {
	members := make([]fdi.Member[*breaker.Breaker], 0, len(breakers))
	for name, b := range breakers {
		members = append(members, fdi.Member[*breaker.Breaker]{Name: name, Value: b})
	}
	return members
}

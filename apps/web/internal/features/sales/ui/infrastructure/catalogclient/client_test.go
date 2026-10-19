// Package catalogclient_test covers the console's adapter over the product
// platform. The adapter adds three things on top of the shared productconsumer kit
// the kit's own tests already cover, and this file pins exactly those: which
// upstream each call is routed to, how the console's write model is translated into
// the platform's commands, and the console-shaped decode of what comes back.
package catalogclient_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	categoryv1 "gogo/contracts/category/v1"
	productv1 "gogo/contracts/product/v1"
	productlistingv1 "gogo/contracts/productlisting/v1"
	"gogo/internal/features/sales/ui/application/results"
	"gogo/internal/features/sales/ui/infrastructure/catalogclient"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/productconsumer"
	"gogo/internal/shared/tenant"
)

// The tenant interceptors below mirror grpcx's production pair, so a bufconn dial
// has the same calling convention as the deployed one and an assertion about the
// stub seeing a tenant tests the adapter rather than the stub.
func outgoingTenant() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if id, ok := tenant.FromContext(ctx); ok {
			ctx = metadata.AppendToOutgoingContext(ctx, tenant.Header, string(id))
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func tenantIncoming() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if values := md.Get(tenant.Header); len(values) > 0 && values[0] != "" {
				ctx = tenant.WithID(ctx, tenant.ID(values[0]))
			}
		}
		return handler(ctx, req)
	}
}

func serve(t *testing.T, register func(grpc.ServiceRegistrar)) *grpc.ClientConn {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(tenantIncoming()))
	register(server)
	go func() { _ = server.Serve(listener) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(outgoingTenant()),
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

// stubPlatform is a contract-only stand-in for the three services the adapter
// calls. It lives on the stub side of the wire, so a green assertion here is
// evidence the adapter's translation is observable in the contract's terms.
type stubPlatform struct {
	productv1.UnimplementedProductServiceServer
	productlistingv1.UnimplementedProductListingServiceServer
	categoryv1.UnimplementedCategoryServiceServer

	mu sync.Mutex

	listProductsFn   func(context.Context, *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error)
	createFn         func(context.Context, *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error)
	updateFn         func(context.Context, *productv1.UpdateProductRequest) (*productv1.UpdateProductResponse, error)
	deleteFn         func(context.Context, *productv1.DeleteProductRequest) (*productv1.DeleteProductResponse, error)
	listCategoriesFn func(context.Context, *categoryv1.ListCategoriesRequest) (*categoryv1.ListCategoriesResponse, error)

	listCalls     int
	createCalls   int
	updateCalls   int
	deleteCalls   int
	categoryCalls int
	tenants       []string

	lastList       *productlistingv1.ListProductsRequest
	lastCreate     *productv1.CreateProductRequest
	lastUpdate     *productv1.UpdateProductRequest
	lastDelete     *productv1.DeleteProductRequest
	lastCategories *categoryv1.ListCategoriesRequest
}

func (s *stubPlatform) record(ctx context.Context) {
	if id, ok := tenant.FromContext(ctx); ok {
		s.tenants = append(s.tenants, string(id))
	} else {
		s.tenants = append(s.tenants, "")
	}
}

func (s *stubPlatform) ListProducts(ctx context.Context, req *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	s.lastList = req
	s.record(ctx)
	if s.listProductsFn == nil {
		return &productlistingv1.ListProductsResponse{}, nil
	}
	return s.listProductsFn(ctx, req)
}

func (s *stubPlatform) CreateProduct(ctx context.Context, req *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls++
	s.lastCreate = req
	s.record(ctx)
	if s.createFn == nil {
		return &productv1.CreateProductResponse{}, nil
	}
	return s.createFn(ctx, req)
}

func (s *stubPlatform) UpdateProduct(ctx context.Context, req *productv1.UpdateProductRequest) (*productv1.UpdateProductResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCalls++
	s.lastUpdate = req
	s.record(ctx)
	if s.updateFn == nil {
		return &productv1.UpdateProductResponse{}, nil
	}
	return s.updateFn(ctx, req)
}

func (s *stubPlatform) DeleteProduct(ctx context.Context, req *productv1.DeleteProductRequest) (*productv1.DeleteProductResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls++
	s.lastDelete = req
	s.record(ctx)
	if s.deleteFn == nil {
		return &productv1.DeleteProductResponse{}, nil
	}
	return s.deleteFn(ctx, req)
}

func (s *stubPlatform) ListCategories(ctx context.Context, req *categoryv1.ListCategoriesRequest) (*categoryv1.ListCategoriesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.categoryCalls++
	s.lastCategories = req
	s.record(ctx)
	if s.listCategoriesFn == nil {
		return &categoryv1.ListCategoriesResponse{}, nil
	}
	return s.listCategoriesFn(ctx, req)
}

func (s *stubPlatform) counts() (list, create, update, delete, categories int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls, s.createCalls, s.updateCalls, s.deleteCalls, s.categoryCalls
}

func (s *stubPlatform) seenTenants() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tenants...)
}

// newClient wires the adapter the way the composition root does: one connection
// per upstream, so each call can be proven to land on the service that owns it.
func newClient(t *testing.T, stub *stubPlatform, brk productconsumer.Breakers) *catalogclient.Client {
	t.Helper()

	write := serve(t, func(s grpc.ServiceRegistrar) { productv1.RegisterProductServiceServer(s, stub) })
	browse := serve(t, func(s grpc.ServiceRegistrar) { productlistingv1.RegisterProductListingServiceServer(s, stub) })
	category := serve(t, func(s grpc.ServiceRegistrar) { categoryv1.RegisterCategoryServiceServer(s, stub) })

	return catalogclient.New(productconsumer.Connections{Write: write, Browse: browse, Category: category}, brk)
}

func TestListAsksTheBrowseReadModelForOneConsolePage(t *testing.T) {
	t.Parallel()

	stub := &stubPlatform{listProductsFn: func(context.Context, *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
		return &productlistingv1.ListProductsResponse{Total: 2, Products: []*productlistingv1.Product{
			{
				Id: 7, CategoryId: 3, Name: "Kopi Susu", Slug: "kopi-susu", Brand: "Aroma",
				PriceCents: 15000, Currency: "IDR", Stock: 4,
				Status: productlistingv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
			},
			{
				Id: 9, Name: "Teh", Slug: "teh", PriceCents: 8000, Currency: "IDR",
				Status: productlistingv1.ProductStatus_PRODUCT_STATUS_DRAFT,
			},
		}}, nil
	}}
	client := newClient(t, stub, productconsumer.Breakers{})

	products, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("len(products) = %d, want 2", len(products))
	}

	// The console asks for one generous page rather than paginating; the browse
	// contract's page size travels as 100 and every filter stays unset.
	req := stub.lastList
	if req.GetLimit() != 100 {
		t.Errorf("wire limit = %d, want 100 (one console page)", req.GetLimit())
	}
	if req.GetOffset() != 0 || req.GetCategoryId() != 0 || req.GetStatus() != productlistingv1.ProductStatus_PRODUCT_STATUS_UNSPECIFIED {
		t.Errorf("wire request = %+v, want an unfiltered first page", req)
	}

	first := products[0]
	if first.ID != "7" || first.Name != "Kopi Susu" || first.Slug != "kopi-susu" || first.Brand != "Aroma" {
		t.Errorf("products[0] = %+v, want the browse row decoded", first)
	}
	if first.Price != 15000 || first.Currency != "IDR" {
		t.Errorf("products[0] money = %d %s, want 15000 IDR", first.Price, first.Currency)
	}
	if first.Status != "active" || first.CategoryID != "3" {
		t.Errorf("products[0] status/category = %q/%q, want active/3", first.Status, first.CategoryID)
	}
	if products[1].ID != "9" || products[1].Status != "draft" || products[1].CategoryID != "" {
		t.Errorf("products[1] = %+v, want id 9 draft with no category", products[1])
	}

	if list, create, _, _, _ := stub.counts(); list != 1 || create != 0 {
		t.Errorf("upstream calls = list %d, create %d; a read must route to browse alone", list, create)
	}
}

func TestCreateSendsTheWholeFormAndReturnsTheCommittedRow(t *testing.T) {
	t.Parallel()

	stub := &stubPlatform{createFn: func(_ context.Context, req *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error) {
		// The committed row answers, not an echo: the name and id below differ
		// from the request so the assertion proves the response is what is mapped.
		return &productv1.CreateProductResponse{Product: &productv1.Product{
			Id: 42, CategoryId: req.GetCategoryId(), Name: "Kopi Susu Gula Aren", Slug: req.GetSlug(),
			Brand: req.GetBrand(), PriceCents: req.GetPriceCents(), Currency: req.GetCurrency(),
			Stock: req.GetStock(), Status: productv1.ProductStatus_PRODUCT_STATUS_ACTIVE,
		}}, nil
	}}
	client := newClient(t, stub, productconsumer.Breakers{})

	committed, err := client.Create(context.Background(), results.ProductInput{
		Name: "Kopi Susu", Slug: "kopi-susu", Description: "Kopi dengan susu", Brand: "Aroma",
		Price: 15000, Currency: "IDR", Stock: 7, Status: "active", CategoryID: "3",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := stub.lastCreate
	if req.GetName() != "Kopi Susu" || req.GetSlug() != "kopi-susu" || req.GetDescription() != "Kopi dengan susu" || req.GetBrand() != "Aroma" {
		t.Errorf("wire request = %+v, want every console field forwarded", req)
	}
	if req.GetPriceCents() != 15000 || req.GetCurrency() != "IDR" || req.GetStock() != 7 {
		t.Errorf("wire money/stock = %d/%s/%d, want 15000/IDR/7", req.GetPriceCents(), req.GetCurrency(), req.GetStock())
	}
	if req.GetCategoryId() != 3 || req.GetStatus() != productv1.ProductStatus_PRODUCT_STATUS_ACTIVE {
		t.Errorf("wire category/status = %d/%s, want 3/ACTIVE", req.GetCategoryId(), req.GetStatus())
	}

	if committed.ID != "42" || committed.Name != "Kopi Susu Gula Aren" {
		t.Errorf("committed = %+v, want the row the write side stored", committed)
	}
	// results.Product deliberately carries no Stock: the sellable quantity is
	// inventory's to answer, not the product read model's.
	if committed.Status != "active" || committed.CategoryID != "3" {
		t.Errorf("committed status/category = %q/%q, want active/3", committed.Status, committed.CategoryID)
	}
}

// TestCreateWithNoCategorySendsTheUnsetKey pins the conversion the write contract
// documents: an unassigned category travels as 0, never as a parse of "".
func TestCreateWithNoCategorySendsTheUnsetKey(t *testing.T) {
	t.Parallel()

	stub := &stubPlatform{}
	client := newClient(t, stub, productconsumer.Breakers{})

	if _, err := client.Create(context.Background(), results.ProductInput{Name: "Teh", Status: "draft"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := stub.lastCreate.GetCategoryId(); got != 0 {
		t.Errorf("wire category = %d, want 0 for an unassigned category", got)
	}
}

// TestUpdateIsAFullEditThatNeverCarriesStock pins the adapter's two deliberate
// decisions. Every field the console's form carries is sent as a pointer, a blank
// one included — so clearing the brand really clears it. Stock is the one field
// that never crosses this call: after creation the sellable quantity belongs to
// inventory, and a metadata edit carrying it would clobber a concurrent
// adjustment.
func TestUpdateIsAFullEditThatNeverCarriesStock(t *testing.T) {
	t.Parallel()

	stub := &stubPlatform{updateFn: func(_ context.Context, req *productv1.UpdateProductRequest) (*productv1.UpdateProductResponse, error) {
		return &productv1.UpdateProductResponse{Product: &productv1.Product{
			Id: req.GetId(), Name: req.GetName(), Slug: req.GetSlug(), Description: req.GetDescription(),
			Brand: req.GetBrand(), PriceCents: req.GetPriceCents(), Currency: req.GetCurrency(),
			Status: productv1.ProductStatus_PRODUCT_STATUS_ARCHIVED, CategoryId: req.GetCategoryId(),
		}}, nil
	}}
	client := newClient(t, stub, productconsumer.Breakers{})

	input := results.ProductInput{
		Name: "Kopi Susu", Slug: "kopi-susu", Description: "", Brand: "",
		Price: 16000, Currency: "IDR", Stock: 99, Status: "archived", CategoryID: "3",
	}
	updated, err := client.Update(context.Background(), "42", input)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	req := stub.lastUpdate
	if req.GetId() != 42 {
		t.Errorf("wire id = %d, want 42", req.GetId())
	}
	if req.Name == nil || req.GetName() != "Kopi Susu" {
		t.Errorf("wire name = %v, want a pointer to the form's value", req.Name)
	}
	if req.Slug == nil || req.GetSlug() != "kopi-susu" {
		t.Errorf("wire slug = %v, want a pointer to the form's value", req.Slug)
	}
	// A blank field is sent as a pointer to "": this is the whole reason Update
	// takes a Patch rather than an Input, and the console depends on it.
	if req.Description == nil || req.GetDescription() != "" {
		t.Errorf("wire description = %v, want a pointer to empty so a cleared field is cleared", req.Description)
	}
	if req.Brand == nil || req.GetBrand() != "" {
		t.Errorf("wire brand = %v, want a pointer to empty", req.Brand)
	}
	if req.PriceCents == nil || req.GetPriceCents() != 16000 {
		t.Errorf("wire price = %v, want a pointer to 16000", req.PriceCents)
	}
	if req.Currency == nil || req.GetCurrency() != "IDR" {
		t.Errorf("wire currency = %v, want a pointer to IDR", req.Currency)
	}
	if req.CategoryId == nil || req.GetCategoryId() != 3 {
		t.Errorf("wire category = %v, want a pointer to 3", req.CategoryId)
	}
	if req.Status == nil || req.GetStatus() != productv1.ProductStatus_PRODUCT_STATUS_ARCHIVED {
		t.Errorf("wire status = %v, want a pointer to ARCHIVED", req.Status)
	}
	if req.Stock != nil {
		t.Errorf("wire stock = %v, want it absent: stock belongs to inventory after creation", req.Stock)
	}

	if updated.ID != "42" || updated.Status != "archived" || updated.Price != 16000 {
		t.Errorf("updated = %+v, want the committed row decoded", updated)
	}
}

// TestDeleteReportsAnAlreadyGoneProductAsNotFound pins the adapter's answer to the
// write side's "removed: false": the cashier pressed delete on a row that had
// already disappeared, and that disagreement about state is reported rather than
// silently treated as success.
func TestDeleteReportsAnAlreadyGoneProductAsNotFound(t *testing.T) {
	t.Parallel()

	t.Run("a row already gone is not found", func(t *testing.T) {
		t.Parallel()

		stub := &stubPlatform{deleteFn: func(context.Context, *productv1.DeleteProductRequest) (*productv1.DeleteProductResponse, error) {
			return &productv1.DeleteProductResponse{Deleted: false}, nil
		}}
		client := newClient(t, stub, productconsumer.Breakers{})

		err := client.Delete(context.Background(), "42")
		if !catalogclient.IsNotFound(err) {
			t.Fatalf("Delete of a gone row = %v, want IsNotFound", err)
		}
		if got := stub.lastDelete.GetId(); got != 42 {
			t.Errorf("wire id = %d, want 42", got)
		}
	})

	t.Run("a real removal is success", func(t *testing.T) {
		t.Parallel()

		stub := &stubPlatform{deleteFn: func(context.Context, *productv1.DeleteProductRequest) (*productv1.DeleteProductResponse, error) {
			return &productv1.DeleteProductResponse{Deleted: true}, nil
		}}
		client := newClient(t, stub, productconsumer.Breakers{})

		if err := client.Delete(context.Background(), "42"); err != nil {
			t.Fatalf("Delete = %v, want nil", err)
		}
	})
}

func TestListCategoriesDecodesIdsAndAsksForEverything(t *testing.T) {
	t.Parallel()

	stub := &stubPlatform{listCategoriesFn: func(context.Context, *categoryv1.ListCategoriesRequest) (*categoryv1.ListCategoriesResponse, error) {
		return &categoryv1.ListCategoriesResponse{Categories: []*categoryv1.Category{
			{Id: 4, Name: "Minuman"},
			{Id: 5, Name: "Makanan"},
		}}, nil
	}}
	client := newClient(t, stub, productconsumer.Breakers{})

	categories, err := client.ListCategories(context.Background())
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	if len(categories) != 2 {
		t.Fatalf("len(categories) = %d, want 2", len(categories))
	}
	if categories[0].ID != "4" || categories[0].Name != "Minuman" {
		t.Errorf("categories[0] = %+v, want 4/Minuman", categories[0])
	}
	if categories[1].ID != "5" || categories[1].Name != "Makanan" {
		t.Errorf("categories[1] = %+v, want 5/Makanan", categories[1])
	}
	// The form's picker wants the whole flat listing; the console sends no window.
	if req := stub.lastCategories; req.GetLimit() != 0 || req.GetOffset() != 0 {
		t.Errorf("wire limit/offset = %d/%d, want 0/0 (no window)", req.GetLimit(), req.GetOffset())
	}
}

// TestAnswersAreDistinguishableThroughTheHelpers pins the three helpers a handler
// branches on: a clean not-found, an outage, and the platform's own reason for a
// refusal. The outage must carry no shown detail — a transport error's text is for
// the log, not for a page.
func TestAnswersAreDistinguishableThroughTheHelpers(t *testing.T) {
	t.Parallel()

	newFailing := func(t *testing.T, code codes.Code, message string) *catalogclient.Client {
		t.Helper()
		stub := &stubPlatform{listProductsFn: func(context.Context, *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
			return nil, status.Error(code, message)
		}}
		return newClient(t, stub, productconsumer.Breakers{})
	}

	t.Run("a clean not-found", func(t *testing.T) {
		t.Parallel()

		client := newFailing(t, codes.NotFound, "product not found")
		_, err := client.List(context.Background())
		if !catalogclient.IsNotFound(err) {
			t.Errorf("IsNotFound(%v) = false, want true", err)
		}
		if catalogclient.Unavailable(err) {
			t.Errorf("Unavailable(%v) = true; a clean answer is not an outage", err)
		}
	})

	t.Run("a conflict stays a business answer", func(t *testing.T) {
		t.Parallel()

		stub := &stubPlatform{createFn: func(context.Context, *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error) {
			return nil, status.Error(codes.AlreadyExists, "Produk dengan slug tersebut sudah ada")
		}}
		client := newClient(t, stub, productconsumer.Breakers{})

		_, err := client.Create(context.Background(), results.ProductInput{Name: "Kopi"})
		if !errors.Is(err, productconsumer.ErrConflict) {
			t.Errorf("Create on a duplicate = %v, want errors.Is(_, ErrConflict)", err)
		}
		if catalogclient.Unavailable(err) {
			t.Errorf("Unavailable(%v) = true; a duplicate is not an outage", err)
		}
		if got := catalogclient.Detail(err); got != "Produk dengan slug tersebut sudah ada" {
			t.Errorf("Detail = %q, want the platform's own reason", got)
		}
	})

	t.Run("an outage is unavailable with no detail", func(t *testing.T) {
		t.Parallel()

		client := newFailing(t, codes.Unavailable, "connection refused: 10.0.0.7:18081")
		_, err := client.List(context.Background())
		if !catalogclient.Unavailable(err) {
			t.Errorf("Unavailable(%v) = false, want true", err)
		}
		if catalogclient.IsNotFound(err) {
			t.Errorf("IsNotFound(%v) = true; an outage is not not-found", err)
		}
		if got := catalogclient.Detail(err); got != "" {
			t.Errorf("Detail = %q, want empty: a transport error must not reach a page", got)
		}
	})

	t.Run("a status nobody names is an outage", func(t *testing.T) {
		t.Parallel()

		client := newFailing(t, codes.Internal, "panic in product-service")
		_, err := client.List(context.Background())
		if !catalogclient.Unavailable(err) {
			t.Errorf("Unavailable(%v) = true... unnamed statuses degrade as outages", err)
		}
		if got := catalogclient.Detail(err); got != "" {
			t.Errorf("Detail = %q, want empty for an unnamed status", got)
		}
	})
}

// TestEachUpstreamRunsUnderItsOwnBreaker pins the module seam end to end: the
// breakers the composition root hands the adapter guard their own upstream, so
// browse failing open does not take writes down with it — the console can still
// accept a product while the listing read model recovers.
func TestEachUpstreamRunsUnderItsOwnBreaker(t *testing.T) {
	t.Parallel()

	stub := &stubPlatform{
		listProductsFn: func(context.Context, *productlistingv1.ListProductsRequest) (*productlistingv1.ListProductsResponse, error) {
			return nil, status.Error(codes.Unavailable, "listing unreachable")
		},
		createFn: func(context.Context, *productv1.CreateProductRequest) (*productv1.CreateProductResponse, error) {
			return &productv1.CreateProductResponse{Product: &productv1.Product{Id: 1, Name: "Kopi"}}, nil
		},
	}
	client := newClient(t, stub, productconsumer.Breakers{
		Browse: breaker.New(1, time.Minute, 1),
	})

	if _, err := client.List(context.Background()); !catalogclient.Unavailable(err) {
		t.Fatalf("first List = %v, want the outage to surface", err)
	}
	if _, err := client.List(context.Background()); !catalogclient.Unavailable(err) {
		t.Fatalf("second List = %v, want the open circuit to answer", err)
	}
	if list, _, _, _, _ := stub.counts(); list != 1 {
		t.Errorf("browse calls = %d, want 1: the open circuit must fail fast", list)
	}

	if _, err := client.Create(context.Background(), results.ProductInput{Name: "Kopi"}); err != nil {
		t.Fatalf("Create while browse is down = %v, want the write upstream to serve", err)
	}
	if _, create, _, _, _ := stub.counts(); create != 1 {
		t.Errorf("write calls = %d, want 1: browse's outage must not cross upstreams", create)
	}
}

// TestTheRequestTenantTravelsToTheUpstream pins that the adapter inherits the
// shared calling convention: the tenant on the caller's context is stamped on the
// wire, and a caller with none sends the default tenant rather than an empty one.
func TestTheRequestTenantTravelsToTheUpstream(t *testing.T) {
	t.Parallel()

	stub := &stubPlatform{}
	client := newClient(t, stub, productconsumer.Breakers{})

	if _, err := client.List(tenant.WithID(context.Background(), "acme")); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := client.Create(context.Background(), results.ProductInput{Name: "Kopi"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	seen := stub.seenTenants()
	if len(seen) != 2 {
		t.Fatalf("upstream saw %d calls, want 2", len(seen))
	}
	if seen[0] != "acme" {
		t.Errorf("first call tenant = %q, want acme", seen[0])
	}
	if seen[1] != "default" {
		t.Errorf("second call tenant = %q, want the default tenant, never empty", seen[1])
	}
}

// Package productconsumer is the shared remote-client kit for the product
// platform: the three services that own product data outside this process.
//
//	product-service            → product.v1          writes  (Create/Update/Delete)
//	product-listing-service    → productlisting.v1   browse  (List/Get)
//	product-search-service     → productsearch.v1    search  (SearchProducts)
//	category-service           → category.v1         global and merchant categories
//
// Those services own the product aggregate outright; this package owns only what
// every caller in gogo would otherwise repeat: wrapping the generated clients,
// running calls under the shared convention (grpcconsumer — tenant stamp, circuit
// breaker, transport-vs-business error classification), and translating each wire
// shape into one neutral Core. Callers map that Core into their own read model;
// none of them imports a generated contract directly.
//
// Why one kit and not four, when D-016 keeps separately versioned contracts apart:
// these four contracts are not independent aggregates. They are four *views* of
// one product domain, owned by one deployment, and a caller that can write a
// product can also browse it. Splitting them would mean four vocabularies and
// four Core structs describing the same row, and a lowest-common-denominator
// Product that omits whichever field the other three happened to agree on. The
// neutral Core here is deliberately the union of what the contracts actually
// carry — a caller reads the fields its contract provides.
//
// Deliberate non-goal: this kit does not unify the *services*. Writes still go to
// product-service alone, and reads still come from the read models — which is why
// a read may briefly disagree with a write (the projection lag documented in
// product-listing-service's contract).
package productconsumer

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"

	categoryv1 "gogo/contracts/category/v1"
	productv1 "gogo/contracts/product/v1"
	productlistingv1 "gogo/contracts/productlisting/v1"
	productsearchv1 "gogo/contracts/productsearch/v1"
	"gogo/internal/shared/breaker"
	"gogo/internal/shared/grpcconsumer"
)

// Core is the neutral product representation every gogo surface receives. It is
// the union of the fields the product contracts carry, mapped by the adapters
// below — never by a caller.
//
// The numeric conversions are deliberate and lossy in one direction only:
//
//   - IDs arrive as int64 (product-service's key) and are carried as decimal
//     strings. gogo's own modules key products by TEXT (inventory's
//     (tenant_id, product_id) primary key, the movement ledger), so string is the
//     shape that keeps every downstream query type-consistent. Decimal
//     stringification is exact and round-trips.
//   - Money arrives as price_cents (minor units) plus a currency code. It is kept
//     as minor units here; gogo's money package formats IDR without decimals, so
//     the unit matches and no conversion factor is applied. Currency is carried
//     rather than assumed — gogo does not hardcode IDR at this boundary.
//
// A field absent from a given contract stays at its zero value: Price is 0 on a
// search hit that carries no price filter result, and Barcode stays empty
// because no product contract has one. Callers must not read an absent field as
// a real zero.
type Core struct {
	ID            string
	ProductTypeID string
	SchemaVersion int32
	Attributes    map[string]string
	Name          string
	Slug          string
	Description   string
	Brand         string
	Price         int64 // minor units
	Currency      string
	Stock         int
	Status        Status
	CategoryID    string
	MediaID       string
	Variants      []Variant
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Variant is a purchasable size or colour of a product.
type Variant struct {
	ID        string
	ProductID string
	SKU       string
	Name      string
	Price     int64
	Currency  string
	Stock     int
	MediaID   string
}

// Status is a product's lifecycle state, normalized from the enum each contract
// declares separately. product.v1, productlisting.v1 and productsearch.v1 each
// define their own identically-valued ProductStatus enum; a caller should not
// have to know which contract answered.
type Status string

const (
	StatusUnspecified Status = ""
	StatusDraft       Status = "draft"
	StatusActive      Status = "active"
	StatusArchived    Status = "archived"
)

// Category is the neutral category representation.
type Category struct {
	ID          string
	Name        string
	Description string
	ParentID    string
	IsGlobal    bool
}

type ProductAttributeDefinition struct {
	Key, Label, Kind, Unit string
	Required               bool
	Options                []string
}

type ProductType struct {
	ID, Name      string
	SchemaVersion int32
	Attributes    []ProductAttributeDefinition
}

// Page is one page of products plus its paging metadata.
type Page struct {
	Products []Core
	Total    int
	// NextCursor is set only by contracts that keyset. productlisting.v1 pages by
	// offset, so it is always empty here; it exists so a caller's pagination code
	// does not have to branch on which read model answered.
	NextCursor string
	// TookMS is set only by search, which reports its own query duration so a slow
	// search is observable as the signal that the read model needs a real engine.
	TookMS int64
}

// Query holds browse and search options. Zero values mean "no filter": an
// unfiltered browse is a valid question, which is why Query carries no separate
// "searching" flag — the caller picks search or browse by method.
type Query struct {
	// CategoryID filters to one category. Empty means unfiltered.
	CategoryID string
	// Name is a free-text term. It routes to product-search-service (which ranks
	// and requires 3+ characters) rather than to browse; see Search.
	Name string
	// Status filters to a lifecycle state. Empty means unfiltered.
	Status Status
	Limit  int
	Offset int
	// IncludeVariants embeds each product's variants in the response.
	IncludeVariants bool
}

// vocabulary is the product platform's error vocabulary: the sentinels a surface
// can branch on without importing any product service to learn its semantics.
//
// Unavailable is set because it is the one sentinel the convention guarantees,
// and it is what lets a page degrade (a 503, an empty-but-valid grid) instead of
// failing as an unknown error. NotFound and Invalid are the two business answers
// that recur across the contracts; Conflict covers the write-side rejections.
var vocabulary = grpcconsumer.Vocabulary{
	NotFound:     ErrNotFound,
	Invalid:      ErrInvalid,
	Conflict:     ErrConflict,
	Precondition: ErrFailedPrecondition,
	Unavailable:  ErrProductPlatformUnavailable,
}

// Client wraps the four generated gRPC clients, their circuit breaker, and the
// wire↔Core translation. It is embedded in a surface's adapter, which knows that
// surface's own read model and port interface.
//
// One Client per connection is not possible: the four services are four separate
// deployments, so this type is composed from four independently dialled
// connections. New and NewRemote build it either way, and a caller that only
// needs browse may pass a nil writer connection rather than dialing a service it
// never calls.
type Client struct {
	products productv1.ProductServiceClient
	browse   productlistingv1.ProductListingServiceClient
	search   productsearchv1.ProductSearchServiceClient
	category categoryv1.CategoryServiceClient

	writerCaller   *grpcconsumer.Caller
	browseCaller   *grpcconsumer.Caller
	searchCaller   *grpcconsumer.Caller
	categoryCaller *grpcconsumer.Caller
}

// Connections bundles the four upstream connections the client calls. Each is a
// grpc.ClientConnInterface, which is satisfied by both a network dial and an
// in-process bufconn, so the same client code runs in either topology.
type Connections struct {
	// Write is product-service. Required by Create/Update/Delete.
	Write grpc.ClientConnInterface
	// Browse is product-listing-service. Required by ListPage/Get.
	Browse grpc.ClientConnInterface
	// Search is product-search-service. Required by Search.
	Search grpc.ClientConnInterface
	// Category is category-service. Required by ListCategories.
	Category grpc.ClientConnInterface
}

// Breakers bundles the per-upstream circuit breakers, one per connection. They are
// separate on purpose: the storefront must keep serving browse while search is
// down, and a shared breaker would take browse out with it.
type Breakers struct {
	Write    *breaker.Breaker
	Browse   *breaker.Breaker
	Search   *breaker.Breaker
	Category *breaker.Breaker
}

// New builds a client over the four connections, defaulting any nil breaker to
// breaker.NewDefault so no upstream can be called without a breaker.
func New(conns Connections, brk Breakers) *Client {
	return &Client{
		products:       productv1.NewProductServiceClient(conns.Write),
		browse:         productlistingv1.NewProductListingServiceClient(conns.Browse),
		search:         productsearchv1.NewProductSearchServiceClient(conns.Search),
		category:       categoryv1.NewCategoryServiceClient(conns.Category),
		writerCaller:   grpcconsumer.New(orDefault(brk.Write), vocabulary),
		browseCaller:   grpcconsumer.New(orDefault(brk.Browse), vocabulary),
		searchCaller:   grpcconsumer.New(orDefault(brk.Search), vocabulary),
		categoryCaller: grpcconsumer.New(orDefault(brk.Category), vocabulary),
	}
}

func orDefault(b *breaker.Breaker) *breaker.Breaker {
	if b == nil {
		return breaker.NewDefault()
	}
	return b
}

// Breaker returns the breaker guarding one upstream, named by Upstream. The
// gateway's metrics and readiness probe read them through this so a new upstream
// reaches both without an edit in the composition root.
func (c *Client) Breaker(u Upstream) *breaker.Breaker {
	switch u {
	case UpstreamWrite:
		return c.writerCaller.Breaker()
	case UpstreamBrowse:
		return c.browseCaller.Breaker()
	case UpstreamSearch:
		return c.searchCaller.Breaker()
	case UpstreamCategory:
		return c.categoryCaller.Breaker()
	default:
		return nil
	}
}

// Upstream names one of the four product-platform connections. It exists so a
// caller can name a breaker or a dial target without a bare string constant
// repeated across the composition root.
type Upstream string

const (
	UpstreamWrite    Upstream = "product"
	UpstreamBrowse   Upstream = "product-listing"
	UpstreamSearch   Upstream = "product-search"
	UpstreamCategory Upstream = "category"
)

// ---- reads -----------------------------------------------------------------

// ListPage returns one page of the browse read model, newest first, plus paging
// metadata. productlisting.v1 pages by offset and has no keyset cursor, so
// Page.NextCursor is empty; a caller that needs keyset semantics over deep pages
// gets them from the write side's own pagination instead.
//
// A ProductStatus of StatusUnspecified is sent as UNSPECIFIED, which the contract
// treats as unfiltered.
func (c *Client) ListPage(ctx context.Context, q Query) (Page, error) {
	limit, offset := q.paging()
	resp, err := grpcconsumer.Do(c.browseCaller, ctx, "browse products", func(ctx context.Context) (*productlistingv1.ListProductsResponse, error) {
		return c.browse.ListProducts(ctx, &productlistingv1.ListProductsRequest{
			CategoryId:      q.numericCategoryID(),
			Status:          q.wireStatus(),
			MinPriceCents:   0,
			MaxPriceCents:   0,
			Limit:           int32(limit),
			Offset:          int32(offset),
			IncludeVariants: q.IncludeVariants,
		})
	})
	if err != nil {
		return Page{}, err
	}

	products := make([]Core, 0, len(resp.GetProducts()))
	for _, p := range resp.GetProducts() {
		products = append(products, coreFromBrowse(p))
	}
	return Page{Products: products, Total: int(resp.GetTotal())}, nil
}

// Get returns one product from the browse read model. The result reflects the last
// event product-listing-service consumed, not a synchronous read of the write
// side, so it can trail a just-completed write.
func (c *Client) Get(ctx context.Context, id string, includeVariants bool) (Core, error) {
	resp, err := grpcconsumer.Do(c.browseCaller, ctx, "get product", func(ctx context.Context) (*productlistingv1.GetProductResponse, error) {
		return c.browse.GetProduct(ctx, &productlistingv1.GetProductRequest{
			Id:              numericID(id),
			IncludeVariants: includeVariants,
		})
	})
	if err != nil {
		return Core{}, err
	}
	return coreFromBrowse(resp.GetProduct()), nil
}

// ListVariants returns the purchasable variants of one product from the browse
// read model.
func (c *Client) ListVariants(ctx context.Context, productID string) ([]Variant, error) {
	resp, err := grpcconsumer.Do(c.browseCaller, ctx, "list variants", func(ctx context.Context) (*productlistingv1.ListVariantsResponse, error) {
		return c.browse.ListVariants(ctx, &productlistingv1.ListVariantsRequest{ProductId: numericID(productID)})
	})
	if err != nil {
		return nil, err
	}
	out := make([]Variant, 0, len(resp.GetVariants()))
	for _, v := range resp.GetVariants() {
		out = append(out, variantFromBrowse(v))
	}
	return out, nil
}

// Search returns the products matching q.Name, ranked by relevance.
//
// Search is a different question from browse and is answered by a different
// service: product-search-service needs trigram indexes and ranking, which browse
// cannot provide. Two consequences a caller must respect:
//
//   - q.Name must be at least 3 characters. The contract rejects a shorter term
//     because a trigram index cannot accelerate it, so the call would degrade into
//     a sequential scan of the whole index. Callers that want an empty-term search
//     to work must call ListPage instead.
//   - browse caps its page size; search caps its own. Both are applied server-side.
//
// An empty q.Name is a caller bug rather than a match-everything request, and is
// reported as ErrInvalid without a round trip.
func (c *Client) Search(ctx context.Context, q Query) (Page, error) {
	term := trimSpace(q.Name)
	if len(term) < minSearchTermLen {
		return Page{}, fmt.Errorf("%w: search term must be at least %d characters", ErrInvalid, minSearchTermLen)
	}

	limit, offset := q.paging()
	resp, err := grpcconsumer.Do(c.searchCaller, ctx, "search products", func(ctx context.Context) (*productsearchv1.SearchProductsResponse, error) {
		return c.search.SearchProducts(ctx, &productsearchv1.SearchProductsRequest{
			Query:         term,
			CategoryId:    q.numericCategoryID(),
			Status:        q.searchWireStatus(),
			MinPriceCents: 0,
			MaxPriceCents: 0,
			Limit:         int32(limit),
			Offset:        int32(offset),
		})
	})
	if err != nil {
		return Page{}, err
	}

	products := make([]Core, 0, len(resp.GetProducts()))
	for _, p := range resp.GetProducts() {
		products = append(products, coreFromSearch(p))
	}
	return Page{
		Products: products,
		Total:    int(resp.GetTotal()),
		TookMS:   resp.GetTookMs(),
	}, nil
}

// ListCategories returns global categories and categories owned by the calling
// tenant. The category service applies the tenant carried by ctx.
func (c *Client) ListCategories(ctx context.Context, limit, offset int32) ([]Category, error) {
	resp, err := grpcconsumer.Do(c.categoryCaller, ctx, "list categories", func(ctx context.Context) (*categoryv1.ListCategoriesResponse, error) {
		return c.category.ListCategories(ctx, &categoryv1.ListCategoriesRequest{Limit: limit, Offset: offset})
	})
	if err != nil {
		return nil, err
	}
	out := make([]Category, 0, len(resp.GetCategories()))
	for _, cat := range resp.GetCategories() {
		out = append(out, Category{
			ID:          formatID(cat.GetId()),
			Name:        cat.GetName(),
			Description: cat.GetDescription(),
			ParentID:    formatOptionalID(cat.GetParentId()),
			IsGlobal:    cat.GetIsGlobal(),
		})
	}
	return out, nil
}

// CreateMerchantCategory creates a category visible only to the tenant carried
// by ctx. The category service rejects calls without a resolved tenant.
func (c *Client) CreateMerchantCategory(
	ctx context.Context,
	name, description, parentID string,
) (Category, error) {
	resp, err := grpcconsumer.Do(c.categoryCaller, ctx, "create merchant category", func(ctx context.Context) (*categoryv1.CreateMerchantCategoryResponse, error) {
		return c.category.CreateMerchantCategory(ctx, &categoryv1.CreateMerchantCategoryRequest{
			Name: name, Description: description, ParentId: numericID(parentID),
		})
	})
	if err != nil {
		return Category{}, err
	}
	cat := resp.GetCategory()
	if cat == nil {
		return Category{}, fmt.Errorf("category service returned an empty create result")
	}
	return Category{
		ID: formatID(cat.GetId()), Name: cat.GetName(), Description: cat.GetDescription(),
		ParentID: formatOptionalID(cat.GetParentId()), IsGlobal: cat.GetIsGlobal(),
	}, nil
}

// CategoryExists answers a batch existence check, which is the cheap way to
// validate a product's category reference (one round trip for N ids rather than
// N GetCategory calls).
func (c *Client) CategoryExists(ctx context.Context, ids []string) (existing, missing []string, err error) {
	numeric := make([]int64, 0, len(ids))
	for _, id := range ids {
		numeric = append(numeric, numericID(id))
	}
	resp, err := grpcconsumer.Do(c.categoryCaller, ctx, "category exists", func(ctx context.Context) (*categoryv1.CategoryExistsResponse, error) {
		return c.category.CategoryExists(ctx, &categoryv1.CategoryExistsRequest{Ids: numeric})
	})
	if err != nil {
		return nil, nil, err
	}
	existing = make([]string, 0, len(resp.GetExists()))
	for _, id := range resp.GetExists() {
		existing = append(existing, formatID(id))
	}
	missing = make([]string, 0, len(resp.GetMissing()))
	for _, id := range resp.GetMissing() {
		missing = append(missing, formatID(id))
	}
	return existing, missing, nil
}

// ---- writes ----------------------------------------------------------------

// Create issues a product command against the write side. The returned Core is
// the committed row as product-service stored it — not an echo of the request.
//
// product-service validates the category and media references over gRPC before
// storing, so a bad reference surfaces as ErrInvalid (400) and an unreachable
// dependency as ErrProductPlatformUnavailable (503, retryable). Neither is
// counted against the breaker: both are the upstream working correctly.
func (c *Client) Create(ctx context.Context, in Input) (Core, error) {
	resp, err := grpcconsumer.Do(c.writerCaller, ctx, "create product", func(ctx context.Context) (*productv1.CreateProductResponse, error) {
		return c.products.CreateProduct(ctx, &productv1.CreateProductRequest{
			ProductTypeId:  in.ProductTypeID,
			SchemaVersion:  in.SchemaVersion,
			Attributes:     in.Attributes,
			CategoryId:     in.numericCategoryID(),
			Name:           in.Name,
			Slug:           in.Slug,
			Description:    in.Description,
			Brand:          in.Brand,
			PriceCents:     in.Price,
			Currency:       in.Currency,
			Stock:          int32(in.Stock),
			Status:         in.wireStatus(),
			PrimaryMediaId: in.numericMediaID(),
			Variants:       in.wireVariants(),
		})
	})
	if err != nil {
		return Core{}, err
	}
	return coreFromWrite(resp.GetProduct()), nil
}

func (c *Client) ListProductTypes(ctx context.Context) ([]ProductType, error) {
	resp, err := grpcconsumer.Do(c.writerCaller, ctx, "list product types", func(ctx context.Context) (*productv1.ListProductTypesResponse, error) {
		return c.products.ListProductTypes(ctx, &productv1.ListProductTypesRequest{})
	})
	if err != nil {
		return nil, err
	}
	out := make([]ProductType, 0, len(resp.GetProductTypes()))
	for _, typ := range resp.GetProductTypes() {
		item := ProductType{ID: typ.GetId(), Name: typ.GetName(), SchemaVersion: typ.GetSchemaVersion()}
		for _, attr := range typ.GetAttributes() {
			item.Attributes = append(item.Attributes, ProductAttributeDefinition{Key: attr.GetKey(), Label: attr.GetLabel(), Kind: attr.GetKind(), Unit: attr.GetUnit(), Required: attr.GetRequired(), Options: attr.GetOptions()})
		}
		out = append(out, item)
	}
	return out, nil
}

// Detail returns the product service's client-facing reason for a business
// rejection, or an empty string when the error is not a refusal.
func Detail(err error) string { return grpcconsumer.Detail(err) }

// Update applies a partial update. Input is a pointer field map: a nil field
// leaves its column alone, which is why the contract declares its fields optional.
// The write side's own idempotency is the caller's concern — see referenceKey.
func (c *Client) Update(ctx context.Context, id string, in Patch) (Core, error) {
	resp, err := grpcconsumer.Do(c.writerCaller, ctx, "update product", func(ctx context.Context) (*productv1.UpdateProductResponse, error) {
		return c.products.UpdateProduct(ctx, &productv1.UpdateProductRequest{
			Id:             numericID(id),
			ProductTypeId:  in.ProductTypeID,
			SchemaVersion:  in.SchemaVersion,
			Attributes:     in.Attributes,
			CategoryId:     in.categoryIDPtr(),
			Name:           in.Name,
			Slug:           in.Slug,
			Description:    in.Description,
			Brand:          in.Brand,
			PriceCents:     in.Price,
			Currency:       in.Currency,
			Stock:          in.Stock,
			Status:         in.statusPtr(),
			PrimaryMediaId: in.mediaIDPtr(),
		})
	})
	if err != nil {
		return Core{}, err
	}
	return coreFromWrite(resp.GetProduct()), nil
}

// Delete removes a product. The write side answers with whether the row was
// actually removed, so a delete of something already gone is reported rather than
// silently treated as success.
func (c *Client) Delete(ctx context.Context, id string) (bool, error) {
	return grpcconsumer.Do(c.writerCaller, ctx, "delete product", func(ctx context.Context) (bool, error) {
		resp, err := c.products.DeleteProduct(ctx, &productv1.DeleteProductRequest{Id: numericID(id)})
		return resp.GetDeleted(), err
	})
}

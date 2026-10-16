package grpcclient

import (
	"context"

	"aggregator-service/graph"
	"aggregator-service/internal/graph/model"
)

// This file adapts the gRPC clients to the GraphQL layer's ports.
//
// The dependency runs one way on purpose. graph declares narrow capability
// interfaces and this file satisfies them, so the resolvers can be tested against
// stubs with no gRPC server anywhere in sight, and the gRPC clients stay usable by
// anything else that needs them. The reverse import would make every resolver test
// drag in the transport.

// Catalog adapts the listing client to graph.CatalogReader.
type Catalog struct {
	reader ListingAPI
}

// NewCatalog wraps the listing client.
func NewCatalog(listing ListingAPI) *Catalog { return &Catalog{reader: listing} }

func (c *Catalog) GetProduct(ctx context.Context, id string, includeVariants bool) (model.Product, error) {
	return c.reader.GetProduct(ctx, id, includeVariants)
}

func (c *Catalog) ListProducts(ctx context.Context, filter graph.BrowseFilter) (model.ProductPage, error) {
	page, err := c.reader.ListProducts(ctx, ProductFilter{
		CategoryID:      filter.CategoryID,
		Status:          filter.Status,
		MinPriceCents:   filter.MinPriceCents,
		MaxPriceCents:   filter.MaxPriceCents,
		Limit:           filter.Limit,
		Offset:          filter.Offset,
		IncludeVariants: filter.IncludeVariants,
	})
	if err != nil {
		return model.ProductPage{}, err
	}
	return model.ProductPage{
		Products: page.Products,
		Total:    page.Total,
		Limit:    page.Limit,
		Offset:   page.Offset,
	}, nil
}

// maxCountPage bounds the sample this method tallies.
const maxCountPage = 100

// CountProductsByCategory counts products per category id.
//
// The listing contract offers no count-by-group RPC, so this fetches one page of
// the whole catalogue and tallies it locally. That is a single round trip, which
// is the property that matters here: the obvious alternative, one ListProducts per
// category, would issue a call per category in the response and turn a field
// annotated on a category into an amplifier.
//
// The sample is bounded, and that bound is respected rather than glossed over. If
// the catalogue holds more products than one page can carry, the true total is
// unknown and every id is omitted from the result, which the resolver renders as a
// null count. Returning the tallied page total would look like an answer and would
// be wrong for exactly the large catalogues where a count is most useful.
func (c *Catalog) CountProductsByCategory(ctx context.Context, categoryIDs []string) (map[string]int64, error) {
	if len(categoryIDs) == 0 {
		return map[string]int64{}, nil
	}

	page, err := c.reader.ListProducts(ctx, ProductFilter{Limit: maxCountPage})
	if err != nil {
		return nil, err
	}
	if page.Total > int64(maxCountPage) {
		// More rows exist than one page holds, so a tally would under-report. Omit
		// every id rather than publish a sample dressed as a total.
		return map[string]int64{}, nil
	}

	wanted := make(map[string]struct{}, len(categoryIDs))
	for _, id := range categoryIDs {
		wanted[id] = struct{}{}
	}

	counts := make(map[string]int64, len(categoryIDs))
	for _, product := range page.Products {
		if _, ok := wanted[product.CategoryID]; !ok {
			continue
		}
		counts[product.CategoryID]++
	}
	// Ids with no products are present with a zero, which is different from absent
	// and is the more useful answer for an empty category.
	for id := range wanted {
		if _, ok := counts[id]; !ok {
			counts[id] = 0
		}
	}
	return counts, nil
}

// Categories adapts the category client to graph.CategoryReader.
type Categories struct {
	client CategoryAPI
}

// NewCategories wraps the category client.
func NewCategories(client CategoryAPI) *Categories { return &Categories{client: client} }

func (c *Categories) GetCategory(ctx context.Context, id string) (model.Category, error) {
	return c.client.GetCategory(ctx, id)
}

func (c *Categories) ListCategories(ctx context.Context, limit, offset int32) ([]model.Category, int64, error) {
	return c.client.ListCategories(ctx, limit, offset)
}

func (c *Categories) CategoryTree(ctx context.Context) ([]model.Category, error) {
	return c.client.CategoryTree(ctx)
}

func (c *Categories) CategoriesByIDs(ctx context.Context, ids []string) (map[string]model.Category, error) {
	return c.client.CategoriesByIDs(ctx, ids)
}

func (c *Categories) CreateCategory(ctx context.Context, in model.CreateCategoryInput) (model.Category, error) {
	return c.client.CreateCategory(ctx, in)
}

func (c *Categories) UpdateCategory(ctx context.Context, id string, in model.UpdateCategoryInput) (model.Category, error) {
	return c.client.UpdateCategory(ctx, id, in)
}

func (c *Categories) DeleteCategory(ctx context.Context, id string) error {
	return c.client.DeleteCategory(ctx, id)
}

// Media adapts the media client to graph.MediaReader.
type Media struct {
	client MediaAPI
}

// NewMedia wraps the media client.
func NewMedia(client MediaAPI) *Media { return &Media{client: client} }

func (m *Media) GetMedia(ctx context.Context, id string) (model.Media, error) {
	return m.client.GetMedia(ctx, id)
}

func (m *Media) ListMedia(ctx context.Context, ownerID string, status model.MediaStatus, limit, offset int32) ([]model.Media, int64, error) {
	return m.client.ListMedia(ctx, ownerID, status, limit, offset)
}

func (m *Media) MediaByIDs(ctx context.Context, ids []string) (map[string]model.Media, error) {
	return m.client.MediaByIDs(ctx, ids)
}

func (m *Media) DownloadURLs(ctx context.Context, ids []string) (map[string]string, error) {
	return m.client.DownloadURLs(ctx, ids, 0)
}

func (m *Media) RegisterMedia(ctx context.Context, in model.RegisterMediaInput) (model.MediaUpload, error) {
	return m.client.RegisterMedia(ctx, in)
}

func (m *Media) CompleteUpload(ctx context.Context, id string) (model.Media, error) {
	return m.client.CompleteUpload(ctx, id)
}

func (m *Media) DeleteMedia(ctx context.Context, id string) error {
	return m.client.DeleteMedia(ctx, id)
}

// Products adapts the product write client to graph.ProductWriter.
type Products struct {
	client ProductAPI
}

// NewProducts wraps the product client.
func NewProducts(client ProductAPI) *Products { return &Products{client: client} }

func (p *Products) CreateProduct(ctx context.Context, in model.CreateProductInput) (model.Product, error) {
	return p.client.CreateProduct(ctx, in)
}

func (p *Products) UpdateProduct(ctx context.Context, id string, in model.UpdateProductInput) (model.Product, error) {
	return p.client.UpdateProduct(ctx, id, in)
}

func (p *Products) DeleteProduct(ctx context.Context, id string) error {
	return p.client.DeleteProduct(ctx, id)
}

// Search adapts the search client to graph.SearchReader.
type Search struct {
	client SearchAPI
}

// NewSearch wraps the search client.
func NewSearch(client SearchAPI) *Search { return &Search{client: client} }

func (s *Search) SearchProducts(ctx context.Context, filter graph.SearchFilter) (model.SearchResult, error) {
	return s.client.SearchProducts(ctx, SearchFilter{
		Query:         filter.Query,
		CategoryID:    filter.CategoryID,
		Status:        filter.Status,
		MinPriceCents: filter.MinPriceCents,
		MaxPriceCents: filter.MaxPriceCents,
		Limit:         filter.Limit,
		Offset:        filter.Offset,
	})
}

// Compile-time checks that each adapter satisfies the port it claims to. Without
// these the mismatch surfaces at the first request that routes through the
// missing method, which is in production.
var (
	_ graph.CatalogReader  = (*Catalog)(nil)
	_ graph.CategoryReader = (*Categories)(nil)
	_ graph.MediaReader    = (*Media)(nil)
	_ graph.ProductWriter  = (*Products)(nil)
	_ graph.SearchReader   = (*Search)(nil)
)

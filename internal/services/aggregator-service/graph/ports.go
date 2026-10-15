package graph

import (
	"context"

	"aggregator-service/internal/graph/model"
)

// The five interfaces below are the GraphQL layer's view of the platform. They
// are declared here, in the consumer, rather than reused from the gRPC adapter
// package, for two reasons:
//
//   - Each one is narrower than the adapter it would otherwise expose. The
//     resolvers need categories by id and the product count for a page; they do
//     not need the whole of category-service's surface. Declaring the narrow
//     contract here means a stub in a test implements three methods, not eight.
//   - They name the *capability* rather than the service. That is what lets the
//     partial-failure design work: a resolver asks for a category and gets an
//     error it can turn into a null field, without caring whether the category
//     came from gRPC, REST, or a cache.
//
// graph.Resolver is satisfied by *grpcclient.Clients for all five gRPC-backed
// ports. Only ProjectionReporter needs a separate implementation.

// CatalogReader is the browse read model.
type CatalogReader interface {
	GetProduct(ctx context.Context, id string, includeVariants bool) (model.Product, error)
	ListProducts(ctx context.Context, filter BrowseFilter) (model.ProductPage, error)
	CountProductsByCategory(ctx context.Context, categoryIDs []string) (map[string]int64, error)
}

// BrowseFilter is a validated browse filter.
type BrowseFilter struct {
	CategoryID      string
	Status          model.Status
	MinPriceCents   int64
	MaxPriceCents   int64
	Limit           int32
	Offset          int32
	IncludeVariants bool
}

// SearchReader is the search read model.
type SearchReader interface {
	SearchProducts(ctx context.Context, filter SearchFilter) (model.SearchResult, error)
}

// SearchFilter is a validated search filter.
type SearchFilter struct {
	Query         string
	CategoryID    string
	Status        model.Status
	MinPriceCents int64
	MaxPriceCents int64
	Limit         int32
	Offset        int32
}

// CategoryReader is the category hierarchy.
type CategoryReader interface {
	GetCategory(ctx context.Context, id string) (model.Category, error)
	ListCategories(ctx context.Context, limit, offset int32) ([]model.Category, int64, error)
	CategoryTree(ctx context.Context) ([]model.Category, error)
	// CategoriesByIDs resolves a set of ids in one round trip. Absent ids are
	// omitted from the result rather than reported as errors: a page of products
	// can legitimately reference a category the caller cannot see.
	CategoriesByIDs(ctx context.Context, ids []string) (map[string]model.Category, error)

	CreateCategory(ctx context.Context, in model.CreateCategoryInput) (model.Category, error)
	UpdateCategory(ctx context.Context, id string, in model.UpdateCategoryInput) (model.Category, error)
	DeleteCategory(ctx context.Context, id string) error
}

// MediaReader is media metadata and presigned URLs.
type MediaReader interface {
	GetMedia(ctx context.Context, id string) (model.Media, error)
	ListMedia(ctx context.Context, ownerID string, status model.MediaStatus, limit, offset int32) ([]model.Media, int64, error)
	// MediaByIDs fetches metadata for several ids in one round trip.
	MediaByIDs(ctx context.Context, ids []string) (map[string]model.Media, error)
	// DownloadURLs resolves several ids to presigned URLs in one round trip.
	// Ids without a READY object are omitted, because there is no URL to hand
	// back and an empty string would be indistinguishable from a real one.
	DownloadURLs(ctx context.Context, ids []string) (map[string]string, error)

	RegisterMedia(ctx context.Context, in model.RegisterMediaInput) (model.MediaUpload, error)
	CompleteUpload(ctx context.Context, id string) (model.Media, error)
	DeleteMedia(ctx context.Context, id string) error
}

// ProductWriter is the write side. It has no read methods, and the absence is
// deliberate: product-service cannot serve a read, so offering one here would be
// an interface that always fails at runtime.
type ProductWriter interface {
	CreateProduct(ctx context.Context, in model.CreateProductInput) (model.Product, error)
	UpdateProduct(ctx context.Context, id string, in model.UpdateProductInput) (model.Product, error)
	DeleteProduct(ctx context.Context, id string) error
}

// ProjectionReporter reports read-model progress.
//
// The two read services expose this over REST only, so this is the one port the
// gRPC adapter cannot satisfy.
type ProjectionReporter interface {
	ListingStatus(ctx context.Context) (model.ProjectionStatus, error)
	SearchStatus(ctx context.Context) (model.ProjectionStatus, error)
}

// Package service holds the read-side business rules for product listings.
//
// Unlike a write service, there is very little here: the read model is a
// projection, so the rules are validation of what the caller asked for and
// assembly of the response. What is worth noticing is what this service does not
// do: it never calls product-service to check whether a product exists, and it
// never writes. A miss here means the projection has not caught up, and saying so
// is more useful than hiding it behind a synchronous lookup that would put the
// write path back in front of every reader.
//
// It also does not search. Browsing answers "what is in category 3, newest
// first", which is a bounded indexed scan, and searching answers "what matches
// this text", which needs trigram indexes and relevance ranking. Those are
// different workloads with different costs, so they are different services;
// product-search-service owns the second one and projects the same events.
package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/apperror"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

const (
	// maxListLimit caps a single page so one request cannot ask for everything.
	maxListLimit = 100
	// defaultListLimit applies when the caller does not specify one.
	defaultListLimit = 20
)

// ListingRepository is the read model this service queries.
type ListingRepository interface {
	GetByID(ctx context.Context, id int64) (*model.Product, error)
	List(ctx context.Context, q *model.ListProductsQuery) (*model.ListProductsResult, error)
	ListVariants(ctx context.Context, productID int64) ([]*model.Variant, error)
}

// ProductCache is the caching port this service depends on.
//
// Declared here, on the consumer side, so the use case names what it needs rather
// than which LRU happens to be wired at boot. That has two consequences worth
// the indirection: swapping the cache becomes a constructor argument instead of a
// code change, and "no cache" becomes a value (nopCache) rather than a nil check
// repeated in every method.
//
// Delete is part of the port rather than an implementation detail because
// invalidation is a correctness requirement here, not an optimisation: the
// projection writes these rows asynchronously, so a cached product outlives the
// update that changed it. See Invalidate.
type ProductCache interface {
	Get(key string) (*model.Product, bool)
	Set(key string, product *model.Product)
	Delete(key string)
}

// nopCache is the ProductCache that caches nothing. Wiring the service without a
// cache used to mean a nil field guarded by an if in every method; this makes the
// uncached case an ordinary implementation of the same port.
type nopCache struct{}

func (nopCache) Get(string) (*model.Product, bool) { return nil, false }
func (nopCache) Set(string, *model.Product)        {}
func (nopCache) Delete(string)                     {}

// NopCache is the ProductCache that stores nothing. Passing it is how a caller
// disables caching; the service itself is never nil-checked.
func NopCache() ProductCache { return nopCache{} }

// productCacheKey builds the cache key for one read.
//
// IncludeVariants is part of the key because the two reads return different
// shapes. This function exists because the key used to be built inline at both
// ends of GetByID, and two formats for one key is two ways to serve a variant-less
// product from the variant-bearing entry.
func productCacheKey(id int64, includeVariants bool) string {
	return fmt.Sprintf("product:%d:%t", id, includeVariants)
}

// ProductListingService answers product read queries.
type ProductListingService struct {
	repo  ListingRepository
	log   *slog.Logger
	cache ProductCache
}

// NewProductListingService returns a service backed by repo, memoising single
// product reads in cache. Pass NopCache() to disable caching.
//
// There is deliberately one constructor, not a pair. Two existed because the
// cache was a concrete type the struct held as a pointer, so "absent" had to be
// representable as nil; the port makes that unnecessary.
func NewProductListingService(repo ListingRepository, log *slog.Logger, cache ProductCache) *ProductListingService {
	if cache == nil {
		cache = nopCache{}
	}
	return &ProductListingService{repo: repo, log: log, cache: cache}
}

// GetByID returns a single product, optionally with its variants.
func (s *ProductListingService) GetByID(
	ctx context.Context,
	id int64,
	includeVariants bool,
) (*model.Product, error) {
	cacheKey := productCacheKey(id, includeVariants)
	if cached, ok := s.cache.Get(cacheKey); ok {
		return cached, nil
	}

	product, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// Always a non-nil slice so the JSON is [] rather than null.
	product.Variants = []*model.Variant{}
	if includeVariants {
		variants, err := s.repo.ListVariants(ctx, id)
		if err != nil {
			return nil, err
		}
		product.Variants = variants
	}

	s.cache.Set(cacheKey, product)

	return product, nil
}

// Invalidate drops the cached reads for a product.
//
// The projection calls this after it applies an event that changed the product's
// rows. Without it a cached entry survives until its TTL expires, so a caller
// reading a just-updated product is served the pre-update one — and the cache's
// package documentation used to promise exactly the invalidation that nothing
// implemented.
//
// Both keys are dropped because the variant-bearing and variant-less reads are
// separate entries, and invalidating one leaves the other stale.
func (s *ProductListingService) Invalidate(id int64) {
	s.cache.Delete(productCacheKey(id, false))
	s.cache.Delete(productCacheKey(id, true))
}

// List returns a page of products matching the filters.
//
// The filters are validated here rather than in the handler so the same rules
// apply however the query arrives, including over gRPC.
func (s *ProductListingService) List(
	ctx context.Context,
	q *model.ListProductsQuery,
) (*model.ListProductsResult, error) {
	if q == nil {
		q = &model.ListProductsQuery{}
	}
	if q.Limit <= 0 {
		q.Limit = defaultListLimit
	}
	if q.Limit > maxListLimit {
		return nil, fmt.Errorf(
			"%w: limit must be at most %d, got %d", apperror.ErrInvalidInput, maxListLimit, q.Limit,
		)
	}
	if q.Offset < 0 {
		return nil, fmt.Errorf("%w: offset must not be negative, got %d", apperror.ErrInvalidInput, q.Offset)
	}
	if q.CategoryID < 0 {
		return nil, fmt.Errorf(
			"%w: category_id must not be negative, got %d", apperror.ErrInvalidInput, q.CategoryID,
		)
	}
	// Presence, not value, is what distinguishes a filter from its absence: a bound
	// of 0 is a legitimate price, so rejecting it would reject a real query.
	if q.MinPriceCents != nil && *q.MinPriceCents < 0 {
		return nil, fmt.Errorf(
			"%w: min_price_cents must not be negative, got %d",
			apperror.ErrInvalidInput, *q.MinPriceCents,
		)
	}
	if q.MaxPriceCents != nil && *q.MaxPriceCents < 0 {
		return nil, fmt.Errorf(
			"%w: max_price_cents must not be negative, got %d",
			apperror.ErrInvalidInput, *q.MaxPriceCents,
		)
	}
	// Only a range that is actually bounded can be inverted. With one side absent
	// there is nothing to contradict.
	if q.MinPriceCents != nil && q.MaxPriceCents != nil && *q.MinPriceCents > *q.MaxPriceCents {
		return nil, fmt.Errorf(
			"%w: min_price_cents (%d) must not exceed max_price_cents (%d)",
			apperror.ErrInvalidInput, *q.MinPriceCents, *q.MaxPriceCents,
		)
	}
	return s.repo.List(ctx, q)
}

// ListVariants returns the variants of a product.
//
// The parent product is read first so that an unknown product is a clean 404
// rather than an empty list, which would be indistinguishable from a product that
// genuinely has no variants.
func (s *ProductListingService) ListVariants(
	ctx context.Context,
	productID int64,
) ([]*model.Variant, error) {
	if _, err := s.repo.GetByID(ctx, productID); err != nil {
		return nil, err
	}
	return s.repo.ListVariants(ctx, productID)
}

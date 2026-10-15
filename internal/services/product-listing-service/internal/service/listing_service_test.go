package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/apperror"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/model"
)

// stubRepo serves a fixed read model and records the queries it was asked for.
type stubRepo struct {
	product  *model.Product
	products []*model.Product
	variants []*model.Variant
	list     *model.ListProductsResult
	total    int64

	err         error
	gotQuery    *model.ListProductsQuery
	getByIDCall int
}

func (s *stubRepo) GetByID(_ context.Context, id int64) (*model.Product, error) {
	s.getByIDCall++
	if s.err != nil {
		return nil, s.err
	}
	if s.product == nil {
		return nil, fmt.Errorf("%w: product %d", apperror.ErrNotFound, id)
	}
	// A copy per call, because a real scan decodes a fresh row each time. Handing
	// back the same pointer would let a cached entry and a fresh read alias, and a
	// test about cache keys would pass without ever exercising them.
	row := *s.product
	return &row, nil
}

func (s *stubRepo) List(_ context.Context, q *model.ListProductsQuery) (*model.ListProductsResult, error) {
	s.gotQuery = q
	if s.list != nil {
		return s.list, nil
	}
	return &model.ListProductsResult{Products: s.products, Total: s.total}, nil
}

func (s *stubRepo) ListVariants(_ context.Context, _ int64) ([]*model.Variant, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.variants == nil {
		return []*model.Variant{}, nil
	}
	return s.variants, nil
}

func newTestService(repo ListingRepository) *ProductListingService {
	return NewProductListingService(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), NopCache())
}

func TestGetByID(t *testing.T) {
	t.Parallel()

	t.Run("omits variants unless asked", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepo{
			product:  &model.Product{ID: 1, Name: "Aurora Phone"},
			variants: []*model.Variant{{ID: 5, ProductID: 1, SKU: "AUR-15-BLK"}},
		}
		svc := newTestService(repo)

		product, err := svc.GetByID(context.Background(), 1, false)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if len(product.Variants) != 0 {
			t.Errorf("variants = %d, want 0 when not requested", len(product.Variants))
		}
		// A nil slice would render as null in JSON, which clients have to special
		// case. It must be an empty slice.
		if product.Variants == nil {
			t.Error("Variants is nil, want an empty slice so the JSON is []")
		}
	})

	t.Run("includes variants when asked", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepo{
			product:  &model.Product{ID: 1},
			variants: []*model.Variant{{ID: 5, ProductID: 1, SKU: "AUR-15-BLK"}},
		}
		product, err := newTestService(repo).GetByID(context.Background(), 1, true)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if len(product.Variants) != 1 || product.Variants[0].SKU != "AUR-15-BLK" {
			t.Errorf("variants = %+v, want the stored one", product.Variants)
		}
	})

	t.Run("reports a missing product as not found", func(t *testing.T) {
		t.Parallel()

		_, err := newTestService(&stubRepo{}).GetByID(context.Background(), 42, false)
		if !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestList(t *testing.T) {
	t.Parallel()

	t.Run("applies the default page size", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepo{}
		if _, err := newTestService(repo).List(context.Background(), nil); err != nil {
			t.Fatalf("List: %v", err)
		}
		if repo.gotQuery.Limit != defaultListLimit {
			t.Errorf("limit = %d, want %d", repo.gotQuery.Limit, defaultListLimit)
		}
	})

	t.Run("rejects invalid filters", func(t *testing.T) {
		t.Parallel()

		cases := map[string]*model.ListProductsQuery{
			"limit too large":     {Limit: maxListLimit + 1},
			"negative offset":     {Offset: -1},
			"negative category":   {CategoryID: -1},
			"negative min price":  {MinPriceCents: pricePointer(-1)},
			"negative max price":  {MaxPriceCents: pricePointer(-1)},
			"inverted price band": {MinPriceCents: pricePointer(1000), MaxPriceCents: pricePointer(100)},
		}
		for name, query := range cases {
			svc := newTestService(&stubRepo{})
			if _, err := svc.List(context.Background(), query); !errors.Is(err, apperror.ErrInvalidInput) {
				t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
			}
		}
	})

	t.Run("keeps an absent status filter as nil", func(t *testing.T) {
		t.Parallel()

		// A non-nil filter of "draft" would hide every active product, so the
		// distinction between "unset" and "draft" has to survive to the query.
		repo := &stubRepo{}
		if _, err := newTestService(repo).List(context.Background(),
			&model.ListProductsQuery{},
		); err != nil {
			t.Fatalf("List: %v", err)
		}
		if repo.gotQuery.Status != nil {
			t.Errorf("status = %v, want nil for an absent filter", *repo.gotQuery.Status)
		}
	})
}

// An unknown product must be a 404, not an empty list. The service reads the
// parent first precisely so the two are distinguishable.
func TestListVariants(t *testing.T) {
	t.Parallel()

	t.Run("returns the variants of a known product", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepo{
			product:  &model.Product{ID: 1},
			variants: []*model.Variant{{ID: 5, ProductID: 1, SKU: "AUR-15-BLK"}},
		}
		variants, err := newTestService(repo).ListVariants(context.Background(), 1)
		if err != nil {
			t.Fatalf("ListVariants: %v", err)
		}
		if len(variants) != 1 {
			t.Errorf("got %d variants, want 1", len(variants))
		}
	})

	t.Run("reports an unknown product rather than an empty list", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepo{}
		_, err := newTestService(repo).ListVariants(context.Background(), 42)
		if !errors.Is(err, apperror.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// The read model is a projection, so a row carries when the projection last wrote
// it. That is what lets a client tell fresh data from a lagging read model.
func TestProjectedAtIsExposed(t *testing.T) {
	t.Parallel()

	projected := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	repo := &stubRepo{product: &model.Product{
		ID: 1, CreatedAt: projected.Add(-time.Hour), ProjectedAt: projected,
	}}

	product, err := newTestService(repo).GetByID(context.Background(), 1, false)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !product.ProjectedAt.Equal(projected) {
		t.Errorf("projected at = %s, want %s", product.ProjectedAt, projected)
	}
	if !product.CreatedAt.Before(product.ProjectedAt) {
		t.Error("a projection cannot be written before the product existed")
	}
}

// pricePointer builds a present price bound. The query field is a pointer so that
// "no filter" and "a bound of zero" stay distinguishable all the way to SQL.
func pricePointer(v int64) *int64 { return &v }

// fakeCache is a ProductCache that records what it was asked to do, so a test can
// assert the caching decisions rather than infer them from call counts.
type fakeCache struct {
	get     map[string]*model.Product
	sets    []string
	deletes []string
}

func newFakeCache() *fakeCache {
	return &fakeCache{get: map[string]*model.Product{}}
}

func (f *fakeCache) Get(key string) (*model.Product, bool) {
	p, ok := f.get[key]
	return p, ok
}

func (f *fakeCache) Set(key string, product *model.Product) {
	f.get[key] = product
	f.sets = append(f.sets, key)
}

func (f *fakeCache) Delete(key string) {
	delete(f.get, key)
	f.deletes = append(f.deletes, key)
}

func newCachedService(repo ListingRepository) (*ProductListingService, *fakeCache) {
	c := newFakeCache()
	return NewProductListingService(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), c), c
}

// The cache is a memo, not the source of truth. A second read must not reach the
// repository again.
func TestGetByIDUsesTheCacheOnASecondRead(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{product: &model.Product{ID: 7, Name: "Aurora Phone"}}
	svc, cache := newCachedService(repo)

	if _, err := svc.GetByID(context.Background(), 7, false); err != nil {
		t.Fatalf("first GetByID: %v", err)
	}
	if _, err := svc.GetByID(context.Background(), 7, false); err != nil {
		t.Fatalf("second GetByID: %v", err)
	}

	if got := repo.getByIDCall; got != 1 {
		t.Errorf("repository was queried %d times, want 1; the second read should have been served from the cache", got)
	}
	if len(cache.sets) != 1 {
		t.Errorf("cache received %d writes, want 1", len(cache.sets))
	}
}

// includeVariants is part of the key. If it were not, the variant-less read would
// be served from the variant-bearing entry and vice versa.
func TestCacheKeySeparatesVariantAndNonVariantReads(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{product: &model.Product{ID: 7}, variants: []*model.Variant{{ID: 70}}}
	svc, _ := newCachedService(repo)

	without, err := svc.GetByID(context.Background(), 7, false)
	if err != nil {
		t.Fatalf("GetByID without variants: %v", err)
	}
	if len(without.Variants) != 0 {
		t.Errorf("expected no variants, got %d", len(without.Variants))
	}

	with, err := svc.GetByID(context.Background(), 7, true)
	if err != nil {
		t.Fatalf("GetByID with variants: %v", err)
	}
	if len(with.Variants) != 1 {
		t.Errorf("expected 1 variant, got %d", len(with.Variants))
	}

	// The second read of each shape must come from its own entry, not the other.
	againWithout, err := svc.GetByID(context.Background(), 7, false)
	if err != nil {
		t.Fatalf("GetByID without variants again: %v", err)
	}
	if len(againWithout.Variants) != 0 {
		t.Errorf("the variant-less read was answered from the variant-bearing entry: %d variants", len(againWithout.Variants))
	}
	if got := repo.getByIDCall; got != 2 {
		t.Errorf("repository queried %d times, want 2", got)
	}
}

// A miss must not be cached. Caching the error would pin a transient database
// failure for the whole TTL.
func TestGetByIDDoesNotCacheAFailure(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{err: apperror.ErrNotFound}
	svc, c := newCachedService(repo)

	for i := 0; i < 3; i++ {
		if _, err := svc.GetByID(context.Background(), 7, false); err == nil {
			t.Fatal("expected an error")
		}
	}
	if got := repo.getByIDCall; got != 3 {
		t.Errorf("repository queried %d times, want 3; a failure must not be memoised", got)
	}
	if len(c.sets) != 0 {
		t.Errorf("cache received %d writes on a failing read, want 0", len(c.sets))
	}
}

// Invalidate is what makes a projected write visible to a reader. It has to drop
// both shapes of the entry, because they are cached under different keys.
func TestInvalidateDropsBothCacheEntries(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{product: &model.Product{ID: 7}, variants: []*model.Variant{{ID: 70}}}
	svc, c := newCachedService(repo)

	if _, err := svc.GetByID(context.Background(), 7, false); err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if _, err := svc.GetByID(context.Background(), 7, true); err != nil {
		t.Fatalf("GetByID with variants: %v", err)
	}
	if len(c.get) != 2 {
		t.Fatalf("expected 2 distinct cache entries, got %d", len(c.get))
	}

	svc.Invalidate(7)

	if len(c.get) != 0 {
		t.Errorf("%d entries survived invalidation", len(c.get))
	}
	if len(c.deletes) != 2 {
		t.Errorf("invalidated %d keys, want 2 (one per variant shape)", len(c.deletes))
	}

	// A read after invalidation must go back to the repository.
	before := repo.getByIDCall
	if _, err := svc.GetByID(context.Background(), 7, false); err != nil {
		t.Fatalf("GetByID after invalidation: %v", err)
	}
	if repo.getByIDCall == before {
		t.Error("a read after invalidation was served from the cache")
	}
}

// NopCache has to satisfy the port, which is what lets a build run without one.
var _ ProductCache = NopCache()

func TestNopCacheIsInert(t *testing.T) {
	t.Parallel()

	c := NopCache()
	c.Set("k", &model.Product{ID: 1})
	if _, ok := c.Get("k"); ok {
		t.Error("NopCache must never report a hit")
	}
	c.Delete("k")
}

// A nil cache is the common mistake at a call site; it must degrade to no caching
// rather than panic on the first read.
func TestNilCacheDegradesToNoCaching(t *testing.T) {
	t.Parallel()

	repo := &stubRepo{product: &model.Product{ID: 7}}
	svc := NewProductListingService(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	if _, err := svc.GetByID(context.Background(), 7, false); err != nil {
		t.Fatalf("GetByID with a nil cache: %v", err)
	}
	if _, err := svc.GetByID(context.Background(), 7, false); err != nil {
		t.Fatalf("second GetByID with a nil cache: %v", err)
	}
	if got := repo.getByIDCall; got != 2 {
		t.Errorf("repository queried %d times, want 2; a nil cache must not cache", got)
	}
}

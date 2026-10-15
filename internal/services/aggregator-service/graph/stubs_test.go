package graph

import (
	"context"
	"fmt"
	"sync"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/graph/model"
)

// Stubs for the resolver tests.
//
// Each stub records the arguments it was called with, which is how the tests assert
// the batching behaviour. The call counts are the point of the whole exercise: a
// test that passes with 2N calls as well as 1 call proves nothing about the design,
// so these counters are asserted directly rather than inferred from the response.

// fakeCatalog is a browse read model.
type fakeCatalog struct {
	mu sync.Mutex

	product      model.Product
	page         model.ProductPage
	getErr       error
	listErr      error
	countErr     error
	counts       map[string]int64
	getCalls     int
	listCalls    int
	countCalls   int
	lastFilter   BrowseFilter
	lastCountIDs []string
}

func (f *fakeCatalog) GetProduct(ctx context.Context, id string, includeVariants bool) (model.Product, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	return f.product, f.getErr
}

func (f *fakeCatalog) ListProducts(ctx context.Context, filter BrowseFilter) (model.ProductPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	f.lastFilter = filter
	return f.page, f.listErr
}

func (f *fakeCatalog) CountProductsByCategory(ctx context.Context, categoryIDs []string) (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.countCalls++
	f.lastCountIDs = append([]string(nil), categoryIDs...)
	return f.counts, f.countErr
}

// fakeSearch is the search read model.
type fakeSearch struct {
	mu     sync.Mutex
	result model.SearchResult
	err    error
	calls  int
	last   SearchFilter
}

func (f *fakeSearch) SearchProducts(ctx context.Context, filter SearchFilter) (model.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = filter
	return f.result, f.err
}

// fakeCategories is the category hierarchy.
type fakeCategories struct {
	mu sync.Mutex

	byID     map[string]model.Category
	tree     []model.Category
	listAll  []model.Category
	total    int64
	getErr   error
	listErr  error
	treeErr  error
	idsErr   error
	writeErr error

	getCalls  int
	listCalls int
	treeCalls int
	idsCalls  int
	lastIDs   []string
}

func (f *fakeCategories) GetCategory(ctx context.Context, id string) (model.Category, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getErr != nil {
		return model.Category{}, f.getErr
	}
	cat, ok := f.byID[id]
	if !ok {
		return model.Category{}, fmt.Errorf("%w: category %s", apperror.ErrNotFound, id)
	}
	return cat, nil
}

func (f *fakeCategories) ListCategories(ctx context.Context, limit, offset int32) ([]model.Category, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.listAll, f.total, nil
}

func (f *fakeCategories) CategoryTree(ctx context.Context) ([]model.Category, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.treeCalls++
	return f.tree, f.treeErr
}

func (f *fakeCategories) CategoriesByIDs(ctx context.Context, ids []string) (map[string]model.Category, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.idsCalls++
	f.lastIDs = append([]string(nil), ids...)
	if f.idsErr != nil {
		return nil, f.idsErr
	}
	out := make(map[string]model.Category, len(ids))
	for _, id := range ids {
		if cat, ok := f.byID[id]; ok {
			out[id] = cat
		}
	}
	return out, nil
}

func (f *fakeCategories) CreateCategory(ctx context.Context, in model.CreateCategoryInput) (model.Category, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return model.Category{}, f.writeErr
	}
	return model.Category{ID: "99", Name: in.Name}, nil
}

func (f *fakeCategories) UpdateCategory(ctx context.Context, id string, in model.UpdateCategoryInput) (model.Category, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return model.Category{}, f.writeErr
	}
	return model.Category{ID: id}, nil
}

func (f *fakeCategories) DeleteCategory(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeErr
}

// fakeMedia is media-service.
type fakeMedia struct {
	mu sync.Mutex

	byID     map[string]model.Media
	listAll  []model.Media
	total    int64
	urls     map[string]string
	upload   model.MediaUpload
	getErr   error
	listErr  error
	byIDsErr error
	urlErr   error
	writeErr error

	getCalls   int
	listCalls  int
	byIDsCalls int
	urlCalls   int
	lastIDs    []string
	lastURLIDs []string
}

func (f *fakeMedia) GetMedia(ctx context.Context, id string) (model.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getErr != nil {
		return model.Media{}, f.getErr
	}
	m, ok := f.byID[id]
	if !ok {
		return model.Media{}, fmt.Errorf("%w: media %s", apperror.ErrNotFound, id)
	}
	return m, nil
}

func (f *fakeMedia) ListMedia(ctx context.Context, ownerID string, status model.MediaStatus, limit, offset int32) ([]model.Media, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	return f.listAll, f.total, f.listErr
}

func (f *fakeMedia) MediaByIDs(ctx context.Context, ids []string) (map[string]model.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byIDsCalls++
	f.lastIDs = append([]string(nil), ids...)
	if f.byIDsErr != nil {
		return nil, f.byIDsErr
	}
	out := make(map[string]model.Media, len(ids))
	for _, id := range ids {
		if m, ok := f.byID[id]; ok {
			out[id] = m
		}
	}
	return out, nil
}

func (f *fakeMedia) DownloadURLs(ctx context.Context, ids []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urlCalls++
	f.lastURLIDs = append([]string(nil), ids...)
	if f.urlErr != nil {
		return nil, f.urlErr
	}
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if url, ok := f.urls[id]; ok {
			out[id] = url
		}
	}
	return out, nil
}

func (f *fakeMedia) RegisterMedia(ctx context.Context, in model.RegisterMediaInput) (model.MediaUpload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return model.MediaUpload{}, f.writeErr
	}
	return f.upload, nil
}

func (f *fakeMedia) CompleteUpload(ctx context.Context, id string) (model.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return model.Media{}, f.writeErr
	}
	return model.Media{ID: id, Status: model.MediaStatusReady}, nil
}

func (f *fakeMedia) DeleteMedia(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writeErr
}

// fakeProducts is the write side.
type fakeProducts struct {
	mu sync.Mutex

	created model.Product
	updated model.Product
	err     error
	createN int
	updateN int
	deleteN int
	lastIn  model.CreateProductInput
	lastUpd model.UpdateProductInput
	lastID  string
}

func (f *fakeProducts) CreateProduct(ctx context.Context, in model.CreateProductInput) (model.Product, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createN++
	f.lastIn = in
	if f.err != nil {
		return model.Product{}, f.err
	}
	return f.created, nil
}

func (f *fakeProducts) UpdateProduct(ctx context.Context, id string, in model.UpdateProductInput) (model.Product, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateN++
	f.lastID = id
	f.lastUpd = in
	if f.err != nil {
		return model.Product{}, f.err
	}
	return f.updated, nil
}

func (f *fakeProducts) DeleteProduct(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteN++
	f.lastID = id
	return f.err
}

// fakeProjection reports read-model progress.
type fakeProjection struct {
	mu         sync.Mutex
	listing    model.ProjectionStatus
	search     model.ProjectionStatus
	listingErr error
	searchErr  error
	calls      int
}

func (f *fakeProjection) ListingStatus(ctx context.Context) (model.ProjectionStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.listing, f.listingErr
}

func (f *fakeProjection) SearchStatus(ctx context.Context) (model.ProjectionStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.search, f.searchErr
}

// newTestResolver wires a resolver to a fresh set of stubs.
func newTestResolver() (*Resolver, *fakeCatalog, *fakeSearch, *fakeCategories, *fakeMedia, *fakeProducts, *fakeProjection) {
	catalog := &fakeCatalog{
		counts: map[string]int64{},
		page:   model.ProductPage{Products: []model.Product{}, Limit: 20},
	}
	search := &fakeSearch{result: model.SearchResult{Hits: []model.SearchHit{}}}
	categories := &fakeCategories{byID: map[string]model.Category{}, listAll: []model.Category{}}
	media := &fakeMedia{byID: map[string]model.Media{}, urls: map[string]string{}}
	products := &fakeProducts{}
	projection := &fakeProjection{
		listing: model.ProjectionStatus{Source: "product-listing-service", Healthy: true},
		search:  model.ProjectionStatus{Source: "product-search-service", Healthy: true},
	}

	return &Resolver{
		CatalogStore:    catalog,
		SearchStore:     search,
		CategoryStore:   categories,
		MediaStore:      media,
		ProductStore:    products,
		ProjectionStore: projection,
	}, catalog, search, categories, media, products, projection
}

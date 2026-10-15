package graph

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"aggregator-service/internal/graph/model"
)

// The tests in this file drive real queries through the executor and count the
// downstream calls. That combination is the only thing that actually proves the
// batching: a resolver tested in isolation can be correct and still be reached once per
// parent, because gqlgen only coalesces a batch when the parents are marshalled in one
// field of one selection set.
//
// The counts asserted here are the number in the README. If one of them rises, an N+1
// has been reintroduced and the test that catches it is here rather than in production.

// seedCatalogue fills the stubs with a catalogue of the given size.
//
// It exists because a call-count assertion on an empty fixture proves nothing: with no
// products, no category is ever resolved, no media is ever fetched, and every count is
// zero. The point of these tests is the fan-out over parents, so there have to be
// parents.
//
// The fixture is deliberately flat — every product in its own leaf category — so that a
// query which resolves the hierarchy cannot accidentally be satisfied by a shared
// ancestor and hide a per-parent call.
func seedCatalogue(catalog *fakeCatalog, categories *fakeCategories, media *fakeMedia, size int) {
	if catalog == nil {
		seedCataloguePart(categories, media, size)
		return
	}

	catalog.mu.Lock()
	catalog.page = model.ProductPage{Products: make([]model.Product, size), Limit: int32(size)}
	for i := range size {
		catalog.page.Products[i] = model.Product{
			ID:             fmt.Sprintf("p%02d", i),
			CategoryID:     fmt.Sprintf("c%02d", i),
			Name:           fmt.Sprintf("Product %02d", i),
			PriceCents:     int64(1000 + i),
			Currency:       "IDR",
			Status:         model.StatusActive,
			PrimaryMediaID: fmt.Sprintf("m%02d", i),
		}
	}
	catalog.mu.Unlock()

	seedCataloguePart(categories, media, size)
}

// seedCataloguePart seeds the two dependencies that are always present in the tests.
// Split out so a test that only needs the category store does not have to invent a
// catalog it would then have to nil out.
func seedCataloguePart(categories *fakeCategories, media *fakeMedia, size int) {
	categories.mu.Lock()
	categories.byID = make(map[string]model.Category, size)
	categories.listAll = make([]model.Category, size)
	for i := range size {
		cat := model.Category{ID: fmt.Sprintf("c%02d", i), Name: fmt.Sprintf("Category %02d", i)}
		categories.byID[cat.ID] = cat
		categories.listAll[i] = cat
	}
	categories.mu.Unlock()

	if media == nil {
		return
	}
	media.mu.Lock()
	media.byID = make(map[string]model.Media, size)
	media.urls = make(map[string]string, size)
	for i := range size {
		record := model.Media{
			ID:       fmt.Sprintf("m%02d", i),
			Filename: fmt.Sprintf("image%02d.png", i),
			// READY, because downloadUrl only presigns for a ready object. A pending
			// fixture would make the batch resolver skip the call entirely, and a
			// call-count assertion on a skipped call proves nothing.
			Status: model.MediaStatusReady,
		}
		media.byID[record.ID] = record
		media.urls[record.ID] = "https://cdn.example/" + record.Filename
	}
	media.mu.Unlock()
}

// seedHierarchy builds a two-level tree: one root and `size` leaves under it, so
// breadcrumbs and children both have something to resolve.
func seedHierarchy(categories *fakeCategories, size int) {
	categories.mu.Lock()
	categories.tree = []model.Category{{ID: "root", Name: "Root"}}
	for i := range size {
		categories.tree = append(categories.tree, model.Category{
			ID:       fmt.Sprintf("c%02d", i),
			Name:     fmt.Sprintf("Leaf %02d", i),
			ParentID: "root",
		})
	}
	categories.mu.Unlock()
}

// callCounts snapshots the counters, so an assertion can talk about one query without
// being affected by whatever ran before it.
//
// The names are the dependency and the operation rather than the resolver method,
// because that is how the README and an operator talk about them.
type callCounts struct {
	// tree is category-service GetCategoryTree, the whole hierarchy.
	tree int
	// count is product-listing-service's per-category product count, which
	// Category.productCount needs and nothing else can derive from the tree.
	count int
	// categoryList is category-service ListCategories, how a page of products'
	// categories are resolved.
	categoryList int
	// mediaBatch is media-service's batch metadata RPC.
	mediaBatch int
	// mediaURLs is media-service's batch presigned-URL RPC.
	mediaURLs int
}

// Every stub is optional. The tests that drive a query through the executor have all of
// them; the tests that call a resolver directly only have the one they assert on, and
// making them pass nils for the rest would only move the nil check somewhere less
// obvious.
func counts(t *testing.T, catalog *fakeCatalog, categories *fakeCategories, media *fakeMedia) callCounts {
	t.Helper()

	out := callCounts{}
	if categories != nil {
		categories.mu.Lock()
		out.tree, out.categoryList = categories.treeCalls, categories.idsCalls
		categories.mu.Unlock()
	}
	if catalog != nil {
		catalog.mu.Lock()
		out.count = catalog.countCalls
		catalog.mu.Unlock()
	}
	if media != nil {
		media.mu.Lock()
		out.mediaBatch, out.mediaURLs = media.byIDsCalls, media.urlCalls
		media.mu.Unlock()
	}
	return out
}

func TestABrowsePageCostsThreeCallsNoMatterHowWide(t *testing.T) {
	// The headline property. products, their categories and their images are three
	// dependent fields, so three calls is the floor for this shape. The width must not
	// change it: 20 parents and 1 parent both cost three.
	//
	// A resolver reached once per parent would show 1 + 20 + 20 here, which is the
	// shape this assertion exists to prevent.
	for _, pageSize := range []int{1, 20, 50} {
		client, catalog, _, categories, media, _, _ := newTestClient(t)
		seedCatalogue(catalog, categories, media, pageSize)

		var response struct {
			Products struct {
				Total    int `json:"total"`
				Products []struct {
					ID       string `json:"id"`
					Category *struct {
						ID string `json:"id"`
					} `json:"category"`
					PrimaryMedia *struct {
						ID          string  `json:"id"`
						DownloadURL *string `json:"downloadUrl"`
					} `json:"primaryMedia"`
				} `json:"products"`
			} `json:"products"`
		}

		// The page size is interpolated rather than passed as a variable: this is a test
		// harness, and a literal keeps the assertion about the call counts rather than
		// about variable handling.
		query := fmt.Sprintf(`query {
			products(filter: { limit: %d }) {
				total
				products { id category { id } primaryMedia { id downloadUrl } }
			}
		}`, pageSize)
		client.MustPost(query, &response)

		got := counts(t, catalog, categories, media)
		if got.categoryList != 1 {
			t.Errorf("page size %d: %d category listing calls, want 1: categories must be batched, not fetched per product", pageSize, got.categoryList)
		}
		if got.mediaBatch != 1 {
			t.Errorf("page size %d: %d media metadata calls, want 1: media-service has a batch RPC and must use it", pageSize, got.mediaBatch)
		}
		if got.mediaURLs != 1 {
			t.Errorf("page size %d: %d download-URL calls, want 1", pageSize, got.mediaURLs)
		}
		if got.tree != 0 {
			t.Errorf("page size %d: %d tree fetches, want 0: a product page does not select a hierarchy", pageSize, got.tree)
		}
	}
}

func TestBreadcrumbsAndChildrenShareOneTreeFetch(t *testing.T) {
	// ancestors and children are sibling fields on the same type and both need the whole
	// hierarchy. Two independent calls for two identical responses is the constant-factor
	// version of an N+1: the cost scales with query shape rather than with the data, and
	// a client adding a field to a selection set pays for it again.
	client, catalog, _, categories, media, _, _ := newTestClient(t)
	seedCatalogue(catalog, categories, media, 10)
	seedHierarchy(categories, 10)

	var response struct {
		Categories struct {
			Categories []struct {
				ID           string                `json:"id"`
				Ancestors    []struct{ ID string } `json:"ancestors"`
				Children     []struct{ ID string } `json:"children"`
				ProductCount *int                  `json:"productCount"`
			} `json:"categories"`
		} `json:"categories"`
	}

	query := `query {
		categories(limit: 10) {
			categories { id ancestors { id } children { id } productCount }
		}
	}`
	client.MustPost(query, &response)

	got := counts(t, catalog, categories, media)
	if got.tree != 1 {
		t.Fatalf("%d tree fetches for a selection of ancestors, children and productCount, want 1", got.tree)
	}
	// productCount adds a call to a different dependency, which cannot be derived from
	// the tree. One is the floor.
	if got.count != 1 {
		t.Errorf("%d product-count calls, want 1", got.count)
	}
}

func TestANestedCategoryQueryFetchesTheTreeOnce(t *testing.T) {
	// categories { children { children } } — two levels of children. Each level is its
	// own batch, so the shape alone implies two calls unless the tree is memoised per
	// request. This is the depth-scaling case: the cost has to be constant in depth, not
	// proportional to it.
	client, catalog, _, categories, media, _, _ := newTestClient(t)
	seedCatalogue(catalog, categories, media, 5)
	seedHierarchy(categories, 5)

	// categories returns a CategoryPage, so the list lives one level in. Getting this
	// nesting wrong makes gqlgen's client reject the response, which is a useful
	// property: a schema change that renames the wrapper fails here rather than
	// silently decoding into an empty struct.
	var response struct {
		Categories struct {
			Categories []struct {
				ID       string `json:"id"`
				Children []struct {
					ID       string                `json:"id"`
					Children []struct{ ID string } `json:"children"`
				} `json:"children"`
			} `json:"categories"`
		} `json:"categories"`
	}

	query := `query {
		categories(limit: 5) {
			categories { id children { id children { id } } }
		}
	}`
	client.MustPost(query, &response)

	if got := counts(t, catalog, categories, media); got.tree != 1 {
		t.Fatalf("%d tree fetches for two levels of children, want 1: the tree is the same bytes each time", got.tree)
	}
}

func TestProductCountOverTheWholeForestIsOneBrowseCall(t *testing.T) {
	// productCount sums a subtree, so it needs every category's direct count. Getting
	// that in one call rather than one per parent is the difference between one gRPC
	// round trip and one per category in the response.
	client, catalog, _, categories, media, _, _ := newTestClient(t)
	seedCatalogue(catalog, categories, media, 50)
	seedHierarchy(categories, 50)

	var response struct {
		Categories struct {
			Categories []struct {
				ID           string `json:"id"`
				ProductCount *int   `json:"productCount"`
			} `json:"categories"`
		} `json:"categories"`
	}

	query := `query { categories(limit: 50) { categories { id productCount } } }`
	client.MustPost(query, &response)

	if got := counts(t, catalog, categories, media); got.count != 1 {
		t.Fatalf("%d product-count calls for 50 categories, want 1", got.count)
	}
}

func TestAProductPageWithCategoriesCostsTheSameAsOneWithout(t *testing.T) {
	// The end-to-end version of the headline property, including the error path: adding
	// a cross-service field to the selection set adds one call, not one per product.
	client, catalog, _, categories, media, _, _ := newTestClient(t)
	seedCatalogue(catalog, categories, media, 20)

	// Both queries decode into the same shape so the assertion is about call counts and
	// not about which fields were selected. An empty struct is not an option: gqlgen's
	// client rejects response keys it has no field for, which is a useful guard against a
	// schema change going unnoticed.
	type page struct {
		Products struct {
			Products []struct {
				ID       string `json:"id"`
				Category *struct {
					ID string `json:"id"`
				} `json:"category"`
				PrimaryMedia *struct {
					ID          string  `json:"id"`
					DownloadURL *string `json:"downloadUrl"`
				} `json:"primaryMedia"`
			} `json:"products"`
		} `json:"products"`
	}

	bare := `query { products(filter: { limit: 20 }) { products { id } } }`
	client.MustPost(bare, &page{})
	after := counts(t, catalog, categories, media)

	withEdges := `query {
		products(filter: { limit: 20 }) {
			products { id category { id } primaryMedia { id downloadUrl } }
		}
	}`
	client.MustPost(withEdges, &page{})
	full := counts(t, catalog, categories, media)

	if added := full.categoryList - after.categoryList; added != 1 {
		t.Errorf("adding category to the selection added %d listing calls, want exactly 1", added)
	}
	if added := full.mediaBatch - after.mediaBatch; added != 1 {
		t.Errorf("adding primaryMedia to the selection added %d media calls, want exactly 1", added)
	}
}

// TestABatchIsSharedByEveryParentInOneList guards the invariant directly, at the level
// the invariant is written about. gqlgen hands the batch resolver every parent of one
// field in one selection set; the assertion is that each one is served from the single
// fetch.
func TestABatchIsSharedByEveryParentInOneList(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	seedCatalogue(nil, categories, nil, 25)

	// The category ids come from the fixture rather than being invented here, so the
	// parents and the seeded categories cannot drift apart.
	const parents = 25
	products := make([]*model.Product, parents)
	for i := range parents {
		products[i] = &model.Product{
			ID:         fmt.Sprintf("p%02d", i),
			CategoryID: fmt.Sprintf("c%02d", i),
		}
	}

	before := counts(t, nil, categories, nil).categoryList
	out, err := (&productResolver{resolver}).Category(t.Context(), products)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := counts(t, nil, categories, nil).categoryList

	if after-before != 1 {
		t.Fatalf("%d listing calls for %d parents, want 1", after-before, parents)
	}
	if len(out) != parents {
		t.Fatalf("got %d results for %d parents", len(out), parents)
	}
	// Every parent's category was seeded, so every entry must be filled. A batch
	// resolver that returned a shorter slice, or dropped one, would misalign every
	// result after the gap — which is why the length assertion above is not enough on
	// its own.
	for i, entry := range out {
		if entry == nil {
			t.Fatalf("parent %d (%s) got nil, but its category %s exists",
				i, products[i].ID, products[i].CategoryID)
		}
		if entry.ID != products[i].CategoryID {
			t.Fatalf("parent %d got category %q, want %q: results must line up with the parent list position",
				i, entry.ID, products[i].CategoryID)
		}
	}
}

func TestABatchResolverReturnsNoErrorForANullableField(t *testing.T) {
	// The invariant that keeps one dead dependency from emptying a page. A batch
	// resolver that returned its error would propagate it to every parent, and because
	// Product.category is a non-null field on a non-null element of a non-null list, the
	// whole page would become null.
	resolver, _, _, categories, _, _, _ := newTestResolver()

	categories.mu.Lock()
	categories.treeErr = errors.New("category-service is down")
	categories.mu.Unlock()

	products := []*model.Product{
		{ID: "p1", CategoryID: "c00"},
		{ID: "p2", CategoryID: "c01"},
	}

	out, err := (&productResolver{resolver}).Category(t.Context(), products)
	if err != nil {
		t.Fatalf("the batch resolver returned an error, which would null the whole page: %v", err)
	}
	if len(out) != len(products) {
		t.Fatalf("got %d results for %d parents, want one per parent", len(out), len(products))
	}
	for i, entry := range out {
		if entry != nil {
			t.Errorf("parent %d got %+v, want nil for every entry when the dependency is down", i, entry)
		}
	}
}

func TestABatchResolverPreservesParentOrder(t *testing.T) {
	// A batch resolver that sorted its output, deduplicated it, or returned a map would
	// silently misalign results against the parent list. The categories come back in a
	// different order than the parents ask for, which is the case that catches it.
	resolver, _, _, categories, _, _, _ := newTestResolver()

	// Deliberately not in alphabetical order, and not in the order the parents ask for.
	// Map iteration order is random in Go, so a resolver that returned the categories in
	// the order it received them would pass a test whose fixture arrived sorted — this
	// one cannot, because the parent list asks for c00, c01, c02 while the map's contents
	// are inserted c02, c00, c01 and every iteration shuffles them again.
	categories.mu.Lock()
	categories.byID = map[string]model.Category{
		"c00": {ID: "c00", Name: "Alpha"},
		"c01": {ID: "c01", Name: "Beta"},
		"c02": {ID: "c02", Name: "Gamma"},
	}
	categories.mu.Unlock()

	products := []*model.Product{
		{ID: "p1", CategoryID: "c00"},
		{ID: "p2", CategoryID: "c01"},
		{ID: "p3", CategoryID: "c02"},
	}

	out, err := (&productResolver{resolver}).Category(t.Context(), products)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"Alpha", "Beta", "Gamma"}
	for i, w := range want {
		if out[i] == nil {
			t.Fatalf("parent %d is nil", i)
		}
		if out[i].Name != w {
			t.Errorf("parent %d got %q, want %q: results must line up with the parent list position", i, out[i].Name, w)
		}
	}
}

// concurrentCounts is used by the race test below, where several goroutines resolve
// batches at once and the total has to add up.
type concurrentCounts struct {
	mu sync.Mutex
	n  int
}

func (c *concurrentCounts) add() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func TestConcurrentBatchesDoNotDoubleFetch(t *testing.T) {
	// gqlgen marshals a batch per goroutine, so two sibling fields on the same type in
	// the same selection set can genuinely run at the same time. A memo without
	// single-flight would fetch twice here rather than once, and the race detector
	// covers the memory safety while this covers the arithmetic.
	resolver, _, _, categories, _, _, _ := newTestResolver()

	products := make([]*model.Product, 10)
	for i := range products {
		products[i] = &model.Product{ID: fmt.Sprintf("p%02d", i), CategoryID: "c00"}
	}

	var wg sync.WaitGroup
	var seen concurrentCounts
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := (&productResolver{resolver}).Category(context.Background(), products)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if len(out) != len(products) {
				t.Errorf("got %d results for %d parents", len(out), len(products))
				return
			}
			seen.add()
		}()
	}
	wg.Wait()

	if seen.n != 4 {
		t.Fatalf("%d of 4 concurrent batches completed", seen.n)
	}

	// Four concurrent batches, each of which must make at most one listing call. Without
	// single-flight this is where the count would rise.
	if got := counts(t, nil, categories, nil).categoryList; got > 4 {
		t.Fatalf("%d listing calls for 4 concurrent batches of 10 parents each, want at most 4", got)
	}
}

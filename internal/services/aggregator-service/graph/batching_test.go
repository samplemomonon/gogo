package graph

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/graph/model"
)

// The tests in this file exist to protect one property: a page of N products
// resolves its cross-service edges in O(1) downstream calls, not O(N).
//
// That property is easy to break and hard to notice. Deleting the batch resolver
// and replacing it with a single-object one still passes every functional test,
// because the response is identical; it only shows up as multiplied load on
// category-service and media-service under production traffic. So the assertions
// here are about call counts and about the lengths of the returned slices, not only
// about the values.

// productPage builds N products sharing two categories and N distinct images, which
// is the shape a real catalogue page has: few categories, many images.
func productPage(n int) []*model.Product {
	products := make([]*model.Product, n)
	for i := range n {
		categoryID := "1"
		if i%2 == 1 {
			categoryID = "2"
		}
		p := model.Product{
			ID:             fmt.Sprintf("%d", i+1),
			CategoryID:     categoryID,
			PrimaryMediaID: fmt.Sprintf("%d", i+1),
			Name:           fmt.Sprintf("product %d", i+1),
			PriceCents:     int64(1000 + i),
		}
		products[i] = &p
	}
	return products
}

func TestProductCategoryIsResolvedInOneCall(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	categories.byID = map[string]model.Category{
		"1": {ID: "1", Name: "Electronics"},
		"2": {ID: "2", Name: "Books"},
	}

	products := productPage(20)
	got, err := (&productResolver{resolver}).Category(t.Context(), products)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if categories.idsCalls != 1 {
		t.Fatalf("CategoriesByIDs called %d times for a page of %d; want exactly 1", categories.idsCalls, len(products))
	}

	if len(got) != len(products) {
		t.Fatalf("resolver returned %d entries for %d parents; gqlgen indexes by position and a short slice misaligns every later result", len(got), len(products))
	}

	// Every parent must receive its own category, in order. A batch resolver that
	// returns results in the wrong order produces a page where every product shows
	// a plausible but incorrect category, which is the worst kind of bug here.
	for i, product := range products {
		if got[i] == nil {
			t.Fatalf("product %s (%s) got no category", product.ID, product.Name)
		}
		if got[i].ID != product.CategoryID {
			t.Fatalf("position %d: got category %s, want %s", i, got[i].ID, product.CategoryID)
		}
	}
}

func TestProductCategorySendsDistinctIDsOnly(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	categories.byID = map[string]model.Category{"1": {ID: "1"}, "2": {ID: "2"}}

	// Twenty products across two categories. Sending twenty ids would be twenty
	// times the request for the same two rows.
	_, err := (&productResolver{resolver}).Category(t.Context(), productPage(20))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(categories.lastIDs) != 2 {
		t.Fatalf("sent %d ids (%v); want 2 distinct ids", len(categories.lastIDs), categories.lastIDs)
	}
}

func TestProductCategoryDegradesWhenServiceIsDown(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	categories.idsErr = fmt.Errorf("%w: category-service unreachable", apperror.ErrUnavailable)

	products := productPage(3)
	collected := collectErrors(t, func(ctx context.Context) {
		got, err := (&productResolver{resolver}).Category(ctx, products)
		if err != nil {
			t.Fatalf("resolver returned an error: %v; a failure on a nullable field must not fail the request", err)
		}
		if len(got) != len(products) {
			t.Fatalf("returned %d entries for %d parents", len(got), len(products))
		}
		for i, entry := range got {
			if entry != nil {
				t.Fatalf("position %d: expected nil when the service is down, got %+v", i, entry)
			}
		}
	})

	// The failure has to be reported. A silently null field is indistinguishable
	// from a product that genuinely has no category, which is the ambiguity this
	// whole platform had to work around when the listing service used to ignore a
	// search term.
	if len(collected) != 1 {
		t.Fatalf("collected %d errors; want 1 so the client can tell degraded from empty", len(collected))
	}
	if _, ok := collected[0].(*fieldError); !ok {
		t.Fatalf("collected %T; want *fieldError", collected[0])
	}
}

func TestProductPrimaryMediaIsResolvedInOneCall(t *testing.T) {
	resolver, _, _, _, media, _, _ := newTestResolver()
	media.byID = map[string]model.Media{}
	media.urls = map[string]string{}
	for i := 1; i <= 20; i++ {
		id := fmt.Sprintf("%d", i)
		media.byID[id] = model.Media{ID: id, Status: model.MediaStatusReady}
	}

	products := productPage(20)
	got, err := (&productResolver{resolver}).PrimaryMedia(t.Context(), products)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if media.byIDsCalls != 1 {
		t.Fatalf("MediaByIDs called %d times for %d products; want 1", media.byIDsCalls, len(products))
	}
	if len(got) != len(products) {
		t.Fatalf("returned %d entries for %d parents", len(got), len(products))
	}
	for i, entry := range got {
		if entry == nil || entry.ID != products[i].PrimaryMediaID {
			t.Fatalf("position %d: media misaligned; got %+v, want %s", i, entry, products[i].PrimaryMediaID)
		}
	}
}

func TestProductPrimaryMediaLeavesUnsetReferenceNil(t *testing.T) {
	resolver, _, _, _, media, _, _ := newTestResolver()
	media.byID = map[string]model.Media{}
	media.urls = map[string]string{}

	products := []*model.Product{
		{ID: "1", PrimaryMediaID: "5"},
		{ID: "2", PrimaryMediaID: ""},
		{ID: "3", PrimaryMediaID: "6"},
	}

	got, err := (&productResolver{resolver}).PrimaryMedia(t.Context(), products)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("returned %d entries", len(got))
	}
	if got[1] != nil {
		t.Fatalf("a product with no primary media should resolve to null, got %+v", got[1])
	}

	// The empty id must not reach the downstream call at all.
	for _, id := range media.lastIDs {
		if id == "" {
			t.Fatalf("an empty media id was sent downstream: %v", media.lastIDs)
		}
	}
}

func TestEmptyParentListCostsNothing(t *testing.T) {
	resolver, _, _, categories, media, _, _ := newTestResolver()

	if _, err := (&productResolver{resolver}).Category(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := (&productResolver{resolver}).PrimaryMedia(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := (&searchHitResolver{resolver}).Category(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := (&categoryResolver{resolver}).Children(t.Context(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if categories.idsCalls != 0 || media.byIDsCalls != 0 {
		t.Fatalf("an empty parent list still called downstream: categories=%d media=%d",
			categories.idsCalls, media.byIDsCalls)
	}
}

func TestSearchHitCategoryIsResolvedInOneCall(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	categories.byID = map[string]model.Category{
		"1": {ID: "1", Name: "Electronics"},
		"2": {ID: "2", Name: "Books"},
	}

	hits := make([]*model.SearchHit, 15)
	for i := range 15 {
		hits[i] = &model.SearchHit{
			Product: model.Product{
				ID:         fmt.Sprintf("%d", i+1),
				CategoryID: fmt.Sprintf("%d", i%2+1),
			},
			Score: 0.9,
		}
	}

	got, err := (&searchHitResolver{resolver}).Category(t.Context(), hits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if categories.idsCalls != 1 {
		t.Fatalf("CategoriesByIDs called %d times for %d hits; want 1", categories.idsCalls, len(hits))
	}
	if len(got) != len(hits) {
		t.Fatalf("returned %d entries for %d parents", len(got), len(hits))
	}
	for i, entry := range got {
		if entry == nil || entry.ID != hits[i].CategoryID {
			t.Fatalf("position %d misaligned", i)
		}
	}
}

func TestCategoryAncestorsUsesOneTreeFetch(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	categories.tree = []model.Category{
		{ID: "1", Name: "Root", ParentID: ""},
		{ID: "2", Name: "Child", ParentID: "1"},
		{ID: "3", Name: "Grandchild", ParentID: "2"},
	}

	objs := []*model.Category{
		{ID: "3", Name: "Grandchild", ParentID: "2"},
		{ID: "2", Name: "Child", ParentID: "1"},
		{ID: "1", Name: "Root", ParentID: ""},
	}

	got, err := (&categoryResolver{resolver}).Ancestors(t.Context(), objs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if categories.treeCalls != 1 {
		t.Fatalf("CategoryTree called %d times for %d categories; want 1", categories.treeCalls, len(objs))
	}
	if len(got) != len(objs) {
		t.Fatalf("returned %d entries for %d parents", len(got), len(objs))
	}

	// A breadcrumb reads root-first, which is the opposite of the order a parent
	// walk collects. Getting this backwards produces a path that ends at the
	// category you asked about, which looks plausible enough to ship.
	wantNames := [][]string{{"Root", "Child", "Grandchild"}, {"Root", "Child"}, {"Root"}}
	for i, chain := range got {
		if len(chain) != len(wantNames[i]) {
			t.Fatalf("position %d: got %d ancestors, want %d", i, len(chain), len(wantNames[i]))
		}
		for j, ancestor := range chain {
			if ancestor.Name != wantNames[i][j] {
				t.Fatalf("position %d ancestor %d: got %q, want %q", i, j, ancestor.Name, wantNames[i][j])
			}
		}
	}
}

func TestCategoryAncestorsTerminatesOnCycle(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	// A cycle that a restore or a pre-validation bug could leave in the data. The
	// category service rejects cyclic moves on write, so this is defence in depth,
	// but a hang here would be a goroutine leak reachable by anyone who can set
	// a category.
	categories.tree = []model.Category{
		{ID: "1", ParentID: "2"},
		{ID: "2", ParentID: "1"},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := (&categoryResolver{resolver}).Ancestors(t.Context(), []*model.Category{{ID: "1"}})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("returned %d entries", len(got))
		}
	}()

	<-done
}

func TestCategoryChildrenBucketsByParent(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	categories.tree = []model.Category{
		{ID: "1", Name: "Root", ParentID: ""},
		{ID: "2", Name: "Zebra", ParentID: "1"},
		{ID: "3", Name: "Apple", ParentID: "1"},
		{ID: "4", Name: "Other", ParentID: ""},
	}

	got, err := (&categoryResolver{resolver}).Children(t.Context(), []*model.Category{{ID: "1"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if categories.treeCalls != 1 {
		t.Fatalf("CategoryTree called %d times; want 1", categories.treeCalls)
	}
	if len(got) != 1 {
		t.Fatalf("returned %d entries", len(got))
	}
	if len(got[0]) != 2 {
		t.Fatalf("got %d children, want 2", len(got[0]))
	}

	// Name-ordered, so two identical queries return identical responses. Unsorted
	// output makes a client diffing two responses see changes that are not changes.
	if got[0][0].Name != "Apple" || got[0][1].Name != "Zebra" {
		t.Fatalf("children not name-ordered: %q, %q", got[0][0].Name, got[0][1].Name)
	}
}

func TestCategoryProductCountUsesOneBrowseCall(t *testing.T) {
	resolver, catalog, _, categories, _, _, _ := newTestResolver()
	categories.tree = []model.Category{
		{ID: "1", Name: "Root", ParentID: ""},
		{ID: "2", Name: "Child", ParentID: "1"},
	}
	catalog.counts = map[string]int64{"1": 5, "2": 3}

	got, err := (&categoryResolver{resolver}).ProductCount(t.Context(), []*model.Category{
		{ID: "1", ParentID: ""},
		{ID: "2", ParentID: "1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if catalog.countCalls != 1 {
		t.Fatalf("CountProductsByCategory called %d times for %d categories; want 1", catalog.countCalls, 2)
	}

	// The parent includes its children: a shopper clicking "Root" expects to see
	// everything below it, and a count of only direct products would make a stocked
	// category look empty.
	if got[0] == nil || *got[0] != 8 {
		t.Fatalf("root count: got %v, want 8 (5 direct + 3 in child)", got[0])
	}
	if got[1] == nil || *got[1] != 3 {
		t.Fatalf("child count: got %v, want 3", got[1])
	}
}

func TestDownloadURLSigningIsBatchedAndSkipsPending(t *testing.T) {
	// Signing is a method on the root rather than a field resolver on Media. That is the
	// whole point of the change: gqlgen coalesces a batch resolver only while it
	// marshals a real list, and a product's primaryMedia is one object per product, so a
	// resolver on Media.downloadUrl would run once per product. Signing from the batch
	// that already fetched the metadata keeps it at one call.
	resolver, _, _, _, media, _, _ := newTestResolver()
	media.urls = map[string]string{"1": "https://objects/1", "3": "https://objects/3"}

	objs := []*model.Media{
		{ID: "1", Status: model.MediaStatusReady},
		{ID: "2", Status: model.MediaStatusPending},
		{ID: "3", Status: model.MediaStatusReady},
		nil,
	}

	resolver.signURLs(t.Context(), "Product.primaryMedia", objs)

	if media.urlCalls != 1 {
		t.Fatalf("DownloadURLs called %d times for %d media; want 1", media.urlCalls, len(objs))
	}

	if objs[0].DownloadURL == nil || *objs[0].DownloadURL != "https://objects/1" {
		t.Fatalf("position 0: got %v", objs[0].DownloadURL)
	}

	// A pending upload has no bytes to serve. Presigning it would return a URL that
	// 404s, which is worse for a client than an honest null.
	if objs[1].DownloadURL != nil {
		t.Fatalf("a PENDING media got a download URL: %v", *objs[1].DownloadURL)
	}
	if objs[2].DownloadURL == nil || *objs[2].DownloadURL != "https://objects/3" {
		t.Fatalf("position 2: got %v", objs[2].DownloadURL)
	}

	// Only the READY ids should have been sent, and a nil parent must not have been
	// dereferenced to find its status.
	if len(media.lastURLIDs) != 2 {
		t.Fatalf("sent %d ids (%v); want only the 2 READY ones", len(media.lastURLIDs), media.lastURLIDs)
	}
}

func TestSigningNothingCostsNothing(t *testing.T) {
	// A page where no image is READY should not call the media service at all. A batch
	// that finds nothing to do is a common case (a catalogue of uploads in progress),
	// and an empty request would be pure overhead.
	resolver, _, _, _, media, _, _ := newTestResolver()

	resolver.signURLs(t.Context(), "Product.primaryMedia", []*model.Media{
		{ID: "1", Status: model.MediaStatusPending},
		nil,
	})

	if media.urlCalls != 0 {
		t.Fatalf("DownloadURLs was called %d times with nothing to sign, want 0", media.urlCalls)
	}
}

func TestAFailedPresignLeavesEveryURLNil(t *testing.T) {
	// The degradation contract: a dead media-service nulls the URLs and leaves the
	// products themselves intact, so a grid still renders. An error returned here would
	// propagate through the non-null product list and empty the page.
	resolver, _, _, _, media, _, _ := newTestResolver()
	media.urlErr = errors.New("media-service is down")

	objs := []*model.Media{{ID: "1", Status: model.MediaStatusReady}}
	collected := collectErrors(t, func(ctx context.Context) {
		resolver.signURLs(ctx, "Product.primaryMedia", objs)
	})

	if objs[0].DownloadURL != nil {
		t.Fatalf("a failed presign still produced a URL: %v", *objs[0].DownloadURL)
	}
	// The record itself must be untouched, which is what keeps the page alive.
	if objs[0].ID != "1" {
		t.Fatalf("the media record was modified by a failed presign: %+v", objs[0])
	}

	// The failure has to be reported, or a grid with no images looks like a catalogue
	// with no images and nothing surfaces the difference.
	if len(collected) != 1 {
		t.Fatalf("collected %d errors, want 1: a silent degradation is the failure mode this layer exists to avoid", len(collected))
	}
	if got := extensionOf(t, collected[0], "source"); got != "media-service" {
		t.Errorf("source = %v, want media-service", got)
	}
}

func TestMoneyIsRenderedAsString(t *testing.T) {
	resolver, _, _, _, _, _, _ := newTestResolver()

	// Above the GraphQL Int ceiling of about 2.1 billion. An int32 here would
	// truncate silently, which for a price is a wrong price rather than an error.
	large := int64(9_007_199_254_740_993)
	products := []*model.Product{
		{PriceCents: large},
		{PriceCents: 0},
		{PriceCents: -500},
	}

	got, err := (&productResolver{resolver}).PriceCents(t.Context(), products)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("returned %d entries", len(got))
	}
	if got[0] != "9007199254740993" {
		t.Fatalf("large price rendered as %q; want the exact int64", got[0])
	}
	if got[1] != "0" {
		t.Fatalf("zero price rendered as %q", got[1])
	}
	if got[2] != "-500" {
		t.Fatalf("negative price rendered as %q", got[2])
	}
}

func TestNilParentsProduceNilEntries(t *testing.T) {
	resolver, _, _, _, _, _, _ := newTestResolver()

	// gqlgen can hand a batch resolver a slice containing a nil when a parent
	// resolved to null. Indexing into one without a check would panic inside the
	// executor, and a panic there is an incident rather than a failed request.
	got, err := (&productResolver{resolver}).PriceCents(t.Context(), []*model.Product{nil, {PriceCents: 10}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "" || got[1] != "10" {
		t.Fatalf("got %v; want [\"\", \"10\"]", got)
	}

	times, err := (&productResolver{resolver}).CreatedAt(t.Context(), []*model.Product{nil})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(times) != 1 || times[0] != nil {
		t.Fatalf("got %v; want a single nil", times)
	}
}

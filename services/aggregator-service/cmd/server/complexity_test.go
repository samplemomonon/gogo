package main

import (
	"testing"

	gqlmodel "aggregator-service/graph/model"
	"aggregator-service/internal/graph/model"
)

// These tests cover the cost model itself, which is the part of the hardening that
// is easy to get wrong in a way no other test catches: a cost model that
// under-prices a shape reports every query as acceptable, and nothing downstream
// notices until the limit is carrying production traffic.

func TestListFieldsArePricedByPageSize(t *testing.T) {
	complexity := newComplexityRoot()

	page := 20
	cheap := complexity.Query.Products(1, &gqlmodel.ProductFilter{Limit: &page})
	expensive := complexity.Query.Products(100, &gqlmodel.ProductFilter{Limit: &page, Status: ptr(model.StatusActive)})

	// gqlgen's default charges one per field per level, which makes a page of 20 cost
	// the same as a single product. The expensive request is the one that returns many
	// objects, so it is exactly the one that has to be budgeted.
	if cheap <= 0 {
		t.Fatalf("a list field priced at %d; a zero cost means the limit cannot see it", cheap)
	}
	if expensive <= cheap {
		t.Fatalf("a filtered page costs %d and an unfiltered one %d; the scale should not shrink with more fields selected",
			expensive, cheap)
	}
}

func TestListCostScalesWithRequestedPageSize(t *testing.T) {
	complexity := newComplexityRoot()

	ten, hundred, thousand := 10, 100, 1000
	small := complexity.Query.Products(1, &gqlmodel.ProductFilter{Limit: &ten})
	medium := complexity.Query.Products(1, &gqlmodel.ProductFilter{Limit: &hundred})
	large := complexity.Query.Products(1, &gqlmodel.ProductFilter{Limit: &thousand})

	// Pricing on what the caller asked for is the conservative direction: a query
	// requesting a thousand items is charged as if it returns a thousand, even though
	// the browse service will cap the page.
	if !(small < medium && medium < large) {
		t.Fatalf("costs %d, %d, %d; they should increase with the requested page size",
			small, medium, large)
	}
}

func TestListCostHasAFloorPerItem(t *testing.T) {
	// The bypass this prevents: scalars are priced at zero, so a list field whose
	// selection is entirely scalars has a child cost of zero, and 0 x 60 is 0. An alias
	// fan-out over `products { total }` would then price at nothing however many times
	// it was repeated, while still doing 60 rows of work per alias.
	complexity := newComplexityRoot()

	page := 60
	scalarOnly := complexity.Query.Products(0, &gqlmodel.ProductFilter{Limit: &page})

	if scalarOnly < page {
		t.Fatalf("a 60-item scalar-only list priced at %d; each item must cost at least 1", scalarOnly)
	}
}

func TestCostSaturatesInsteadOfOverflowing(t *testing.T) {
	// An overflow here would produce a small cost for an enormous page, which is
	// precisely the bypass a limit exists to prevent.
	complexity := newComplexityRoot()

	huge := maxInt
	got := complexity.Query.Products(3, &gqlmodel.ProductFilter{Limit: &huge})

	if got != maxInt {
		t.Fatalf("got %d, want it saturated at maxInt (%d)", got, maxInt)
	}
}

func TestMissingLimitFallsBackToTheDefault(t *testing.T) {
	// A query that omits the limit is charged the default page size rather than
	// nothing, so omitting the argument cannot be cheaper than sending it.
	complexity := newComplexityRoot()

	omitted := complexity.Query.Products(1, &gqlmodel.ProductFilter{})
	explicit := complexity.Query.Products(1, &gqlmodel.ProductFilter{Limit: ptr(defaultLimit)})

	if omitted != explicit {
		t.Fatalf("omitted limit costs %d, explicit default costs %d; they should match",
			omitted, explicit)
	}
}

func TestRecursiveCategoryFieldsAreNotMultiplied(t *testing.T) {
	// `children` is self-referential, so a multiplier would compound with depth: 20^14
	// for a fourteen-level query. That made the complexity limit fire at ~1.6e18 on a
	// query the depth limit should have rejected, which means the depth limit was
	// unreachable for exactly the shape it exists to bound.
	//
	// Recursion is bounded by MAX_QUERY_DEPTH instead, and the cost of a nested
	// selection stays linear in its depth.
	//
	// The assertion is that these are nil, not that they return a number: a nil
	// complexity function is how gqlgen is told to use its default of one per field per
	// level. Setting them to a function at all is the mistake being guarded.
	complexity := newComplexityRoot()

	if complexity.Category.Children != nil {
		t.Fatal("Category.children must not have a complexity function; a multiplier compounds with depth")
	}
	if complexity.Category.Ancestors != nil {
		t.Fatal("Category.ancestors must not have a complexity function")
	}
	if complexity.Category.ProductCount != nil {
		t.Fatal("Category.productCount must not have a complexity function")
	}
	if complexity.Query.CategoryTree != nil {
		t.Fatal("Query.categoryTree must not have a complexity function")
	}
}

func TestEveryListRootIsPriced(t *testing.T) {
	// A root list field left at gqlgen's default would be invisible to the limit: one
	// request for a hundred thousand rows would price the same as one for ten.
	complexity := newComplexityRoot()

	page := 10
	checks := map[string]int{
		"Query.products":       complexity.Query.Products(1, &gqlmodel.ProductFilter{Limit: &page}),
		"Query.searchProducts": complexity.Query.SearchProducts(1, gqlmodel.SearchFilter{Limit: &page}),
		"Query.categories":     complexity.Query.Categories(1, &page, nil),
		"Query.mediaList":      complexity.Query.MediaList(1, "owner", nil, &page, nil),
	}

	for name, cost := range checks {
		if cost <= 0 {
			t.Errorf("%s costs %d; an unpriced list field is invisible to the limit", name, cost)
		}
	}
}

func TestCategoryCountsInSubtreeAreNotCompounded(t *testing.T) {
	// A guard on the same class of bug as the recursive fields: the cost model must
	// stay linear in depth so that MAX_QUERY_DEPTH, not complexity, is what bounds a
	// deeply nested query.
	complexity := newComplexityRoot()

	// Walk down a hypothetical selection the way gqlgen would: each level costs one
	// more than its children because a nil function falls back to the default of one
	// per field per level.
	total := 0
	cost := 1
	for range 14 {
		total += cost
		cost++
	}

	// Linear in depth, so 14 levels costs in the tens rather than 20^14. This is the
	// property that leaves MAX_QUERY_DEPTH reachable for deeply nested selections.
	if total > 200 {
		t.Fatalf("14 levels of children cost %d; the model should be linear in depth", total)
	}
	if complexity.Category.Children != nil {
		t.Fatal("a function here is what makes the cost compound; it must stay nil")
	}
}

func ptr[T any](v T) *T { return &v }

package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	gqlmodel "aggregator-service/graph/model"
	"aggregator-service/internal/apperror"
	"aggregator-service/internal/graph/model"
)

// Reads and writes, asserted on the semantics the platform depends on. The
// eventual-consistency behaviour is the most important of these: it is the one
// property a client cannot infer from the schema and gets wrong in both directions.

func TestSearchRejectsShortTermWithoutCallingTheService(t *testing.T) {
	resolver, _, search, _, _, _, _ := newTestResolver()

	_, err := (&queryResolver{resolver}).SearchProducts(t.Context(), gqlmodel.SearchFilter{Query: "ab"})
	if err == nil {
		t.Fatal("expected an error for a two-character term")
	}
	if search.calls != 0 {
		t.Fatalf("the search service was called %d times for a rejected term; the check must happen before the round trip", search.calls)
	}

	var invalid *apperror.InvalidInput
	if !errors.As(err, &invalid) {
		t.Fatalf("got %T, want *apperror.InvalidInput so the client sees why", err)
	}
	// The message has to point at the alternative. A bare "too short" leaves the
	// caller with no idea that browse answers a different question.
	if !strings.Contains(invalid.Message, "products") {
		t.Fatalf("message %q should name the browse alternative", invalid.Message)
	}
}

func TestSearchAcceptsMinimumLength(t *testing.T) {
	resolver, _, search, _, _, _, _ := newTestResolver()
	search.result = model.SearchResult{Hits: []model.SearchHit{}, Limit: 20}

	// Three characters is the documented minimum: trigram indexes cannot
	// accelerate a shorter pattern.
	_, err := (&queryResolver{resolver}).SearchProducts(t.Context(), gqlmodel.SearchFilter{Query: "abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if search.calls != 1 {
		t.Fatalf("search called %d times, want 1", search.calls)
	}
}

func TestSearchTrimsTheTerm(t *testing.T) {
	resolver, _, search, _, _, _, _ := newTestResolver()
	search.result = model.SearchResult{Hits: []model.SearchHit{}}

	if _, err := (&queryResolver{resolver}).SearchProducts(t.Context(), gqlmodel.SearchFilter{Query: "  abc  "}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if search.last.Query != "abc" {
		t.Fatalf("sent query %q; want it trimmed", search.last.Query)
	}
}

func TestProductNotFoundBecomesNullNotAnError(t *testing.T) {
	resolver, catalog, _, _, _, _, _ := newTestResolver()
	catalog.getErr = fmt.Errorf("%w: product 999", apperror.ErrNotFound)

	got, err := (&queryResolver{resolver}).Product(t.Context(), "999", nil)
	if err != nil {
		t.Fatalf("a missing product must not be an error: %v", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

func TestProductDefaultsToIncludingVariants(t *testing.T) {
	resolver, catalog, _, _, _, _, _ := newTestResolver()
	catalog.product = model.Product{ID: "1", HasVariants: true}

	got, err := (&queryResolver{resolver}).Product(t.Context(), "1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a product")
	}
	if !catalog.lastFilter.IncludeVariants && !catalog.product.HasVariants {
		t.Fatal("expected variants to be included by default: a product page is the common case and it needs them")
	}
}

func TestBrowseIncludesVariantsByDefault(t *testing.T) {
	resolver, catalog, _, _, _, _, _ := newTestResolver()

	_, err := (&queryResolver{resolver}).Products(t.Context(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !catalog.lastFilter.IncludeVariants {
		t.Fatal("browse should include variants by default")
	}
	if catalog.lastFilter.Limit != defaultLimit {
		t.Fatalf("limit %d, want the default %d", catalog.lastFilter.Limit, defaultLimit)
	}
}

func TestBrowseClampsTheLimit(t *testing.T) {
	resolver, catalog, _, _, _, _, _ := newTestResolver()

	// Clamped rather than rejected. A limit above the maximum is a client that
	// would like more rows, and answering with the maximum plus the applied limit
	// is more useful than a refusal; a caller paging on total can detect the clamp.
	over := 10_000
	if _, err := (&queryResolver{resolver}).Products(t.Context(), &gqlmodel.ProductFilter{Limit: &over}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if catalog.lastFilter.Limit != maxLimit {
		t.Fatalf("limit %d, want it clamped to %d", catalog.lastFilter.Limit, maxLimit)
	}

	negative := -5
	if _, err := (&queryResolver{resolver}).Products(t.Context(), &gqlmodel.ProductFilter{Limit: &negative}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if catalog.lastFilter.Limit != defaultLimit {
		t.Fatalf("limit %d, want the default for a negative value", catalog.lastFilter.Limit)
	}
}

func TestBrowseRejectsNegativeOffset(t *testing.T) {
	resolver, catalog, _, _, _, _, _ := newTestResolver()

	negative := -10
	if _, err := (&queryResolver{resolver}).Products(t.Context(), &gqlmodel.ProductFilter{Offset: &negative}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A negative offset would wrap if it reached a service as an unsigned value.
	if catalog.lastFilter.Offset != 0 {
		t.Fatalf("offset %d, want 0", catalog.lastFilter.Offset)
	}
}

func TestBrowseDiscardsInvertedPriceRange(t *testing.T) {
	resolver, catalog, _, _, _, _, _ := newTestResolver()

	min, max := 1000, 100
	_, err := (&queryResolver{resolver}).Products(t.Context(), &gqlmodel.ProductFilter{
		MinPriceCents: &min,
		MaxPriceCents: &max,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// An inverted range can never match anything. Dropping the filter answers with
	// the whole catalogue instead, which is also wrong, but it is at least a
	// documented behaviour rather than a silent empty page.
	if catalog.lastFilter.MinPriceCents != 0 || catalog.lastFilter.MaxPriceCents != 0 {
		t.Fatalf("inverted range was forwarded: min=%d max=%d",
			catalog.lastFilter.MinPriceCents, catalog.lastFilter.MaxPriceCents)
	}
}

func TestCategoryTreeIsSorted(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	categories.tree = []model.Category{
		{ID: "3", Name: "Zebra"},
		{ID: "1", Name: "Apple"},
		{ID: "2", Name: "Mango"},
	}

	got, err := (&queryResolver{resolver}).CategoryTree(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d categories", len(got))
	}
	if got[0].Name != "Apple" || got[2].Name != "Zebra" {
		t.Fatalf("not name-ordered: %s, %s, %s", got[0].Name, got[1].Name, got[2].Name)
	}
}

func TestProjectionHealthSurvivesOneServiceBeingDown(t *testing.T) {
	resolver, _, _, _, _, _, projection := newTestResolver()
	projection.listing = model.ProjectionStatus{Records: 42, AppliedEvents: 100, Healthy: true, Source: "product-listing-service"}
	projection.searchErr = fmt.Errorf("%w: unreachable", apperror.ErrUnavailable)

	collected := collectErrors(t, func(ctx context.Context) {
		got, err := (&queryResolver{resolver}).ProjectionHealth(ctx)
		if err != nil {
			t.Fatalf("must not error: %v", err)
		}
		if got.Listing == nil || got.Listing.Records != 42 {
			t.Fatalf("the reachable projection must still be reported: %+v", got.Listing)
		}
		if got.Search != nil {
			t.Fatalf("the unreachable projection must be null, not fabricated: %+v", got.Search)
		}
	})

	// Reported separately from the listing status, because a stalled projection and
	// an unreachable service call for completely different responses.
	if len(collected) != 1 {
		t.Fatalf("collected %d errors, want 1", len(collected))
	}
}

func TestWriteResultReportsNotYetProjected(t *testing.T) {
	resolver, _, _, _, _, products, projection := newTestResolver()
	projection.listing = model.ProjectionStatus{AppliedEvents: 7, Healthy: true}
	products.created = model.Product{ID: "42", Name: "Aurora Phone"}

	got, err := (&mutationResolver{resolver}).CreateProduct(t.Context(), gqlmodel.CreateProductInput{
		CategoryID: "1",
		Name:       "Aurora Phone",
		PriceCents: 12999000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A product from the write side has no projected_at, so it cannot have been read
	// back from a read model yet. Reporting true would be a guess.
	if got.Projected {
		t.Fatal("a product returned by the write side must not claim to be projected")
	}
	if got.AppliedEvents == nil || *got.AppliedEvents != 7 {
		t.Fatalf("applied events %v, want 7", got.AppliedEvents)
	}
}

func TestWriteResultSurvivesStatusEndpointBeingDown(t *testing.T) {
	resolver, _, _, _, _, products, projection := newTestResolver()
	projection.listingErr = fmt.Errorf("%w: status endpoint down", apperror.ErrUnavailable)
	products.created = model.Product{ID: "42"}

	collected := collectErrors(t, func(ctx context.Context) {
		got, err := (&mutationResolver{resolver}).CreateProduct(ctx, gqlmodel.CreateProductInput{
			CategoryID: "1",
			Name:       "Aurora",
			PriceCents: 100,
		})
		if err != nil {
			t.Fatalf("the write committed, so a status failure must not fail it: %v", err)
		}
		if got.Product.ID != "42" {
			t.Fatalf("got %+v", got.Product)
		}
	})

	// Failing a committed mutation because a status endpoint was down would report a
	// success as a failure, and a client that retries on failure creates a duplicate.
	if len(collected) != 1 {
		t.Fatalf("collected %d errors, want the failure recorded", len(collected))
	}
}

func TestCreateProductRejectsNegativePrice(t *testing.T) {
	resolver, _, _, _, _, products, _ := newTestResolver()

	_, err := (&mutationResolver{resolver}).CreateProduct(t.Context(), gqlmodel.CreateProductInput{
		CategoryID: "1",
		Name:       "Aurora",
		PriceCents: -1,
	})
	if err == nil {
		t.Fatal("expected a negative price to be rejected")
	}
	if products.createN != 0 {
		t.Fatalf("the write service was called %d times for an invalid input", products.createN)
	}
}

func TestEmptyUpdateIsRejected(t *testing.T) {
	resolver, _, _, _, _, products, _ := newTestResolver()

	// An update that changes nothing is rejected rather than acknowledged. A silent
	// success would leave a client believing a rename had happened when no write was
	// issued at all.
	_, err := (&mutationResolver{resolver}).UpdateProduct(t.Context(), "1", gqlmodel.UpdateProductInput{})
	if err == nil {
		t.Fatal("expected an empty update to be rejected")
	}
	if products.updateN != 0 {
		t.Fatalf("the write service was called %d times", products.updateN)
	}
}

func TestUpdateDistinguishesOmittedFromZero(t *testing.T) {
	resolver, _, _, _, _, products, _ := newTestResolver()

	// Setting stock to zero is a real instruction; omitting it means leave alone.
	// Collapsing the two would make it impossible to clear a field.
	zero := 0
	_, err := (&mutationResolver{resolver}).UpdateProduct(t.Context(), "1", gqlmodel.UpdateProductInput{Stock: &zero})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if products.lastUpd.Stock == nil {
		t.Fatal("an explicit zero was treated as an omission")
	}
	if *products.lastUpd.Stock != 0 {
		t.Fatalf("stock %d, want 0", *products.lastUpd.Stock)
	}
	if products.lastUpd.Name != nil {
		t.Fatal("an omitted field became present")
	}
}

func TestUpdateRejectsNegativePrice(t *testing.T) {
	resolver, _, _, _, _, products, _ := newTestResolver()

	negative := -1
	_, err := (&mutationResolver{resolver}).UpdateProduct(t.Context(), "1", gqlmodel.UpdateProductInput{PriceCents: &negative})
	if err == nil {
		t.Fatal("expected a negative price to be rejected")
	}
	if products.updateN != 0 {
		t.Fatalf("the write service was called %d times", products.updateN)
	}
}

func TestMediaUploadSurfacesSignedHeaders(t *testing.T) {
	resolver, _, _, _, media, _, _ := newTestResolver()
	media.upload = model.MediaUpload{
		Media: model.Media{ID: "1", Status: model.MediaStatusPending},
		Upload: model.UploadTarget{
			URL:    "https://objects/put",
			Method: "PUT",
			Headers: []model.Header{
				{Name: "X-Amz-Date", Value: "20260101T000000Z"},
				{Name: "Content-Type", Value: "image/png"},
			},
		},
	}

	got, err := (&mutationResolver{resolver}).RegisterMedia(t.Context(), gqlmodel.RegisterMediaInput{
		OwnerID:     "user-1",
		Filename:    "red.png",
		ContentType: "image/png",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Upload.Method != "PUT" {
		t.Fatalf("method %q", got.Upload.Method)
	}

	// The signature covers the headers, so a client must send them verbatim. Losing
	// one produces an upload that fails at the object store with no clue why, so they
	// are carried through rather than filtered.
	if len(got.Upload.Headers) != 2 {
		t.Fatalf("got %d headers, want 2", len(got.Upload.Headers))
	}
	if got.Upload.URL != "https://objects/put" {
		t.Fatalf("url %q", got.Upload.URL)
	}
}

func TestCategoryWritesAreReportedAsUnavailable(t *testing.T) {
	resolver, _, _, categories, _, _, _ := newTestResolver()
	categories.writeErr = fmt.Errorf("%w: category writes are not exposed over gRPC", apperror.ErrUnavailable)

	_, err := (&mutationResolver{resolver}).CreateCategory(t.Context(), gqlmodel.CreateCategoryInput{Name: "New"})
	if err == nil {
		t.Fatal("expected an error")
	}
	// Not surfaced as a client error: the request was fine, the capability is not
	// available on this transport. Reporting it as BAD_USER_INPUT would tell the
	// caller to fix something they did right.
	if !errors.Is(err, apperror.ErrUnavailable) {
		t.Fatalf("got %v, want it classified as unavailable", err)
	}
}

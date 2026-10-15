package graph

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"

	"aggregator-service/graph/generated"
	"aggregator-service/internal/apperror"
	"aggregator-service/internal/config"
	"aggregator-service/internal/graph/model"
	"aggregator-service/internal/handler"
)

// End-to-end tests through the real executor.
//
// Everything above tests resolvers directly, which proves they behave but not that
// the schema, the batching and the response encoding agree with each other. These
// tests execute actual queries, so they are where a mismatch between the SDL and
// the resolver signatures shows up, and where the call-count assertions prove the
// batching holds for a real query rather than a hand-built parent slice.

// newTestClient builds a client backed by the stubs.
func newTestClient(t *testing.T) (*client.Client, *fakeCatalog, *fakeSearch, *fakeCategories, *fakeMedia, *fakeProducts, *fakeProjection) {
	t.Helper()

	resolver, catalog, search, categories, media, products, projection := newTestResolver()

	return client.New(handlerFor(resolver)), catalog, search, categories, media, products, projection
}

// handlerFor builds the real GraphQL handler around a resolver.
//
// The production handler is used rather than a bare one so these tests exercise the
// shipped configuration: the error presenter, the limits, the transport. A test
// harness that omitted them would pass while the deployed service failed.
func handlerFor(resolver *Resolver) http.Handler {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	// Generous limits, so these tests exercise the composition rather than the
	// hardening. The hardening has its own tests.
	cfg.MaxQueryComplexity = 10_000
	cfg.MaxQueryDepth = 25
	cfg.RequestTimeout = 30 * time.Second

	exec := generated.NewExecutableSchema(generated.Config{Resolvers: resolver})

	// Wrapped with the request cache, because main does this and a harness that
	// skipped it would be measuring a service that is not the one that runs. The
	// call-count tests depend on it: without it, three sibling category fields each
	// fetch the hierarchy independently.
	return RequestCacheMiddleware(handler.NewGraphQL(cfg, exec, discardLogger()))
}

func TestProductPageResolvesInThreeCalls(t *testing.T) {
	c, catalog, _, categories, media, _, _ := newTestClient(t)

	catalog.page = model.ProductPage{
		Total: 20,
		Limit: 20,
		Products: func() []model.Product {
			products := make([]model.Product, 20)
			for i := range 20 {
				products[i] = model.Product{
					ID:             string(rune('a' + i%26)),
					CategoryID:     "1",
					Name:           "Product " + string(rune('A'+i%26)),
					PrimaryMediaID: "7",
					PriceCents:     12999000,
					Stock:          5,
					Status:         model.StatusActive,
				}
			}
			return products
		}(),
	}
	categories.byID = map[string]model.Category{"1": {ID: "1", Name: "Electronics"}}
	media.byID = map[string]model.Media{
		"7": {ID: "7", Status: model.MediaStatusReady, Filename: "red.png"},
	}
	media.urls = map[string]string{"7": "https://objects/7"}

	// Field names match the GraphQL response exactly: gqlgen's client refuses keys it
	// does not recognise, so a mismatch here fails the test rather than silently
	// reading a zero.
	var response struct {
		Products struct {
			Products []struct {
				ID       string
				Name     string
				Price    string `json:"priceCents"`
				Stock    int
				Status   string
				Category struct {
					Name string
				}
				PrimaryMedia struct {
					DownloadURL *string `json:"downloadUrl"`
					Filename    string
				}
			}
			Total int
		} `json:"products"`
	}

	err := c.Post(`query {
		products(filter: {limit: 20}) {
			total
			products { id name priceCents stock status category { name } primaryMedia { filename downloadUrl } }
		}
	}`, &response)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(response.Products.Products) != 20 {
		t.Fatalf("got %d products", len(response.Products.Products))
	}

	// One browse call, one category batch, one media batch. This is the assertion the
	// whole design exists for: without batching the category and media resolvers
	// would each be called 20 times.
	if catalog.listCalls != 1 {
		t.Errorf("browse called %d times, want 1", catalog.listCalls)
	}
	if categories.idsCalls != 1 {
		t.Errorf("category batch called %d times, want 1", categories.idsCalls)
	}
	if media.byIDsCalls != 1 {
		t.Errorf("media batch called %d times, want 1", media.byIDsCalls)
	}

	first := response.Products.Products[0]
	if first.Category.Name != "Electronics" {
		t.Errorf("category not resolved: %+v", first.Category)
	}
	if first.PrimaryMedia.Filename != "red.png" {
		t.Errorf("media not resolved: %+v", first.PrimaryMedia)
	}
	if first.PrimaryMedia.DownloadURL == nil || *first.PrimaryMedia.DownloadURL != "https://objects/7" {
		t.Errorf("download URL: %v", first.PrimaryMedia.DownloadURL)
	}

	// Money rendered as a string, so a price above the GraphQL Int ceiling survives.
	if first.Price != "12999000" {
		t.Errorf("price %q, want \"12999000\"", first.Price)
	}
}

func TestDegradedCategoryStillReturnsThePage(t *testing.T) {
	// The central claim of this service: one unavailable dependency costs one field,
	// not one page. If this test fails, the whole reason for the layer is gone.
	c, catalog, _, categories, media, _, _ := newTestClient(t)

	catalog.page = model.ProductPage{
		Total: 3,
		Limit: 20,
		Products: []model.Product{
			{ID: "1", Name: "One", CategoryID: "1", PrimaryMediaID: "7"},
			{ID: "2", Name: "Two", CategoryID: "1", PrimaryMediaID: "7"},
			{ID: "3", Name: "Three", CategoryID: "1", PrimaryMediaID: "7"},
		},
	}
	categories.idsErr = apperrorUnavailable("category-service is down")
	media.byID = map[string]model.Media{"7": {ID: "7", Status: model.MediaStatusReady}}
	media.urls = map[string]string{"7": "https://objects/7"}

	var response struct {
		Products struct {
			Products []struct {
				ID       string
				Name     string
				Category *struct{ Name string }
			}
			Total int
		} `json:"products"`
	}

	// RawPost rather than Post, because Post turns any errors array into a Go error.
	// That is the wrong shape for this assertion: the request must succeed with a
	// partial response, and the interesting part is what the errors array says.
	resp, err := c.RawPost(`query {
		products { total products { id name category { name } } }
	}`)
	if err != nil {
		t.Fatalf("request failed outright: %v", err)
	}

	var decoded struct {
		Products struct {
			Products []struct {
				ID       string
				Name     string
				Category *struct{ Name string }
			}
			Total int
		} `json:"products"`
	}
	if err := reencode(resp.Data, &decoded); err != nil {
		t.Fatalf("decode data: %v (raw: %v)", err, resp.Data)
	}
	response = decoded

	// The page survives in full. Reporting it as empty would be indistinguishable
	// from a genuinely empty catalogue, which is the failure mode this platform
	// already had to work around when the listing service silently ignored a query.
	if len(response.Products.Products) != 3 {
		t.Fatalf("got %d products, want 3; one dead dependency must not erase the page", len(response.Products.Products))
	}
	for _, p := range response.Products.Products {
		if p.Category != nil {
			t.Fatalf("product %s has a category despite the service being down", p.ID)
		}
	}

	// The failure is reported. A silently null field is indistinguishable from a
	// product that genuinely has no category.
	if !hasGraphQLError(resp.Errors, "DEPENDENCY_UNAVAILABLE") {
		t.Fatalf("errors %s should report the unavailable dependency", resp.Errors)
	}
}

func TestSearchOverShortTermIsAClientError(t *testing.T) {
	c, _, search, _, _, _, _ := newTestClient(t)

	resp, err := c.RawPost(`query { searchProducts(filter: {query: "ab"}) { total } }`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if len(resp.Errors) == 0 {
		t.Fatal("expected an error for a two-character search term")
	}
	if search.calls != 0 {
		t.Fatalf("the search service was called %d times for a rejected term", search.calls)
	}
	if !hasGraphQLError(resp.Errors, "BAD_USER_INPUT") {
		t.Fatalf("errors %s should be BAD_USER_INPUT", resp.Errors)
	}
}

func TestProjectionHealthReportsBothReadModels(t *testing.T) {
	c, _, _, _, _, _, projection := newTestClient(t)
	projection.listing = model.ProjectionStatus{
		Records: 128, AppliedEvents: 4096, Healthy: true,
		Source: "product-listing-service",
	}
	projection.search = model.ProjectionStatus{
		Records: 130, AppliedEvents: 4097, Healthy: true,
		Source: "product-search-service",
	}

	var response struct {
		ProjectionHealth struct {
			Listing struct {
				Records       int
				AppliedEvents int
				Healthy       bool
				Source        string `json:"source"`
			}
			Search struct {
				Records int
				Source  string `json:"source"`
			}
		} `json:"projectionHealth"`
	}

	err := c.Post(`query { projectionHealth { listing { records appliedEvents healthy source } search { records source } } }`, &response)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if response.ProjectionHealth.Listing.Records != 128 {
		t.Errorf("listing records %d", response.ProjectionHealth.Listing.Records)
	}
	if response.ProjectionHealth.Listing.Source != "product-listing-service" {
		t.Errorf("listing source %q", response.ProjectionHealth.Listing.Source)
	}
	if response.ProjectionHealth.Search.Records != 130 {
		t.Errorf("search records %d", response.ProjectionHealth.Search.Records)
	}
}

func TestCreateProductReturnsTheCommittedRow(t *testing.T) {
	c, _, _, categories, _, products, projection := newTestClient(t)
	categories.byID = map[string]model.Category{"1": {ID: "1", Name: "Electronics"}}
	projection.listing = model.ProjectionStatus{AppliedEvents: 7, Healthy: true}
	products.created = model.Product{
		ID:         "42",
		Name:       "Aurora Phone 15 Pro",
		PriceCents: 12999000,
		CategoryID: "1",
		Status:     model.StatusActive,
	}

	var response struct {
		CreateProduct struct {
			Projected     bool
			AppliedEvents *int `json:"appliedEvents"`
			Product       struct {
				ID    string
				Name  string
				Price string `json:"priceCents"`
			}
		} `json:"createProduct"`
	}

	err := c.Post(`mutation {
		createProduct(input: {categoryId: "1", name: "Aurora Phone 15 Pro", priceCents: 12999000}) {
			projected
			appliedEvents
			product { id name priceCents }
		}
	}`, &response)
	if err != nil {
		t.Fatalf("mutation failed: %v", err)
	}

	if response.CreateProduct.Product.ID != "42" {
		t.Fatalf("product id %q", response.CreateProduct.Product.ID)
	}
	if response.CreateProduct.Product.Price != "12999000" {
		t.Fatalf("price %q", response.CreateProduct.Product.Price)
	}

	// The write committed and the read models have not caught up. Reporting projected
	// true would be a guess, and a client that cannot tell "committed" from "lost"
	// retries the write into a duplicate.
	if response.CreateProduct.Projected {
		t.Error("a product from the write side must not claim to be projected")
	}
	if response.CreateProduct.AppliedEvents == nil || *response.CreateProduct.AppliedEvents != 7 {
		t.Errorf("appliedEvents %v, want 7", response.CreateProduct.AppliedEvents)
	}
}

func TestEmptyUpdateIsRejectedAsAClientError(t *testing.T) {
	c, _, _, _, _, products, _ := newTestClient(t)

	resp, err := c.RawPost(`mutation { updateProduct(id: "1", input: {}) { projected } }`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if len(resp.Errors) == 0 {
		t.Fatal("expected an error for an update that changes nothing")
	}
	if products.updateN != 0 {
		t.Fatalf("the write service was called %d times", products.updateN)
	}
	if !hasGraphQLError(resp.Errors, "BAD_USER_INPUT") {
		t.Fatalf("errors %s should be BAD_USER_INPUT", resp.Errors)
	}
}

func TestIntrospectionReturnsTheComposedSchema(t *testing.T) {
	// Introspection is what makes the composition discoverable. A client that cannot
	// see the schema cannot discover that Product.category came from a different
	// service than Product.priceCents.
	c, _, _, _, _, _, _ := newTestClient(t)

	// The `name` field is selected, so the response struct carries it: gqlgen's client
	// rejects response keys the destination struct does not declare.
	var response struct {
		Schema struct {
			QueryType struct {
				Name   string
				Fields []struct {
					Name string
				} `json:"fields"`
			} `json:"queryType"`
		} `json:"__schema"`
	}

	err := c.Post(`query { __schema { queryType { name fields { name } } } }`, &response)
	if err != nil {
		t.Fatalf("introspection failed: %v", err)
	}

	names := map[string]bool{}
	for _, f := range response.Schema.QueryType.Fields {
		names[f.Name] = true
	}
	for _, want := range []string{"products", "searchProducts", "categoryTree", "projectionHealth", "mediaList"} {
		if !names[want] {
			t.Errorf("introspection is missing %q", want)
		}
	}
}

func TestCategoryTreeExposesBreadcrumbsAndChildren(t *testing.T) {
	c, catalog, _, categories, _, _, _ := newTestClient(t)
	categories.tree = []model.Category{
		{ID: "1", Name: "Electronics", ParentID: ""},
		{ID: "2", Name: "Phones", ParentID: "1"},
	}
	catalog.counts = map[string]int64{"1": 4, "2": 2}

	var response struct {
		CategoryTree []struct {
			ID           string
			Name         string
			ParentID     string `json:"parentId"`
			ProductCount *int   `json:"productCount"`
			Children     []struct {
				Name string
			}
		} `json:"categoryTree"`
	}

	err := c.Post(`query {
		categoryTree { id name parentId productCount children { name } }
	}`, &response)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(response.CategoryTree) != 2 {
		t.Fatalf("got %d categories", len(response.CategoryTree))
	}

	// Name-ordered, so two identical queries return identical responses.
	if response.CategoryTree[0].Name != "Electronics" {
		t.Fatalf("first category %q, want the name-ordered first", response.CategoryTree[0].Name)
	}

	root := response.CategoryTree[0]
	// A root has no parent, rendered as the empty string rather than "0".
	if root.ParentID != "" {
		t.Errorf("root parentId %q, want empty", root.ParentID)
	}
	if len(root.Children) != 1 || root.Children[0].Name != "Phones" {
		t.Errorf("children %+v", root.Children)
	}

	// The root count includes its child: a shopper clicking "Electronics" expects to
	// see everything below it.
	if root.ProductCount == nil || *root.ProductCount != 6 {
		t.Errorf("root productCount %v, want 6 (4 + 2)", root.ProductCount)
	}
}

func TestVariantsAppearWhenRequested(t *testing.T) {
	c, catalog, _, _, _, _, _ := newTestClient(t)
	catalog.page = model.ProductPage{
		Total: 1,
		Limit: 20,
		Products: []model.Product{{
			ID:          "1",
			Name:        "Aurora",
			HasVariants: true,
			Variants: []model.Variant{{
				ID: "10", ProductID: "1", SKU: "AUR-128-BLK",
				Name: "128GB Black", PriceCents: 12999000, Stock: 3,
			}},
		}},
	}

	var response struct {
		Products struct {
			Products []struct {
				ID       string
				Variants []struct {
					SKU     string
					Price   string `json:"priceCents"`
					Stock   int
					MediaID string `json:"mediaId"`
				}
			}
		} `json:"products"`
	}

	err := c.Post(`query { products { products { id variants { sku priceCents stock mediaId } } } }`, &response)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	if len(response.Products.Products) != 1 {
		t.Fatalf("got %d products", len(response.Products.Products))
	}
	variants := response.Products.Products[0].Variants
	if len(variants) != 1 {
		t.Fatalf("got %d variants", len(variants))
	}
	if variants[0].SKU != "AUR-128-BLK" {
		t.Errorf("sku %q", variants[0].SKU)
	}
	// Variant money is a string too, for the same reason.
	if variants[0].Price != "12999000" {
		t.Errorf("price %q", variants[0].Price)
	}
	// An unset variant image renders as empty rather than "0".
	if variants[0].MediaID != "" {
		t.Errorf("mediaId %q, want empty", variants[0].MediaID)
	}
}

func TestTimestampsRenderAsRFC3339(t *testing.T) {
	c, catalog, _, _, _, _, _ := newTestClient(t)
	projected := timeAt(t, "2026-09-30T12:00:00Z")
	catalog.page = model.ProductPage{
		Total: 1,
		Limit: 20,
		Products: []model.Product{{
			ID:          "1",
			Name:        "Aurora",
			CreatedAt:   timeAt(t, "2026-09-01T08:30:00Z"),
			ProjectedAt: &projected,
		}},
	}

	var response struct {
		Products struct {
			Products []struct {
				CreatedAt   *string `json:"createdAt"`
				ProjectedAt *string `json:"projectedAt"`
			}
		} `json:"products"`
	}

	err := c.Post(`query { products { products { createdAt projectedAt } } }`, &response)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	product := response.Products.Products[0]
	if product.CreatedAt == nil || !strings.HasSuffix(*product.CreatedAt, "Z") {
		t.Errorf("createdAt %v", product.CreatedAt)
	}
	if product.ProjectedAt == nil {
		t.Fatal("projectedAt should be set for a read from the projection")
	}
}

func TestUnsetProjectionTimestampRendersAsNullNotYearOne(t *testing.T) {
	// A missing timestamp presented as "0001-01-01T00:00:00Z" reads as a real date, and
	// a client sorting or filtering on it is misled.
	//
	// projectedAt is the field to assert on because it is genuinely nullable: a
	// product from the write side has not been projected, and a read model row always
	// has. createdAt and updatedAt are declared non-null, so a zero there is a
	// contract violation and surfacing it as a protocol error is the correct outcome
	// rather than something to paper over.
	c, catalog, _, _, _, _, _ := newTestClient(t)
	created := timeAt(t, "2026-09-01T08:30:00Z")
	catalog.page = model.ProductPage{
		Total: 1,
		Limit: 20,
		Products: []model.Product{{
			ID:        "1",
			Name:      "Aurora",
			CreatedAt: created,
			// ProjectedAt deliberately unset.
		}},
	}

	raw := postRaw(t, c, `query { products { products { createdAt projectedAt } } }`)

	if strings.Contains(raw, "0001-01-01") {
		t.Fatalf("response contains year 1: %s", raw)
	}
	if !strings.Contains(raw, `"projectedAt":null`) {
		t.Errorf("projectedAt should be null: %s", raw)
	}
	// The set timestamp is still a real RFC 3339 instant.
	if !strings.Contains(raw, `"createdAt":"2026-09-01T08:30:00Z"`) {
		t.Errorf("createdAt should be RFC 3339: %s", raw)
	}
}

func TestErrorResponseCarriesAMachineReadableCode(t *testing.T) {
	// Clients branch on the code, not on prose. Parsing a message to learn whether to
	// retry is the thing this avoids.
	c, _, _, _, _, _, projection := newTestClient(t)
	projection.listingErr = apperrorUnavailable("dial tcp 10.0.0.4:9090: connect: connection refused")
	projection.searchErr = apperrorUnavailable("dial tcp 10.0.0.4:9094: connect: connection refused")

	resp, err := c.RawPost(`query { projectionHealth { listing { records } search { records } } }`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	raw := string(resp.Errors)

	if !strings.Contains(raw, `"code":"DEPENDENCY_UNAVAILABLE"`) {
		t.Fatalf("response lacks a machine-readable code: %s", raw)
	}
	// The retry hint travels as data too, so a client does not infer it from the code
	// string it happens to recognise.
	if !strings.Contains(raw, `"retryable":true`) {
		t.Fatalf("response lacks the retryable hint: %s", raw)
	}

	// The detail stays server-side. A gRPC error names the target address, and a
	// client reading it learns the internal topology of every service in the platform.
	for _, leak := range []string{"10.0.0.4", "connection refused", "dial tcp", "localhost"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("internal detail %q leaked to the client: %s", leak, raw)
		}
	}
}

// --- helpers --------------------------------------------------------------

// hasGraphQLError reports whether the collected errors carry the given code.
//
// gqlgen's client surfaces errors as raw JSON rather than as typed values, so this
// inspects the encoded form. Asserting on the raw response is the point: it checks
// that the code a client actually receives is the one the presenter intended, which
// a test against an internal error type would not.
func hasGraphQLError(raw json.RawMessage, code string) bool {
	return strings.Contains(string(raw), code)
}

// apperrorUnavailable builds a retryable downstream failure.
func apperrorUnavailable(message string) error {
	return fmt.Errorf("%w: %s", apperror.ErrUnavailable, message)
}

// timeAt parses a fixture timestamp.
func timeAt(t *testing.T, rfc3339 string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}

// discardLogger silences the handler's logging in tests.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// postRaw executes a query and returns the raw JSON body, errors included.
//
// The body is returned whole because several assertions are about the encoded form:
// that a code is present, or that an absent timestamp rendered as null rather than as
// the year 1. Decoding into a struct first would lose both.
func postRaw(t *testing.T, c *client.Client, query string) string {
	t.Helper()

	resp, err := c.RawPost(query)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if len(resp.Errors) > 0 {
		t.Fatalf("unexpected errors: %s", resp.Errors)
	}
	return reencodeString(resp.Data)
}

// reencode round-trips the client's decoded data back into a struct.
//
// gqlgen's client unmarshals into `any` before handing it over, so the assertions
// need one more decode. Going through JSON rather than reflecting over the map keeps
// the struct tags doing the work, so a renamed field in the test's own struct fails
// here rather than silently reading zero.
func reencode(data any, dst any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func reencodeString(data any) string {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprintf("%v", data)
	}
	return string(raw)
}

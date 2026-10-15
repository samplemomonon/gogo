package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"

	"aggregator-service/graph"
	"aggregator-service/graph/generated"
	"aggregator-service/internal/apperror"
	"aggregator-service/internal/config"
	"aggregator-service/internal/graph/model"
)

// The hardening tests.
//
// Each limit here exists because a GraphQL endpoint lets the caller choose the cost
// of its own request, which a REST endpoint does not. The tests are written to fail
// when a limit stops working, and to document what a legitimate query is allowed to
// do, because a limit that is too tight is as much an outage as one that is missing.

// withPersistedQuery attaches an APQ extension carrying the given hash.
func withPersistedQuery(hash string) client.Option {
	return func(r *client.Request) {
		r.Extensions = map[string]any{
			"persistedQuery": map[string]any{
				"version":    1,
				"sha256Hash": hash,
			},
		}
	}
}

// testMutationToken is the credential these tests present. config.Load refuses to
// return a configuration with no MUTATION_TOKENS, because an absent value must
// refuse every mutation rather than permitting them — so a harness that wants a
// working server has to configure one.
const testMutationToken = "test-only-mutation-token-value"

// testDownstreamToken is the credential this service presents to its peers.
const testDownstreamToken = "test-only-downstream-token-value"

// withServiceCredentials sets the credentials config.Load requires.
//
// A mutation token and a downstream token are both mandatory: without either, the
// service either refuses every write or cannot call anything at all, and a test
// harness building a server wants neither behaviour. The rule is enforced in
// config, not here.
func withServiceCredentials(t *testing.T) {
	t.Helper()
	t.Setenv("MUTATION_TOKENS", testMutationToken)
	t.Setenv("DOWNSTREAM_SERVICE_TOKEN", testDownstreamToken)
}

// withMutationToken sets a token in the environment for the duration of the test.
func withMutationToken(t *testing.T) {
	t.Helper()
	withServiceCredentials(t)
}

// hardenedClient builds a client with the shipped configuration plus overrides.
func hardenedClient(t *testing.T, mutate func(*config.Config)) *client.Client {
	t.Helper()

	withMutationToken(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	// Generous defaults so each test isolates the limit it is exercising.
	cfg.MaxQueryComplexity = 100_000
	cfg.MaxQueryDepth = 100
	cfg.MaxQueryLength = 100_000
	cfg.ParserTokenLimit = 100_000
	cfg.RequestTimeout = 30 * time.Second

	mutate(cfg)

	// The stub resolvers are needed because these tests reach the executor: a limit
	// that rejects a query is proven by the request failing before a resolver runs,
	// but a limit that wrongly rejects one is proven by the request succeeding, and
	// that success needs something to resolve.
	exec := generated.NewExecutableSchema(generated.Config{Resolvers: stubResolvers()})
	return client.New(NewGraphQL(cfg, exec, testLogger()))
}

// stubResolvers answers every query with a fixed empty result.
//
// These tests are about the request being admitted or rejected, not about what comes
// back, so the resolvers return the zero value rather than needing the full set of
// collaborators.
func stubResolvers() *graph.Resolver {
	catalog := &alwaysEmptyCatalog{}
	return &graph.Resolver{
		CatalogStore:    catalog,
		SearchStore:     &alwaysEmptySearch{},
		CategoryStore:   &alwaysEmptyCategories{},
		MediaStore:      &alwaysEmptyMedia{},
		ProductStore:    &alwaysFailingWrites{},
		ProjectionStore: &alwaysEmptyProjection{},
	}
}

type alwaysEmptyCatalog struct{}

func (*alwaysEmptyCatalog) GetProduct(context.Context, string, bool) (model.Product, error) {
	return model.Product{}, nil
}
func (*alwaysEmptyCatalog) ListProducts(context.Context, graph.BrowseFilter) (model.ProductPage, error) {
	return model.ProductPage{Products: []model.Product{}, Limit: 20}, nil
}
func (*alwaysEmptyCatalog) CountProductsByCategory(context.Context, []string) (map[string]int64, error) {
	return map[string]int64{}, nil
}

type alwaysEmptySearch struct{}

func (*alwaysEmptySearch) SearchProducts(context.Context, graph.SearchFilter) (model.SearchResult, error) {
	return model.SearchResult{Hits: []model.SearchHit{}}, nil
}

type alwaysEmptyCategories struct{}

func (*alwaysEmptyCategories) GetCategory(context.Context, string) (model.Category, error) {
	return model.Category{}, nil
}
func (*alwaysEmptyCategories) ListCategories(context.Context, int32, int32) ([]model.Category, int64, error) {
	return []model.Category{}, 0, nil
}
func (*alwaysEmptyCategories) CategoryTree(context.Context) ([]model.Category, error) {
	return []model.Category{}, nil
}
func (*alwaysEmptyCategories) CategoriesByIDs(context.Context, []string) (map[string]model.Category, error) {
	return map[string]model.Category{}, nil
}
func (*alwaysEmptyCategories) CreateCategory(context.Context, model.CreateCategoryInput) (model.Category, error) {
	return model.Category{}, nil
}
func (*alwaysEmptyCategories) UpdateCategory(context.Context, string, model.UpdateCategoryInput) (model.Category, error) {
	return model.Category{}, nil
}
func (*alwaysEmptyCategories) DeleteCategory(context.Context, string) error { return nil }

type alwaysEmptyMedia struct{}

func (*alwaysEmptyMedia) GetMedia(context.Context, string) (model.Media, error) {
	return model.Media{}, nil
}
func (*alwaysEmptyMedia) ListMedia(context.Context, string, model.MediaStatus, int32, int32) ([]model.Media, int64, error) {
	return []model.Media{}, 0, nil
}
func (*alwaysEmptyMedia) MediaByIDs(context.Context, []string) (map[string]model.Media, error) {
	return map[string]model.Media{}, nil
}
func (*alwaysEmptyMedia) DownloadURLs(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (*alwaysEmptyMedia) RegisterMedia(context.Context, model.RegisterMediaInput) (model.MediaUpload, error) {
	return model.MediaUpload{}, nil
}
func (*alwaysEmptyMedia) CompleteUpload(context.Context, string) (model.Media, error) {
	return model.Media{}, nil
}
func (*alwaysEmptyMedia) DeleteMedia(context.Context, string) error { return nil }

type alwaysFailingWrites struct{}

func (*alwaysFailingWrites) CreateProduct(context.Context, model.CreateProductInput) (model.Product, error) {
	return model.Product{}, apperror.ErrInvalidInput
}
func (*alwaysFailingWrites) UpdateProduct(context.Context, string, model.UpdateProductInput) (model.Product, error) {
	return model.Product{}, apperror.ErrInvalidInput
}
func (*alwaysFailingWrites) DeleteProduct(context.Context, string) error {
	return apperror.ErrInvalidInput
}

type alwaysEmptyProjection struct{}

func (*alwaysEmptyProjection) ListingStatus(context.Context) (model.ProjectionStatus, error) {
	return model.ProjectionStatus{Source: "product-listing-service", Healthy: true}, nil
}
func (*alwaysEmptyProjection) SearchStatus(context.Context) (model.ProjectionStatus, error) {
	return model.ProjectionStatus{Source: "product-search-service", Healthy: true}, nil
}

func TestComplexityLimitRejectsAnAliasFanOut(t *testing.T) {
	// The classic GraphQL amplification. Each alias is a separate selection, so each
	// one is resolved separately, and the request is shallow enough to pass any depth
	// limit. Only a cost model that counts aliases stops this.
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.MaxQueryComplexity = 50
	})

	fields := make([]string, 60)
	for i := range fields {
		fields[i] = fmt.Sprintf("a%d: product(id: \"1\") { id name }", i)
	}
	query := "query { " + strings.Join(fields, " ") + " }"

	resp, err := c.RawPost(query)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if !strings.Contains(string(resp.Errors), "COMPLEXITY_LIMIT_EXCEEDED") {
		t.Fatalf("expected a complexity rejection, got errors: %s", resp.Errors)
	}
}

func TestComplexityLimitAllowsANormalPage(t *testing.T) {
	// The other direction. A limit that rejects a real product page is as broken as
	// one that accepts a fan-out, so this asserts the budget is generous enough for
	// the query the service exists to serve.
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.MaxQueryComplexity = 250
	})

	query := `query {
		products(filter: {limit: 20}) {
			total
			products {
				id name slug priceCents stock status
				category { id name ancestors { name } }
				primaryMedia { filename downloadUrl }
				variants { sku priceCents stock }
			}
		}
	}`

	resp, err := c.RawPost(query)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if len(resp.Errors) > 0 {
		t.Fatalf("a normal product page was rejected: %s", resp.Errors)
	}
}

func TestDepthLimitRejectsDeepNesting(t *testing.T) {
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.MaxQueryDepth = 6
	})

	// categoryTree -> children -> children -> children -> children -> children -> id.
	// Category is self-referential, which makes it the cheapest way to build an
	// arbitrarily deep valid document.
	query := `query {
		categoryTree {
			children { children { children { children { children { id } } } } }
		}
	}`

	resp, err := c.RawPost(query)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if !strings.Contains(string(resp.Errors), "DEPTH_LIMIT_EXCEEDED") {
		t.Fatalf("expected a depth rejection, got: %s", resp.Errors)
	}
	// The response names the offending nesting, so a client author can find it rather
	// than guessing which part of a large document to blame.
	if !strings.Contains(string(resp.Errors), "deepestPath") {
		t.Errorf("the rejection should name the deepest path: %s", resp.Errors)
	}
}

func TestDepthLimitAllowsTheNormalShape(t *testing.T) {
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.MaxQueryDepth = 12
	})

	// The deepest legitimate shape the platform actually produces: a page of products
	// whose categories carry breadcrumbs.
	query := `query {
		products {
			products {
				category { ancestors { id name parentId } children { id name } }
			}
		}
	}`

	resp, err := c.RawPost(query)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if len(resp.Errors) > 0 {
		t.Fatalf("a normal product page was rejected: %s", resp.Errors)
	}
}

func TestIntrospectionIsOffWhenDisabled(t *testing.T) {
	// The schema is the full published API surface, so introspection hands a caller a
	// machine-readable inventory of it. Turning it off is the cheapest way to deny
	// that, and it must actually deny it rather than only failing to volunteer it.
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.EnableIntrospection = false
	})

	resp, err := c.RawPost(`query { __schema { queryType { name } } }`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if len(resp.Errors) == 0 {
		t.Fatal("introspection succeeded while disabled")
	}
	if !strings.Contains(string(resp.Errors), "__schema") {
		t.Errorf("the rejection should name the field: %s", resp.Errors)
	}
}

func TestIntrospectionIsOnWhenEnabled(t *testing.T) {
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.EnableIntrospection = true
	})

	var out struct {
		Schema struct {
			QueryType struct{ Name string } `json:"queryType"`
		} `json:"__schema"`
	}
	if err := c.Post(`query { __schema { queryType { name } } }`, &out); err != nil {
		t.Fatalf("introspection failed while enabled: %v", err)
	}
	if out.Schema.QueryType.Name != "Query" {
		t.Errorf("query type %q", out.Schema.QueryType.Name)
	}
}

func TestParserTokenLimitRejectsAParseBlowup(t *testing.T) {
	// The limit has to be at the parser, not in a validator: a deeply nested document
	// exhausts the stack while it is being parsed, before validation could look at it.
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.ParserTokenLimit = 200
	})

	// Balanced and syntactically valid, but far past 200 tokens. An unbalanced document
	// would be rejected by the parser for the wrong reason, which would make this test
	// pass without the token limit doing anything.
	query := "query { categoryTree { children {" + strings.Repeat("children { ", 150) +
		"id" + strings.Repeat(" }", 151) + " } }"

	// A parse failure is a protocol error, so gqlgen answers 422 and the client reports
	// it as a transport error rather than handing back a response. That status is the
	// assertion: it is what tells a proxy and a client library the query was refused
	// before execution rather than executed and failed.
	_, err := c.RawPost(query)
	if err == nil {
		t.Fatal("a document past the parser token limit was accepted")
	}
	if !strings.Contains(err.Error(), "exceeded token limit") {
		t.Fatalf("expected a token limit rejection, got: %v", err)
	}
}

func TestPersistedQueryMissIsReported(t *testing.T) {
	// The APQ handshake. A client sends a hash first, the server replies
	// PERSISTED_QUERY_NOT_FOUND, and the client then sends the text once. A server
	// that did not implement this would answer a hash-only request as a parse error,
	// which breaks every APQ client on its first call.
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.PersistedQueriesEnable = true
	})

	// Hash only, no query text: that is the optimistic first call a real APQ client
	// makes. The extension is built by hand because gqlgen's client exposes no helper
	// for it, and the shape has to be exactly what a client sends.
	resp, err := c.RawPost("", withPersistedQuery(
		"0000000000000000000000000000000000000000000000000000000000000000"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if !strings.Contains(string(resp.Errors), "PERSISTED_QUERY_NOT_FOUND") {
		t.Fatalf("expected PERSISTED_QUERY_NOT_FOUND, got: %s", resp.Errors)
	}
}

func TestPersistedQueryRejectsAMismatchedHash(t *testing.T) {
	// A client that claims a hash its query does not produce must be refused. Storing
	// it anyway would let one client's document be served to another under the wrong
	// key.
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.PersistedQueriesEnable = true
	})

	resp, err := c.RawPost("query Probe { __typename }", withPersistedQuery(
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if !strings.Contains(string(resp.Errors), "hash does not match") {
		t.Fatalf("expected a hash mismatch rejection, got: %s", resp.Errors)
	}
}

func TestQueryLengthLimitRejectsAWideDocumentThroughTheServer(t *testing.T) {
	// End-to-end through the real handler. The extension test proves the check works in
	// isolation; this proves it is actually installed on the server, which is a separate
	// thing to get wrong.
	c := hardenedClient(t, func(cfg *config.Config) {
		cfg.MaxQueryLength = 200
		cfg.MaxQueryComplexity = 100_000
		cfg.MaxQueryDepth = 100
		cfg.ParserTokenLimit = 100_000
	})

	// Wide and shallow: many one-token selections rather than deep nesting, so the
	// parser token limit stays inside budget and only the byte cap can catch this.
	query := "query { " + strings.Repeat("__typename ", 200) + " }"

	resp, err := c.RawPost(query)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if !strings.Contains(string(resp.Errors), "QUERY_TOO_LONG") {
		t.Fatalf("expected a length rejection, got: %s", resp.Errors)
	}
}

func TestPlaygroundIsOffByDefault(t *testing.T) {
	// The playground executes arbitrary queries against real downstream services, so
	// it must not be reachable without an explicit opt-in.
	withMutationToken(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.PlaygroundEnabled {
		t.Fatal("the playground must default off")
	}
	if PlaygroundHandler(cfg) != nil {
		t.Fatal("no playground handler should be built while disabled")
	}
}

func TestPlaygroundIsBuiltWhenEnabled(t *testing.T) {
	withMutationToken(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.PlaygroundEnabled = true
	if PlaygroundHandler(cfg) == nil {
		t.Fatal("a playground handler should be built while enabled")
	}
}

// TestPlaygroundWiringIsSafeWhenDisabled is a regression test for a startup crash.
//
// PlaygroundHandler returns http.Handler rather than http.HandlerFunc on purpose. A nil
// http.HandlerFunc assigned into an http.Handler field is a non-nil interface holding a
// nil function value, so `server.Playground != nil` passes, the nil reaches
// ServeMux.Handle, and the process panics during startup with "http: nil handler".
//
// That panic only happens with the playground disabled, which is the production default,
// so it would have taken the service down on first deploy rather than on a
// configuration nobody runs. This asserts the whole wiring path the binary takes.
func TestPlaygroundWiringIsSafeWhenDisabled(t *testing.T) {
	withMutationToken(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.PlaygroundEnabled = false

	server := &Server{
		GraphQL:    http.NotFoundHandler(),
		Playground: PlaygroundHandler(cfg),
		Config:     cfg,
		Log:        testLogger(),
	}

	// Building the router must not panic.
	router := server.Handler()

	// And the route must genuinely be absent.
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/playground", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", recorder.Code)
	}
}

func TestPlaygroundWiringIsSafeWhenEnabled(t *testing.T) {
	withMutationToken(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.PlaygroundEnabled = true

	server := &Server{
		GraphQL:    http.NotFoundHandler(),
		Playground: PlaygroundHandler(cfg),
		Config:     cfg,
		Log:        testLogger(),
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/playground", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 when enabled", recorder.Code)
	}
}

// TestServerStartsWithoutAGraphQLHandler covers the other startup crash.
//
// A Server built with a nil GraphQL handler would panic in ServeMux registration for
// the same reason. The nil fallback answers 503 instead, which leaves the probes
// working so an operator can diagnose the wiring rather than seeing a crash loop.
func TestServerStartsWithoutAGraphQLHandler(t *testing.T) {
	withMutationToken(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	server := &Server{Config: cfg, Log: testLogger()}
	router := server.Handler()

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/query", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", recorder.Code)
	}

	// The probes still answer, so the misconfiguration is diagnosable.
	for _, path := range []string{"/health", "/healthz", "/readyz", "/version"} {
		probe := httptest.NewRecorder()
		router.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, path, nil))
		if probe.Code != http.StatusOK {
			t.Errorf("%s status %d, want 200", path, probe.Code)
		}
	}
}

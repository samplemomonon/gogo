package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aggregator-service/graph/generated"
	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"

	"aggregator-service/internal/config"
	"aggregator-service/internal/graph/model"
)

// The mutation authentication tests.
//
// The property under test is not "an error came back". It is that **the write did
// not happen**. Every rejection test therefore asserts on a counter in the stub
// store as well as on the response, because an authenticated-looking error
// returned after the write already landed would pass a status-code check and fail
// the only one that matters.

const validToken = "correct-horse-battery-staple-token"

const otherValidToken = "second-rotation-token-value"

// authTestServer builds a real gqlgen handler over counting stubs.
//
// A real handler rather than a middleware unit test, because the decision this
// guards is made from the operation gqlgen resolved. A test that called the
// middleware with a hand-built context would pass while the registration in
// NewGraphQL was wrong.
func authTestServer(t *testing.T, tokens []string) (http.Handler, *countingStore) {
	t.Helper()

	// The environment is set rather than the field, because config.Load validates:
	// the token rule is enforced at load time, which is the earliest point at which
	// a deployment that forgot it can be refused.
	t.Setenv("MUTATION_TOKENS", strings.Join(tokens, ","))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.MaxQueryComplexity = 100_000
	cfg.MaxQueryDepth = 100
	cfg.MaxQueryLength = 100_000
	cfg.ParserTokenLimit = 100_000
	cfg.RequestTimeout = 30 * time.Second

	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config must be valid: %v", err)
	}

	store := &countingStore{}
	resolvers := stubResolvers()
	resolvers.ProductStore = store

	exec := generated.NewExecutableSchema(generated.Config{Resolvers: resolvers})
	return NewGraphQL(cfg, exec, testLogger()), store
}

// post sends a GraphQL document and returns the status and the parsed envelope.
func post(t *testing.T, handler http.Handler, query string, authHeader string) (int, map[string]any) {
	t.Helper()

	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var envelope map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, envelope
}

// createProduct is the write used throughout: it goes through ProductStore, whose
// stub counts, and it is the mutation a real caller would reach for first.
const createProductMutation = `mutation { createProduct(input: {categoryId: "1", name: "X", priceCents: 1, stock: 1, status: ACTIVE}) { projected } }`

// countingStore records whether any write reached the domain layer.
type countingStore struct {
	writes atomic.Int64
}

func (s *countingStore) CreateProduct(context.Context, model.CreateProductInput) (model.Product, error) {
	s.writes.Add(1)
	return model.Product{ID: "created"}, nil
}

func (s *countingStore) UpdateProduct(context.Context, string, model.UpdateProductInput) (model.Product, error) {
	s.writes.Add(1)
	return model.Product{}, nil
}

func (s *countingStore) DeleteProduct(context.Context, string) error {
	s.writes.Add(1)
	return nil
}

// --- the rejections -------------------------------------------------------

func TestMutationWithoutATokenIsRefusedAndDoesNotWrite(t *testing.T) {
	handler, store := authTestServer(t, []string{validToken})

	code, envelope := post(t, handler, createProductMutation, "")

	if got := store.writes.Load(); got != 0 {
		t.Fatalf("the write reached the domain layer %d time(s); authentication is a "+
			"control that must stop the write, not merely report on it afterwards", got)
	}
	if code != http.StatusOK && code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 200-with-errors or 401", code)
	}
	if code := errorCode(t, envelope); code != "UNAUTHENTICATED" {
		t.Errorf("error code = %q, want UNAUTHENTICATED — a client cannot react to an "+
			"unrecognised code", code)
	}
}

func TestMutationWithTheWrongTokenIsRefusedAndDoesNotWrite(t *testing.T) {
	handler, store := authTestServer(t, []string{validToken})

	for name, header := range map[string]string{
		"wrong token":       "Bearer not-the-right-token-value-at-all",
		"empty bearer":      "Bearer ",
		"basic scheme":      "Basic " + validToken,
		"bare token":        validToken,
		"token with space":  "Bearer " + validToken + " extra",
		"scheme lowercased": "bearer " + validToken,
	} {
		t.Run(name, func(t *testing.T) {
			store.writes.Store(0)
			_, envelope := post(t, handler, createProductMutation, header)

			if got := store.writes.Load(); got != 0 {
				t.Fatalf("write reached the domain layer with header %q", header)
			}
			if code := errorCode(t, envelope); code != "UNAUTHENTICATED" {
				t.Errorf("error code = %q, want UNAUTHENTICATED", code)
			}
		})
	}
}

// A missing token and a wrong token must be indistinguishable from outside.
// Distinguishing them would tell an attacker which guesses are worth continuing.
func TestMissingAndWrongTokenAreIndistinguishable(t *testing.T) {
	handler, _ := authTestServer(t, []string{validToken})

	_, missing := post(t, handler, createProductMutation, "")
	_, wrong := post(t, handler, createProductMutation, "Bearer wrong-entirely-value-here")

	missingErr := firstError(t, missing)
	wrongErr := firstError(t, wrong)

	if missingErr != wrongErr {
		t.Errorf("an absent token and an invalid token produced different client-visible "+
			"errors:\n  absent:  %s\n  invalid: %s", missingErr, wrongErr)
	}
}

// The token must not reach the client in any form.
func TestTheTokenIsNeverEchoed(t *testing.T) {
	handler, _ := authTestServer(t, []string{validToken})

	req := httptest.NewRequest(http.MethodPost, "/query",
		strings.NewReader(fmt.Sprintf(`{"query":%q}`, createProductMutation)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+validToken)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), validToken) {
		t.Errorf("the response body contains the configured token:\n%s", rec.Body.String())
	}
	// No assertion on WWW-Authenticate: the challenge header belongs to a 401, and
	// this endpoint answers 200 with an errors array because that is the shape a
	// GraphQL client already parses. The machine-readable signal a caller acts on is
	// the UNAUTHENTICATED code in the error extensions.
}

// --- the acceptances ------------------------------------------------------

func TestMutationWithAValidTokenSucceeds(t *testing.T) {
	handler, store := authTestServer(t, []string{validToken})

	_, envelope := post(t, handler, createProductMutation, "Bearer "+validToken)

	if got := store.writes.Load(); got != 1 {
		t.Errorf("the write reached the domain layer %d time(s), want 1", got)
	}
	if _, failed := envelope["errors"]; failed {
		t.Errorf("a valid token was refused: %v", envelope["errors"])
	}
}

// Rotation is why the token set is a set.
func TestAnyConfiguredTokenIsAccepted(t *testing.T) {
	handler, store := authTestServer(t, []string{validToken, otherValidToken})

	for _, token := range []string{validToken, otherValidToken} {
		store.writes.Store(0)
		_, envelope := post(t, handler, createProductMutation, "Bearer "+token)

		if got := store.writes.Load(); got != 1 {
			t.Errorf("token %q: write count = %d, want 1", token, got)
		}
		if _, failed := envelope["errors"]; failed {
			t.Errorf("token %q was refused: %v", token, envelope["errors"])
		}
	}
}

// --- reads stay public ----------------------------------------------------

// The asymmetry is the design. A catalogue that required a credential to browse
// would impose a cost on every anonymous visitor to defend nothing.
func TestQueriesRemainPublic(t *testing.T) {
	handler, _ := authTestServer(t, []string{validToken})

	_, envelope := post(t, handler, `{ projectionHealth { listing { records } } }`, "")
	if _, failed := envelope["errors"]; failed {
		t.Errorf("an unauthenticated read was refused: %v", envelope["errors"])
	}
}

func TestIntrospectionRemainsPublic(t *testing.T) {
	handler, _ := authTestServer(t, []string{validToken})

	_, envelope := post(t, handler, `{ __schema { mutationType { name } } }`, "")
	if _, failed := envelope["errors"]; failed {
		t.Errorf("an unauthenticated introspection query was refused: %v", envelope["errors"])
	}
}

// Introspection lists the mutation fields. A client must be able to discover what
// it needs a token for, so the mutation root stays visible without one.
func TestTheMutationRootIsDiscoverableWithoutAToken(t *testing.T) {
	handler, _ := authTestServer(t, []string{validToken})

	_, envelope := post(t, handler, `{ __schema { mutationType { name fields { name } } } }`, "")

	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data in response: %v", envelope)
	}
	if data["__schema"] == nil {
		t.Fatalf("no schema returned: %v", envelope)
	}
}

// --- fail closed ----------------------------------------------------------

// The empty-token-set case cannot be reached through config.Load, which refuses to
// return such a configuration. That refusal is the real defence and it is tested in
// the config package; what is asserted here is the defence in depth beneath it, so a
// Server constructed directly — as every test in this package does — is not
// accidentally permissive.
func TestAnEmptyTokenSetRefusesAMutationAtTheMiddleware(t *testing.T) {
	log := testLogger()
	tokens := NewTokenSet(nil)
	if tokens.Len() != 0 {
		t.Fatalf("Len = %d, want 0", tokens.Len())
	}

	op := &ast.OperationDefinition{Operation: ast.Mutation, Name: "createProduct"}
	ctx := graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		Operation: op,
		Headers:   http.Header{"Authorization": []string{"Bearer " + validToken}},
	})

	called := false
	next := func(ctx context.Context) graphql.ResponseHandler {
		called = true
		return func(ctx context.Context) *graphql.Response { return &graphql.Response{} }
	}

	resp := RequireMutationToken(tokens, log)(ctx, next)(ctx)

	if called {
		t.Error("the mutation handler ran with an empty token set")
	}
	if len(resp.Errors) == 0 {
		t.Error("no error was returned")
	}
}

// --- persisted queries ----------------------------------------------------

// The persisted-query path resolves the operation before the check runs, which is
// the point of doing the check at the operation layer. If it did not, a client
// could register a mutation once and then execute it by hash forever.
func TestAMutationSentAsAPersistedHashStillRequiresAToken(t *testing.T) {
	handler, store := authTestServer(t, []string{validToken})

	// Registration sends the hash *and* the text together, which is what puts the
	// document in the persisted-query cache. Sending the text alone executes it but
	// caches nothing, so the replay below would miss for the wrong reason.
	hash := sha256Hex(createProductMutation)
	store.writes.Store(0)
	postPersisted(t, handler, hash, createProductMutation, "Bearer "+validToken)
	if got := store.writes.Load(); got != 1 {
		t.Fatalf("registration failed, write count = %d", got)
	}

	// Now replay the same document by hash alone, with no credential.
	store.writes.Store(0)
	_, envelope := postPersisted(t, handler, hash, "", "")

	if got := store.writes.Load(); got != 0 {
		t.Fatalf("a persisted-query mutation executed without a token (%d writes)", got)
	}
	if code := errorCode(t, envelope); code != "UNAUTHENTICATED" {
		t.Errorf("error code = %q, want UNAUTHENTICATED", code)
	}
}

// postPersisted sends a persisted-query request. query is the document text, which
// the APQ protocol sends alongside the hash exactly once and omits thereafter.
//
// The body is marshalled rather than formatted, because a hand-built JSON string
// with four nested braces is exactly the kind of thing that is missing one and
// fails as "unexpected EOF" — an error about the test's transport rather than
// about the rule it is meant to be testing.
func postPersisted(t *testing.T, handler http.Handler, hash, query, authHeader string) (int, map[string]any) {
	t.Helper()

	payload := map[string]any{
		"extensions": map[string]any{
			"persistedQuery": map[string]any{"version": 1, "sha256Hash": hash},
		},
	}
	if query != "" {
		payload["query"] = query
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var envelope map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode response %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, envelope
}

// --- the token set itself -------------------------------------------------

func TestTokenSetDoesNotHoldPlaintext(t *testing.T) {
	set := NewTokenSet([]string{validToken})

	// A digest-based set exists so that a heap dump or a fmt of the struct does
	// not hand over a usable credential. Asserting on the rendered value is a
	// crude but real check that the token was digested rather than stored.
	if strings.Contains(fmt.Sprintf("%v", set), validToken) {
		t.Error("the token appears in the rendered TokenSet; it must be stored as a digest")
	}
}

func TestAcceptsIsFalseForAnEmptySet(t *testing.T) {
	if NewTokenSet(nil).Accepts(validToken) {
		t.Error("an empty TokenSet accepted a token")
	}
	if NewTokenSet([]string{"", "   "}).Accepts("") {
		t.Error("blank tokens formed a usable set")
	}
}

// --- helpers --------------------------------------------------------------

// sha256Hex is the hash Automatic Persisted Queries looks up by.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func errorCode(t *testing.T, envelope map[string]any) string {
	t.Helper()
	return firstError(t, envelope)
}

func firstError(t *testing.T, envelope map[string]any) string {
	t.Helper()

	raw, ok := envelope["errors"]
	if !ok {
		t.Fatalf("response has no errors array: %v", envelope)
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("errors array is empty or malformed: %v", raw)
	}
	first, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("error entry is not an object: %v", list[0])
	}

	message, _ := first["message"].(string)
	if extensions, ok := first["extensions"].(map[string]any); ok {
		if code, ok := extensions["code"].(string); ok {
			return code
		}
	}
	return message
}

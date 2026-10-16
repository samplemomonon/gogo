package handler

import (
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"
)

// The two limit extensions are the only pieces of hardening written by hand rather
// than configured, so they get tested as carefully as if they were a dependency. A
// limit with an off-by-one either rejects legitimate queries or, worse, lets through
// the queries it was written to stop.

const depthTestSchema = `
type Query {
  product(id: ID!): Product
  products: [Product!]!
  categories: [Category!]!
}
type Product {
  id: ID!
  name: String!
  category: Category
  related: [Product!]
}
type Category {
  id: ID!
  parent: Category
  children: [Category!]
}
`

// operationContext parses a query and wraps it as gqlgen would hand it over.
func operationContext(t *testing.T, query string) *graphql.OperationContext {
	t.Helper()

	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: depthTestSchema})
	if err != nil {
		t.Fatalf("load schema: %v", err)
	}
	doc, gqlErr := parser.ParseQuery(&ast.Source{Name: "query.graphql", Input: query})
	if gqlErr != nil {
		t.Fatalf("parse query: %v", gqlErr)
	}
	// Validate first, so the fragment definitions the depth walk follows are
	// resolved. A document handed to the mutator in production is always validated,
	// and the walk relies on that to follow a spread to its definition.
	listErrs := validator.Validate(schema, doc)
	if listErrs != nil {
		t.Fatalf("validate query: %v", listErrs)
	}

	// OperationContext carries no schema; the mutator only needs the parsed document,
	// which is what the depth walk reads.
	return &graphql.OperationContext{
		Doc:       doc,
		Operation: doc.Operations[0],
		RawQuery:  query,
	}
}

func TestDepthLimitAcceptsAFlatQuery(t *testing.T) {
	op := operationContext(t, `{ products { id name } }`)

	if err := FixedDepthLimit(5).MutateOperationContext(t.Context(), op); err != nil {
		t.Fatalf("a two-level query should pass a limit of 5: %v", err)
	}
}

func TestDepthLimitRejectsADeepQuery(t *testing.T) {
	// Six levels: products, related, related, related, related, category, parent.
	query := `
    query {
      products {
        related { related { related { category { parent { children { id } } } } } }
      }
    }
  `
	op := operationContext(t, query)

	err := FixedDepthLimit(4).MutateOperationContext(t.Context(), op)
	if err == nil {
		t.Fatal("a deeply nested query should be rejected")
	}

	code := err.Extensions["code"]
	if code != "DEPTH_LIMIT_EXCEEDED" {
		t.Fatalf("code %v, want DEPTH_LIMIT_EXCEEDED", code)
	}
	if got := err.Extensions["limit"]; got != 4 {
		t.Fatalf("limit extension %v, want 4", got)
	}
	if got, ok := err.Extensions["actual"].(int); !ok || got <= 4 {
		t.Fatalf("actual extension %v, want a depth above the limit", err.Extensions["actual"])
	}
	// The deepest path is what lets a client author find the offending nesting
	// instead of guessing which part of a large document to blame.
	if path, ok := err.Extensions["deepestPath"].(string); !ok || !strings.Contains(path, "related") {
		t.Fatalf("deepestPath %v, want it to name the nested field", err.Extensions["deepestPath"])
	}
}

func TestDepthIsExact(t *testing.T) {
	// { products { id } } is depth 2: products is level 1, id is level 2.
	op := operationContext(t, `{ products { id } }`)

	if err := FixedDepthLimit(2).MutateOperationContext(t.Context(), op); err != nil {
		t.Fatalf("depth 2 should pass a limit of exactly 2: %v", err)
	}
	if err := FixedDepthLimit(1).MutateOperationContext(t.Context(), op); err == nil {
		t.Fatal("depth 2 should fail a limit of 1")
	}
}

func TestDepthFollowsFragments(t *testing.T) {
	// The fragment's selections nest at the point of the spread, not at the
	// definition. Counting a fragment definition separately would let depth hide
	// inside one, and would also double-count a fragment used more than once.
	query := `
    query {
      products { ...productFields }
    }
    fragment productFields on Product {
      related { related { related { id } } }
    }
  `
	op := operationContext(t, query)

	if err := FixedDepthLimit(3).MutateOperationContext(t.Context(), op); err == nil {
		t.Fatal("the fragment nests 4 deep, so a limit of 3 should reject")
	}
	if err := FixedDepthLimit(5).MutateOperationContext(t.Context(), op); err != nil {
		t.Fatalf("the fragment nests 4 deep, which should pass a limit of 5: %v", err)
	}
}

func TestDepthCountsInlineFragmentsAtTheSameLevel(t *testing.T) {
	// An inline fragment adds no nesting of its own; it only changes the type.
	op := operationContext(t, `{
      products {
        ... on Product { category { id } }
      }
    }`)

	if err := FixedDepthLimit(3).MutateOperationContext(t.Context(), op); err != nil {
		t.Fatalf("depth is 3 (products, category, id); the inline fragment should not add a level: %v", err)
	}
}

func TestDepthLimitZeroIsDisabled(t *testing.T) {
	// Zero means no limit, matching gqlgen's own convention for MaxDepth. Silently
	// rejecting everything would be a surprising way to disable a limit.
	query := `{ products { related { related { category { parent { id } } } } } }`
	op := operationContext(t, query)

	if err := FixedDepthLimit(0).MutateOperationContext(t.Context(), op); err != nil {
		t.Fatalf("a zero limit should disable the check: %v", err)
	}
	if err := FixedDepthLimit(-1).MutateOperationContext(t.Context(), op); err != nil {
		t.Fatalf("a negative limit should disable the check: %v", err)
	}
}

func TestSelectionDepthReportsTheDeepestBranch(t *testing.T) {
	// Two branches of different depth: the shallower one must not cap the reported
	// depth, or a deep branch elsewhere in the query would slip through.
	op := operationContext(t, `{
      products {
        id
        category { parent { parent { parent { id } } } }
      }
    }`)

	err := FixedDepthLimit(4).MutateOperationContext(t.Context(), op)
	if err == nil {
		t.Fatal("the deep branch should be rejected")
	}
	actual, ok := err.Extensions["actual"].(int)
	if !ok {
		t.Fatalf("actual is %T", err.Extensions["actual"])
	}
	// The deep branch is products, category, parent, parent, parent, id. The leaf id
	// counts, which is what makes the number mean something to a caller reading it
	// against their own query.
	if actual != 6 {
		t.Fatalf("actual depth %d, want 6 (products, category, parent x3, id)", actual)
	}
}

func TestExtensionsSatisfyTheServerInterface(t *testing.T) {
	// The server's Use method requires ExtensionName, so a limiter implementing only
	// the mutator interface would fail to install at server construction.
	for _, ext := range []graphql.HandlerExtension{FixedDepthLimit(10), QueryLengthLimit(10)} {
		if ext.ExtensionName() == "" {
			t.Errorf("%T has no ExtensionName", ext)
		}
		if err := ext.Validate(nil); err != nil {
			t.Errorf("%T Validate: %v", ext, err)
		}
	}
}

func TestQueryLengthLimitRejectsALongDocument(t *testing.T) {
	// The shape the token limit does not catch: a wide selection of short tokens stays
	// shallow and cheap by token count while the document itself grows without bound.
	op := operationContext(t, "query { "+strings.Repeat("__typename ", 400)+"}")

	err := QueryLengthLimit(100).MutateOperationContext(t.Context(), op)
	if err == nil {
		t.Fatal("a document past the byte limit was accepted")
	}
	if code := err.Extensions["code"]; code != "QUERY_TOO_LONG" {
		t.Fatalf("code %v, want QUERY_TOO_LONG", code)
	}
	if got := err.Extensions["actual"]; got == nil {
		t.Error("the rejection should report the actual size")
	}
}

func TestQueryLengthLimitAllowsAShortDocument(t *testing.T) {
	op := operationContext(t, "{ products { id } }")

	if err := QueryLengthLimit(16_000).MutateOperationContext(t.Context(), op); err != nil {
		t.Fatalf("a short document was rejected: %v", err)
	}
}

func TestQueryLengthLimitZeroIsDisabled(t *testing.T) {
	op := operationContext(t, "query { "+strings.Repeat("__typename ", 400)+"}")

	if err := QueryLengthLimit(0).MutateOperationContext(t.Context(), op); err != nil {
		t.Fatalf("a zero limit should disable the check: %v", err)
	}
}

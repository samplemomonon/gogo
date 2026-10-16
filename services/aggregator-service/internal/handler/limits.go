package handler

import (
	"context"
	"fmt"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// This file holds the two hardening extensions that gqlgen does not provide.
//
// Both are written here rather than pulled in as dependencies, and for the depth
// limiter the reason is concrete: the recommended alternative,
// github.com/oyyblin/gqlgen-depth-limit-extension, was last committed in February 2023,
// has one star, and pins gqlgen v0.17.24. Taking that into a service that pins
// v0.17.95 would mean an unmaintained dependency guarding the one limit that has to
// work. Each extension here is small enough to own.

// FixedDepthLimit rejects queries nested deeper than max.
//
// Why this is separate from the complexity limit, which is already built in: the two
// bound different things. Complexity is about cost, and a deeply nested query is also
// an expensive one, so a cost limit incidentally limits depth. That is not sufficient.
// Depth bounds recursion in the executor and in client-side response handling, and the
// failure it prevents is a stack exhaustion rather than a slow query. A limit set
// purely on cost lets a narrow, deeply nested query through whenever its total stays
// under budget.
//
// The check runs as an operation-context mutator, which is before execution and before
// any resolver runs. A rejected query therefore costs a parse and nothing more.
func FixedDepthLimit(max int) *depthLimitExtension {
	return &depthLimitExtension{max: max}
}

type depthLimitExtension struct {
	max int
}

// MutateOperationContext validates the parsed document's depth.
func (d *depthLimitExtension) MutateOperationContext(_ context.Context, opCtx *graphql.OperationContext) *gqlerror.Error {
	if d.max <= 0 || opCtx == nil || opCtx.Operation == nil {
		return nil
	}

	depth, deepest := selectionDepth(opCtx.Operation.SelectionSet, 0)

	if depth <= d.max {
		return nil
	}

	out := gqlerror.Errorf(
		"query is nested %d levels deep, which exceeds the maximum of %d",
		depth, d.max,
	)
	out.Extensions = map[string]any{
		"code":   "DEPTH_LIMIT_EXCEEDED",
		"limit":  d.max,
		"actual": depth,
		// Reported so a client author can find the offending nesting rather than
		// guessing which part of a large document to blame.
		"deepestPath": deepest,
	}
	if opCtx.Operation.Position != nil {
		out.Locations = []gqlerror.Location{{
			Line:   opCtx.Operation.Position.Line,
			Column: opCtx.Operation.Position.Start,
		}}
	}
	return out
}

// ExtensionName identifies the extension in stats and logs.
func (d *depthLimitExtension) ExtensionName() string { return "DepthLimit" }

// Validate is called when the extension is installed. A depth limit needs nothing from
// the schema, so it has nothing to check.
func (d *depthLimitExtension) Validate(graphql.ExecutableSchema) error { return nil }

// selectionDepth walks a selection set and returns its depth and deepest path.
//
// A fragment contributes its selections at the point of the spread, not at its
// definition, because that is where the response actually nests. Counting a fragment
// definition separately would let depth hide inside one, and would also double-count a
// fragment used more than once.
//
// Cycles cannot occur in a validated document: the validator has already confirmed the
// fragment graph is acyclic. The walk relies on that rather than carrying a seen-set,
// which would have to be threaded through the recursion to be correct anyway.
func selectionDepth(set ast.SelectionSet, current int) (int, string) {
	if len(set) == 0 {
		return current, ""
	}

	maxDepth := current
	maxPath := ""

	for _, selection := range set {
		var depth int
		var path string

		switch sel := selection.(type) {
		case *ast.Field:
			// +1 for the field itself, which is what makes the number mean something to
			// a caller reading it against their own query: a leaf field is depth 1.
			depth, path = selectionDepth(sel.SelectionSet, current+1)
			path = fmt.Sprintf("%s.%s", path, sel.Name)

		case *ast.InlineFragment:
			// An inline fragment adds no nesting of its own; it only changes the type.
			depth, path = selectionDepth(sel.SelectionSet, current)

		case *ast.FragmentSpread:
			// Resolved by the time this runs. An unresolvable spread cannot occur
			// post-validation, so a nil definition means the document never reached
			// this point; skipping is safer than dereferencing.
			if sel.Definition == nil {
				continue
			}
			depth, path = selectionDepth(sel.Definition.SelectionSet, current)
			path = fmt.Sprintf("...%s%s", sel.Name, path)

		default:
			continue
		}

		if depth > maxDepth {
			maxDepth = depth
			maxPath = path
		}
	}

	return maxDepth, maxPath
}

// QueryLengthLimit rejects a query document longer than max bytes.
//
// It exists because the parser token limit counts tokens rather than bytes. A wide
// selection made of many short tokens can be very long while staying shallow, so the
// token count stays inside budget while the document still occupies the parse,
// validation and marshalling paths. A byte cap is the cheap outer bound for that shape.
//
// Like the depth limiter this runs after parsing, which is later than ideal for a DoS
// guard. The parser token limit is the earlier bound and this is the backstop; moving
// it before the parse would mean intercepting raw request parameters, which is what the
// persisted-query extension already does for a different reason, and duplicating that
// here would risk the two disagreeing about what a request is.
func QueryLengthLimit(max int) *queryLengthExtension {
	return &queryLengthExtension{max: max}
}

type queryLengthExtension struct {
	max int
}

func (q *queryLengthExtension) MutateOperationContext(_ context.Context, opCtx *graphql.OperationContext) *gqlerror.Error {
	if q.max <= 0 || opCtx == nil {
		return nil
	}
	if len(opCtx.RawQuery) <= q.max {
		return nil
	}

	out := gqlerror.Errorf(
		"query document is %d bytes, which exceeds the maximum of %d",
		len(opCtx.RawQuery), q.max,
	)
	out.Extensions = map[string]any{
		"code":   "QUERY_TOO_LONG",
		"limit":  q.max,
		"actual": len(opCtx.RawQuery),
	}
	if opCtx.Operation != nil && opCtx.Operation.Position != nil {
		out.Locations = []gqlerror.Location{{
			Line:   opCtx.Operation.Position.Line,
			Column: opCtx.Operation.Position.Start,
		}}
	}
	return out
}

func (q *queryLengthExtension) ExtensionName() string { return "QueryLengthLimit" }

func (q *queryLengthExtension) Validate(graphql.ExecutableSchema) error { return nil }

// Compile-time checks that both extensions satisfy what the server's Use requires.
// Without them a missing ExtensionName would only surface at server construction.
var (
	_ graphql.HandlerExtension        = (*depthLimitExtension)(nil)
	_ graphql.OperationContextMutator = (*depthLimitExtension)(nil)
	_ graphql.HandlerExtension        = (*queryLengthExtension)(nil)
	_ graphql.OperationContextMutator = (*queryLengthExtension)(nil)
)

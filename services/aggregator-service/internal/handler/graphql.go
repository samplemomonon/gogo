package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/99designs/gqlgen/complexity"
	"github.com/99designs/gqlgen/graphql"
	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"aggregator-service/internal/apperror"
	"aggregator-service/internal/config"
)

// NewGraphQL builds the GraphQL handler.
//
// Every limit here answers one question: a REST client can only ask for what the
// server's routes expose, while a GraphQL client chooses its own cost per request.
// Without these, one request can name an expensive field a hundred times under
// different aliases and each occurrence is resolved separately.
//
// The limits are layered rather than singular because they catch different shapes.
// Complexity alone is known to be evadable, which is what CVE-2025-8014 was, so
// depth is bounded separately and both are backed by a parser-level limit that
// runs before either of them.
func NewGraphQL(cfg *config.Config, exec graphql.ExecutableSchema, log *slog.Logger) *gqlhandler.Server {
	srv := gqlhandler.New(exec)

	srv.AddTransport(transport.POST{})
	srv.AddTransport(transport.Options{})
	// GET is enabled but restricted by the transports themselves. A query in a URL
	// lands in access logs, proxy logs, and browser history, which is why it is off
	// by default upstream; it is useful here for read-only health checks and for
	// clients that cannot POST.
	srv.AddTransport(transport.GET{})

	// Parse each distinct document once. Without this every request re-parses and
	// re-validates a document that has almost certainly been seen before.
	srv.SetQueryCache(newDocumentCache(lruDefaultSize))

	// Bounds parser recursion. A deeply nested document exhausts the stack during
	// parsing, before validation could reject it, so this has to be here rather than
	// in a validator. Same class of fix as gqlgen's response to CVE-2023-49559.
	srv.SetParserTokenLimit(cfg.ParserTokenLimit)

	// Complexity. Every object and list costs one, scalars cost zero. Charging for
	// scalars would make the budget depend on how many fields a client happens to
	// select rather than on how much work the server does, and the work is driven
	// by object resolution. The limit sits above what a legitimate product page
	// costs, so normal queries pass with headroom.
	srv.Use(extension.FixedComplexityLimit(cfg.MaxQueryComplexity,
		complexity.WithFixedScalarValue(0),
	))

	// Depth, bounded separately. Complexity alone does not stop a narrow query
	// nested deeply: each level can cost one and the document can still recurse far
	// enough to exhaust the resolver stack. gqlgen has no built-in depth limiter, so
	// this is the extension below rather than a config key.
	srv.Use(FixedDepthLimit(cfg.MaxQueryDepth))

	if cfg.EnableIntrospection {
		// The executor defaults introspection to off; this turns it on, and it is
		// gated so a production deployment can leave it off and deny a caller a
		// machine-readable inventory of the whole schema.
		srv.Use(extension.Introspection{})
	}

	if cfg.PersistedQueriesEnable {
		// Automatic Persisted Queries. The client hashes its query and sends only
		// the hash; on a miss the server replies PERSISTED_QUERY_NOT_FOUND and the
		// client sends the text once, which is then cached. A returning client never
		// transmits a query document again, so the input an attacker controls never
		// reaches the parser.
		srv.Use(extension.AutomaticPersistedQuery{
			Cache: newPersistedQueryCache(lruDefaultSize),
		})
	}

	// Write operations require a bearer token. This is registered at the operation
	// layer rather than as transport middleware because only gqlgen knows, after
	// parsing and persisted-query resolution, whether the request is a write —
	// and because a document that fails to parse never reaches a resolver, so
	// there is nothing to protect on that path.
	//
	// Order matters relative to nothing above it: it reads the operation context,
	// which is populated before extensions run.

	srv.AroundOperations(RequireMutationToken(NewTokenSet(cfg.MutationTokens), log))

	srv.SetErrorPresenter(errorPresenter(log))

	// Query length, bounded on the raw document string. The parser token limit counts
	// tokens rather than bytes, so a wide selection made of many short tokens can be
	// long without being deep. A byte cap is the cheap outer bound covering that shape.
	if cfg.MaxQueryLength > 0 {
		srv.Use(QueryLengthLimit(cfg.MaxQueryLength))
	}

	return srv
}

// PlaygroundHandler returns the interactive schema browser, or nil when disabled.
//
// It is a separate route rather than a transport, so the query endpoint keeps a single
// content type and the browser UI is reachable at one explicit path only.
//
// The return type is http.Handler rather than http.HandlerFunc deliberately. Returning
// a nil http.HandlerFunc produces a non-nil interface holding a nil function, so a
// caller's `if h != nil` check passes and the nil reaches ServeMux.Handle, which
// panics at startup rather than failing at build time. Returning the interface makes
// the nil honest.
func PlaygroundHandler(cfg *config.Config) http.Handler {
	if !cfg.PlaygroundEnabled {
		return nil
	}
	return playground.Handler("ecommerce aggregator", "/query")
}

// errorPresenter renders errors for the client.
//
// The default presenter passes the full internal error message through, which is
// right for a validation failure and wrong for everything else: a downstream gRPC
// error carries the target address, and a wrapped Go error carries package paths
// and function names. Only errors the aggregator has classified as safe to show are
// passed through. The rest become a fixed message with the detail kept in the log.
//
// This is the single place that decides what a client learns about a failure, so it
// is deliberately narrow: a new error type becomes visible by being added here,
// rather than leaking by default.
func errorPresenter(log *slog.Logger) graphql.ErrorPresenterFunc {
	return func(ctx context.Context, err error) *gqlerror.Error {
		// Classification comes first, and this ordering is load-bearing rather than
		// incidental.
		//
		// gqlgen's executor wraps whatever a resolver returns into a *gqlerror.Error
		// before the presenter sees it, copying the message and discarding whatever
		// the original carried. *gqlerror.Error implements Unwrap, so the aggregator's
		// own sentinels are still reachable through the wrapper -- but only if it is
		// inspected before the *gqlerror.Error branch claims the error and passes it
		// through unchanged. Testing that branch first strips the machine-readable code
		// a client needs in order to decide whether to retry, and turns every internal
		// error into "internal server error" because the wrapper carries no code.
		if classified := classifyGraphQLError(ctx, err); classified != nil {
			log.Info("resolver error",
				"code", classified.Extensions["code"],
				"message", classified.Message,
				"path", gqlPath(classified.Path),
			)
			return classified
		}

		// Carries its own extensions: a resolver that classified a field failure
		// itself, so its message was written to be shown.
		var withExtensions interface{ Extensions() map[string]any }
		if errors.As(err, &withExtensions) {
			out := &gqlerror.Error{
				Message:    err.Error(),
				Path:       graphql.GetPath(ctx),
				Extensions: withExtensions.Extensions(),
			}
			logAt(log, out)
			return out
		}

		// Anything else that is already a GraphQL error was produced by gqlgen itself:
		// a validation failure, a parse failure, a hardening rejection, or an APQ
		// handshake message. Those are written to be read by the client and carry
		// their own codes, so they pass through untouched.
		var gqlErr *gqlerror.Error
		if errors.As(err, &gqlErr) {
			logAt(log, gqlErr)
			return gqlErr
		}

		// Unclassified means an internal error. The message stays in the log.
		log.Error("unclassified resolver error",
			"error", err.Error(),
			"path", gqlPath(graphql.GetPath(ctx)),
		)
		return &gqlerror.Error{
			Message: "internal server error",
			Path:    graphql.GetPath(ctx),
		}
	}
}

// classifyGraphQLError converts a classified aggregator error into a GraphQL
// error, or returns nil when the error carries no classification.
//
// Returning nil for the unclassified case is the point: the caller then treats the
// error as a bug and hides its message, so an unrecognised error type is private
// by default rather than exposed by default.
func classifyGraphQLError(ctx context.Context, err error) *gqlerror.Error {
	// A client mistake carries a message written for the client, so it passes
	// through. The field name travels as an extension so a form can attach the
	// message to the control that caused it.
	var invalid *apperror.InvalidInput
	if errors.As(err, &invalid) {
		out := &gqlerror.Error{
			Message:    invalid.Message,
			Path:       graphql.GetPath(ctx),
			Extensions: map[string]any{"code": "BAD_USER_INPUT"},
		}
		if invalid.Field != "" {
			out.Extensions["field"] = invalid.Field
		}
		return out
	}

	// Everything else gets a fixed message and a code. The sentinel is matched
	// rather than err.Error() rendered, because the wrapped text may name a
	// downstream address.
	path := graphql.GetPath(ctx)
	switch {
	case errors.Is(err, apperror.ErrNotFound):
		return &gqlerror.Error{
			Message:    "not found",
			Path:       path,
			Extensions: map[string]any{"code": "NOT_FOUND"},
		}
	case errors.Is(err, apperror.ErrConflict):
		return &gqlerror.Error{
			Message:    "conflict",
			Path:       path,
			Extensions: map[string]any{"code": "CONFLICT"},
		}
	case errors.Is(err, apperror.ErrUnavailable):
		return &gqlerror.Error{
			Message: "a required service is temporarily unavailable",
			Path:    path,
			Extensions: map[string]any{
				"code":      "DEPENDENCY_UNAVAILABLE",
				"retryable": true,
			},
		}
	case errors.Is(err, apperror.ErrUnauthenticated):
		// retryable is absent rather than false. This is not a fault the server had
		// and recovered from; it is a credential the caller has not supplied, and
		// the same request will fail identically until they supply one.
		return &gqlerror.Error{
			Message:    "authentication is required for this operation",
			Path:       path,
			Extensions: map[string]any{"code": "UNAUTHENTICATED"},
		}
	case errors.Is(err, apperror.ErrRejected):
		return &gqlerror.Error{
			Message:    "the query was rejected by a server limit",
			Path:       path,
			Extensions: map[string]any{"code": "REJECTED"},
		}
	default:
		return nil
	}
}

// gqlPath renders a response path for the log.
func gqlPath(path ast.Path) string {
	if len(path) == 0 {
		return ""
	}
	return fmt.Sprintf("%v", path)
}

// logAt picks a log level from a GraphQL error code.
func logAt(log *slog.Logger, err *gqlerror.Error) {
	code := err.Extensions["code"]
	switch code {
	case "BAD_USER_INPUT":
		// A validation failure is the caller's mistake and entirely expected, so it
		// is not worth a line at warn.
		log.Debug("client error", "message", err.Message, "path", gqlPath(err.Path))
	case "PERSISTED_QUERY_NOT_FOUND", "OPERATION_RESOLUTION_FAILURE":
		// Both are normal parts of the APQ handshake.
		log.Debug("graphql protocol error", "message", err.Message)
	case "INTERNAL", "INTERNAL_SERVER_ERROR":
		log.Error("internal graphql error", "message", err.Message, "path", gqlPath(err.Path))
	default:
		log.Warn("graphql error", "code", code, "message", err.Message, "path", gqlPath(err.Path))
	}
}

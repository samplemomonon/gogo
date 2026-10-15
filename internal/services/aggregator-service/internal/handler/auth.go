package handler

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"aggregator-service/internal/apperror"
)

// Authentication for this service.
//
// # What is protected, and what is not
//
// Reads are public. This is a product catalogue: browsing, searching and reading
// a category tree are the things the service exists to make fast, and gating them
// would buy nothing a caching layer does not already do.
//
// Mutations are not public. There are ten of them — three product writes, three
// category writes, and four media writes including registerMedia, which returns a
// presigned URL that lets the caller PUT arbitrary bytes into object storage.
// Before this file, every one of them was reachable by anyone who could reach the
// port.
//
// # Why the check sits where it does
//
// The decision "is this request a write?" cannot be made from the HTTP method,
// because POST /query carries both queries and mutations. Nor should it be made by
// re-parsing the document in middleware: that parses every request twice, and the
// repository already treats the parse as the cost worth avoiding — Automatic
// Persisted Queries exists so a returning client never transmits a document at all.
//
// So the check runs at the operation layer, through gqlgen's AroundOperations.
// By then gqlgen has parsed the document, resolved automatic persisted queries,
// and selected the operation by name, so the operation type is known exactly and
// the answer is not a guess. It also costs nothing on the paths that do not need
// it: a query that never reaches a write is one integer comparison.
//
// # Why a static bearer token, and not more
//
// The five services below this one have no authentication either, so there is no
// identity to delegate to and no principal to derive. What is available at this
// layer is the thing the README already identified: "the composition layer is the
// most logical place to add it, because one point closes all APIs."
//
// So this is a credential check, not an identity system. It answers exactly one
// question — may this caller write — and it is honest about being that. Callers
// are identified by which token they present, not by who they are. A real
// identity system, with per-caller authorisation and audit, is a larger decision
// that belongs above this service and is deliberately not faked here.
//
// Multiple tokens are supported because rotation is a requirement, not a nicety:
// a single token means rotating it invalidates every caller at once. Comparison is
// constant-time and over fixed-length digests, so neither the token's length nor
// its content can be recovered from response timing.

// bearerPrefix is the only accepted scheme. "Basic" is not supported and is not
// silently ignored — a caller sending the wrong scheme is refused rather than
// having its credential dropped and the request treated as anonymous.
const bearerPrefix = "Bearer "

// TokenSet is the set of bearer tokens permitted to execute mutations.
//
// It holds digests rather than the tokens themselves, so a dump of the struct, or
// a value captured in a heap profile, does not disclose a usable credential.
type TokenSet struct {
	// digests are SHA-256 of each configured token.
	digests [][sha256.Size]byte
}

// NewTokenSet digests the supplied tokens.
//
// It does not validate them: config.Validate is the single place that decides what
// a usable token is, so that the rule is stated once and startup is where it is
// enforced.
func NewTokenSet(tokens []string) *TokenSet {
	set := &TokenSet{}
	for _, token := range tokens {
		if strings.TrimSpace(token) == "" {
			continue
		}
		set.digests = append(set.digests, sha256.Sum256([]byte(token)))
	}
	return set
}

// Accepts reports whether presented matches any configured token.
//
// Every token is compared, and no early return, so the time taken does not depend
// on which token matched or on how far through the set the match occurred.
//
// That property is NOT covered by a behavioural test, deliberately. Timing a
// comparison loop reliably requires either a statistical test with a large
// iteration count or an AST check on this file's own source, and both are worse
// than the gap they close: a timing test that passes by luck teaches a reader the
// property is verified when it is not, and an AST check breaks the moment the
// function is refactored into a shape that is still constant-time.
//
// The cost of being wrong here is an attacker learning a token prefix from
// response latency. It is recorded rather than papered over.
func (s *TokenSet) Accepts(presented string) bool {
	if s == nil || len(s.digests) == 0 || presented == "" {
		return false
	}

	candidate := sha256.Sum256([]byte(presented))

	// Compare against a fixed slot even after a match, so the loop body does not
	// become an early exit that leaks the match position through timing.
	var matched int
	for i := range s.digests {
		matched |= subtle.ConstantTimeCompare(candidate[:], s.digests[i][:])
	}
	return matched == 1
}

// Len reports how many tokens are configured, for logging and startup checks.
func (s *TokenSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.digests)
}

// bearerToken extracts the credential from an Authorization header.
//
// Returns "" for an absent, empty, or non-Bearer header. The value is not
// trimmed: a trailing space means the caller built the header wrong, and silently
// accepting it would mean two different header spellings are both valid.
func bearerToken(header http.Header) string {
	values := header.Values("Authorization")
	if len(values) == 0 {
		return ""
	}
	value := values[0]
	if !strings.HasPrefix(value, bearerPrefix) {
		return ""
	}
	token := strings.TrimPrefix(value, bearerPrefix)
	if token == "" || strings.ContainsAny(token, " \t") {
		return ""
	}
	return token
}

// RequireMutationToken returns gqlgen operation middleware that refuses writes
// without a valid bearer token.
//
// Reads pass through untouched. That asymmetry is the design, not an omission:
// a catalogue that required a credential to browse would have moved the cost to
// every anonymous visitor to save nothing.
//
// An empty TokenSet refuses every mutation. Config.Validate already refuses to
// start in that state, so this is the second line rather than the first — a Server
// constructed directly in a test must not accidentally be permissive.
func RequireMutationToken(tokens *TokenSet, log *slog.Logger) graphql.OperationMiddleware {
	return func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		op := graphql.GetOperationContext(ctx)

		// A nil operation means gqlgen did not resolve one. It will fail the
		// request itself with a parse or resolution error, and no resolver runs, so
		// there is nothing here to protect against.
		if op.Operation == nil || op.Operation.Operation == ast.Query {
			return next(ctx)
		}

		if tokens.Accepts(bearerToken(op.Headers)) {
			return next(ctx)
		}

		// The operation name and the path are safe to record; the token is not, and
		// is not passed anywhere near this call. Absence of a credential and an
		// invalid one are logged at the same level deliberately — distinguishing
		// them would tell an attacker which guesses were worth continuing.
		log.Warn("rejected an unauthenticated write operation",
			slog.String("operation", string(op.Operation.Operation)),
			slog.String("operation_name", op.Operation.Name),
			slog.Bool("token_supplied", bearerToken(op.Headers) != ""),
		)

		// The response is built through classifyGraphQLError rather than
		// graphql.ErrorResponse, because a response returned by an extension does not
		// pass through the error presenter — using ErrorResponse here would drop the
		// UNAUTHENTICATED code and leave clients with a bare string to match on.
		return func(ctx context.Context) *graphql.Response {
			return &graphql.Response{
				Errors: gqlerror.List{classifyGraphQLError(ctx, apperror.ErrUnauthenticated)},
			}
		}
	}
}

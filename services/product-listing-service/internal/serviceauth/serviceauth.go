// Package serviceauth is the inbound credential check shared by this
// repository's platform services.
//
// # Why this exists
//
// The composition layer authenticates its callers, which is only useful if the
// services behind it refuse callers that did not come through it. Without this,
// the control on the aggregator is decorative: product-service listens on the
// host with POST /api/v1/products and no credential of any kind, so
//
//	curl localhost:8082/api/v1/products -d '{...}'
//
// writes to the catalogue while the aggregator's MUTATION_TOKENS sits unused a
// few ports away.
//
// So the rule is the obvious one: a service answers only callers that present a
// credential it was configured to accept. Reads and writes alike, because the
// catalogue's product list is not public data and the aggregator is the thing
// that decides what to expose of it.
//
// # Why every transport needs its own wiring
//
// gRPC metadata and an HTTP header are different plumbing, and a check that
// guards one while the other is open is the usual shape of this mistake. The
// decision itself is shared; only the plumbing differs.
package serviceauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ErrUnauthenticated is returned, or reported, when no usable credential arrived.
//
// It is deliberately not a permission error. A caller presenting the wrong
// credential and a caller presenting none are the same situation, and answering
// them differently would tell an attacker which guesses were worth continuing.
var ErrUnauthenticated = errors.New("service authentication required")

// bearerPrefix is the only accepted scheme on HTTP.
const bearerPrefix = "Bearer "

// TokenSet is the set of credentials this service accepts from its callers.
//
// It holds digests rather than the credentials, so a value captured in a heap
// profile does not disclose one that works.
type TokenSet struct {
	digests [][sha256.Size]byte
}

// NewTokenSet digests the supplied credentials.
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

// Accepts reports whether presented matches a configured credential.
//
// Every credential is compared and there is no early return, so the time taken
// does not depend on which one matched. As in the aggregator's TokenSet, that
// property is not covered by a behavioural test — see the note there.
func (s *TokenSet) Accepts(presented string) bool {
	if s == nil || len(s.digests) == 0 || presented == "" {
		return false
	}

	candidate := sha256.Sum256([]byte(presented))

	var matched int
	for i := range s.digests {
		matched |= subtle.ConstantTimeCompare(candidate[:], s.digests[i][:])
	}
	return matched == 1
}

// Len reports how many credentials are configured.
func (s *TokenSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.digests)
}

// TokenFromHeader extracts a credential from an HTTP Authorization header.
//
// Only Bearer is accepted. A missing header, a different scheme and an empty
// value all yield "", which the set rejects — so a caller using the wrong scheme
// is refused rather than silently treated as anonymous.
func TokenFromHeader(header http.Header) string {
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

// TokenFromMetadata extracts a credential from gRPC metadata.
//
// gRPC transports HTTP/2 headers as metadata, so this is the same header the
// HTTP path reads — but arriving through metadata.MD rather than http.Header.
func TokenFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	values := md.Get("authorization")
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

// HTTP wraps a handler so only callers with a valid credential reach it.
//
// exemptPaths are answered without a credential. Health and readiness probes
// must be on that list, and getting it wrong is not a subtle failure: an
// orchestrator probing liveness and receiving 401 will restart the container,
// which restarts the process that was answering the probe, which makes the
// outage total rather than partial. That is the same reasoning that keeps a
// liveness probe from touching its dependencies.
func (s *TokenSet) HTTP(next http.Handler, exemptPaths []string) http.Handler {
	exempt := make(map[string]bool, len(exemptPaths))
	for _, path := range exemptPaths {
		exempt[path] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt[r.URL.Path] || s.Accepts(TokenFromHeader(r.Header)) {
			next.ServeHTTP(w, r)
			return
		}
		writeJSONError(w, http.StatusUnauthorized)
	})
}

// writeJSONError answers in the JSON shape these services already use for
// errors, so a client does not have to special-case a plain-text response from
// this one layer.
func writeJSONError(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"a valid service credential is required"}}`))
}

// Unary returns a gRPC unary interceptor that refuses callers without a valid
// credential.
//
// It returns an interceptor rather than wrapping one so the composition is
// unambiguous: pass it to grpc.NewServer as the outermost interceptor, and a
// refused call never reaches recovery or logging as though it were a server
// error.
//
// Unauthenticated is returned rather than PermissionDenied: the caller has not
// identified itself, which is a different thing from being identified and
// refused. gRPC clients distinguish the two, and a client retrying on
// Unauthenticated when it means Unavailable would be misdirected.
func (s *TokenSet) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if s.Accepts(TokenFromMetadata(ctx)) {
			return handler(ctx, req)
		}
		return nil, status.Error(codes.Unauthenticated, ErrUnauthenticated.Error())
	}
}

// Stream returns a gRPC stream interceptor on the same terms as Unary.
//
// gRPC's own health checking protocol is exempt by construction rather than by
// path: it is answered before interceptors run, so a grpc_health_probe client
// without a credential still succeeds.
func (s *TokenSet) Stream() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if s.Accepts(TokenFromMetadata(stream.Context())) {
			return handler(srv, stream)
		}
		return status.Error(codes.Unauthenticated, ErrUnauthenticated.Error())
	}
}

package tenant

import (
	"log/slog"
	"net/http"
	"strings"
)

// Header is the request header an API client may use to name its tenant. It is
// honored only when the HostResolver was built with trustHeader: without
// authentication a client could otherwise choose whose data it reads, so the
// default posture trusts the deployment-controlled Host header instead.
const Header = "X-Tenant-ID"

// Resolver resolves the tenant for one inbound request.
type Resolver interface {
	// Resolve returns the request's tenant and whether a mapping matched. A
	// resolver never fails: an unmatched request falls back to a default.
	Resolve(r *http.Request) (ID, bool)
}

// HostResolver resolves requests against a Registry by their Host header, with
// an optional trusted X-Tenant-ID override.
type HostResolver struct {
	registry    *Registry
	trustHeader bool
}

// NewHostResolver wires a resolver over reg. When trustHeader is true an
// X-Tenant-ID header naming a served tenant wins over the host mapping.
func NewHostResolver(reg *Registry, trustHeader bool) *HostResolver {
	return &HostResolver{registry: reg, trustHeader: trustHeader}
}

// Resolve returns the tenant of req: the trusted header when enabled and
// known, else the host mapping, else the registry default.
func (r *HostResolver) Resolve(req *http.Request) (ID, bool) {
	if r == nil || r.registry == nil {
		return Default, false
	}
	if r.trustHeader {
		if raw := strings.TrimSpace(req.Header.Get(Header)); raw != "" {
			if id := ID(raw); r.registry.Has(id) {
				return id, true
			}
		}
	}
	return r.registry.ResolveHost(req.Host)
}

// Middleware carries the request's resolved tenant on its context. It never
// rejects a request: an unmatched host falls back to the default tenant, so a
// single-tenant deployment behaves exactly as before.
func Middleware(resolver Resolver, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			id, _ := resolver.Resolve(req)
			logger.Debug("tenant middleware", "host", req.Host, "tenant", id)
			next.ServeHTTP(w, req.WithContext(WithID(req.Context(), id)))
		})
	}
}

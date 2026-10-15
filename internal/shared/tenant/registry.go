package tenant

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// Registry maps an inbound request's host to a tenant ID and enumerates the
// tenants a process serves. It is immutable after construction, so it is safe
// for concurrent request handling with no locking.
type Registry struct {
	defaultID ID
	byHost    map[string]ID
	ids       []ID
	has       map[ID]struct{}
}

// Options declares the tenants a process serves.
type Options struct {
	// DefaultID is the tenant used when no host matches. Empty means Default.
	DefaultID ID

	// Hosts maps a host (case-insensitive, port and trailing dot ignored) to a
	// tenant ID, e.g. "shop.example.com" -> "ecommerce". One host names
	// exactly one tenant.
	Hosts map[string]ID

	// IDs optionally lists every tenant explicitly, including tenants reached
	// only by an API client (no host). When empty the served set is derived
	// from DefaultID and Hosts' values.
	IDs []ID
}

// NewRegistry builds a Registry from Options, normalizing hosts and rejecting
// a host mapped to an empty tenant. The served tenant set is deterministic
// (sorted), so a startup backfill iterates tenants in a stable order.
func NewRegistry(opts Options) (*Registry, error) {
	defaultID := opts.DefaultID
	if defaultID.IsZero() {
		defaultID = Default
	}

	byHost := make(map[string]ID, len(opts.Hosts))
	for rawHost, id := range opts.Hosts {
		if id.IsZero() {
			return nil, fmt.Errorf("tenant: host %q maps to an empty tenant", rawHost)
		}
		host := normalizeHost(rawHost)
		if host == "" {
			return nil, fmt.Errorf("tenant: empty host name")
		}
		byHost[host] = id
	}

	// The served set: the explicit list when given, plus Default and every
	// host-mapped tenant, de-duplicated.
	seen := make(map[ID]struct{})
	ids := make([]ID, 0, len(opts.IDs)+len(byHost)+1)
	add := func(id ID) {
		if id.IsZero() {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	add(defaultID)
	for _, id := range opts.IDs {
		add(id)
	}
	for _, id := range byHost {
		add(id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	return &Registry{
		defaultID: defaultID,
		byHost:    byHost,
		ids:       ids,
		has:       seen,
	}, nil
}

// ResolveHost returns the tenant mapped to host, or (Default, false) when no
// mapping matches. The host is normalized first, so "Shop.Example.com:8443"
// and "shop.example.com." resolve identically.
func (r *Registry) ResolveHost(host string) (ID, bool) {
	if r == nil {
		return Default, false
	}
	id, ok := r.byHost[normalizeHost(host)]
	if !ok {
		return r.defaultID, false
	}
	return id, true
}

// Default returns the tenant used when no host matches.
func (r *Registry) Default() ID {
	if r == nil {
		return Default
	}
	return r.defaultID
}

// IDs returns every tenant this process serves, in deterministic order. The
// listing projection fans its startup backfill out over this set.
func (r *Registry) IDs() []ID {
	if r == nil {
		return []ID{Default}
	}
	out := make([]ID, len(r.ids))
	copy(out, r.ids)
	return out
}

// Has reports whether id is a tenant this process serves. The trusted-header
// resolver uses it to reject a header naming an unknown tenant.
func (r *Registry) Has(id ID) bool {
	if r == nil || id.IsZero() {
		return false
	}
	_, ok := r.has[id]
	return ok
}

// normalizeHost lowercases host, drops any port and trims a trailing dot, so
// equivalent host spellings resolve to the same tenant.
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

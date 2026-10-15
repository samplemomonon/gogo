// Package tenant is gogo's multi-tenancy substrate: a tenant is the boundary
// one website's data lives behind, so a single backend can serve many sites
// without their rows ever mingling.
//
// The package owns three things and nothing else: the ID that names a tenant,
// the context carrier every layer passes it through, and the Registry that
// resolves an inbound request's host to an ID. It deliberately knows nothing
// about storage: a tenant is a filter a repository applies, never a lever that
// selects a table or builds SQL. That rule is what keeps adding a website a
// configuration change rather than a schema change.
package tenant

import "context"

// ID names a tenant. It is a plain string so it can be a database column
// value, an event field and a map key without conversion.
type ID string

// Default is the tenant a request resolves to when nothing maps its host.
// Single-tenant deployments run entirely as Default, so introducing tenancy
// changes no observable behaviour until a second tenant is configured.
const Default ID = "default"

// String returns the ID's wire/column form.
func (id ID) String() string { return string(id) }

// IsZero reports whether the ID is unset. An unset ID is never a valid tenant:
// read paths fall back to Default instead.
func (id ID) IsZero() bool { return id == "" }

// contextKey is unexported so no other package can collide with the tenant
// value on a context.
type contextKey struct{}

// WithID returns a context carrying id. The HTTP middleware calls it once per
// request; every layer below reads the same value.
func WithID(ctx context.Context, id ID) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext returns the tenant carried by ctx. The boolean is false when no
// tenant was set, so a caller that must not guess can tell the difference.
func FromContext(ctx context.Context) (ID, bool) {
	id, ok := ctx.Value(contextKey{}).(ID)
	if !ok || id.IsZero() {
		return "", false
	}
	return id, true
}

// FromContextOrDefault returns the context's tenant, or Default when none was
// set. Read paths use it so the single-tenant deployment needs no special
// case: "no tenant" and "the default tenant" are the same data.
func FromContextOrDefault(ctx context.Context) ID {
	if id, ok := FromContext(ctx); ok {
		return id
	}
	return Default
}

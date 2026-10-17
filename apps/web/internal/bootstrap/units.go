// The gateway's unit table: which top-level HTTP surfaces and which feature
// modules the composition root wires, and which upstreams get a circuit breaker.
//
// This used to be six literal Register(b) calls and seven literal
// contributeBreaker(b, …) calls scattered through the middle of di.go. That
// had two costs. The list of units was invisible: you had to read the whole
// container assembly to learn that the gateway had six of them. And the claim
// that a breaker "stays in lockstep by construction" was only true for the
// seven names someone remembered to type — adding an eighth upstream meant
// editing Go in the middle of a 400-line function, with nothing to remind you
// that a second place needed the same edit.
//
// So the lists live here instead: one place, typed, reviewable as a diff. The
// invariants are enforced by units_test.go, including the one that matters —
// every unit in the tree that offers a Register must be listed here, so adding
// a module without wiring it is a test failure rather than a boot-time absence.
package bootstrap

import (
	fdi "gogo/internal/shared/foundation/di"

	"gogo/internal/modules/billing"
	"gogo/internal/modules/ops"
	"gogo/internal/pages/auth"
	"gogo/internal/pages/home"
	"gogo/internal/pages/profile"
	"gogo/internal/pages/seller"
)

// unitKind distinguishes the two wiring shapes a top-level unit can have.
type unitKind int

const (
	// kindPage is a top-level HTTP surface: wired into the container and mounted
	// by the shell directly. A page has no manifest and no lifecycle, so it is
	// not federated and never appears in /module-registry.
	kindPage unitKind = iota

	// kindModule is a federated feature module: wired into the container AND
	// added to the module registry, so the shell discovers it by manifest and
	// starts and drains it with the rest.
	kindModule
)

// unit is one entry of the gateway's unit table.
type unit struct {
	// path is the unit's directory, relative to the repository root. It is not
	// used at runtime; it is the identity the tests check the declaration
	// against, which is why it is kept even though the import already says it.
	path string

	kind unitKind

	// register contributes the unit's own object graph to the container. Each
	// unit keys itself by type, so two units cannot collide and no name string
	// has to be kept in sync by hand.
	register func(*fdi.Builder)
}

// registeredUnits lists every unit whose Register contributes itself to the
// container.
//
// Order is documentation, not semantics. Registration only declares lazy
// providers, so the resolution order — which *is* load-bearing, because
// resolving the app forces the in-process connections that register their own
// modules as a side effect — is decided in di.go's app provider, not here.
var registeredUnits = []unit{
	{path: modulesDir + "/billing", kind: kindModule, register: billing.Register},
	{path: modulesDir + "/ops", kind: kindModule, register: ops.Register},
	{path: pagesDir + "/home", kind: kindPage, register: home.Register},
	{path: pagesDir + "/seller", kind: kindPage, register: seller.Register},
	{path: pagesDir + "/auth", kind: kindPage, register: auth.Register},
	{path: pagesDir + "/profile", kind: kindPage, register: profile.Register},
}

// modulesDir and pagesDir are the two roots the unit table draws from. They are
// spelled once so the table reads as "/billing" and "home" rather than as full
// import paths repeated six times.
const (
	modulesDir = "internal/modules"
	pagesDir   = "internal/pages"
)

// upstream is one dependency the gateway calls, with its readiness class
// attached.
type upstream struct {
	name string

	// optional is the readiness class: true means an open breaker on this
	// upstream degrades the surfaces that need it without taking the gateway
	// out of rotation. Declaring it here and nowhere else is what keeps the
	// classification visible in one table.
	optional bool
}

// breakerUpstreams lists every upstream the gateway calls, in the order its
// breakers are contributed.
//
// Every entry is both a named breaker and a member of the contributed breaker
// group that the ops readiness probe reads, so an upstream cannot be added
// without becoming visible to the load balancer and classified for readiness.
// Reading the map for metrics off that same group is what makes a new upstream
// automatic there too.
//
// inventory, sales and auth are reached in-process through grpcx.InProcess, but
// they are still real dependencies that can fail, so they get breakers on the
// same terms as the remote ones. The product-platform four are separate breakers
// on purpose: the storefront must keep serving browse while search is down —
// which is why search is the one upstream readiness does not gate on. A browse
// outage still fails readiness: without the grid there is no storefront left to
// keep serving (see TestBrowseOutageIsReportedRatherThanRenderedAsEmpty).
var breakerUpstreams = []upstream{
	{name: upstreamProduct},
	{name: upstreamProductListing},
	{name: upstreamProductSearch, optional: true},
	{name: upstreamCategory},
	{name: upstreamInventory},
	{name: upstreamSales},
	{name: upstreamAuth},
}

// registerUnits contributes every declared unit to the container.
func registerUnits(b *fdi.Builder) {
	for _, u := range registeredUnits {
		u.register(b)
	}
}

// registerUpstreamBreakers gives every declared upstream a breaker.
func registerUpstreamBreakers(b *fdi.Builder) {
	for _, u := range breakerUpstreams {
		contributeBreaker(b, u.name)
	}
}

// ReadinessPolicy is this root's classification of the declared upstreams,
// derived from the table so the two cannot drift: an optional upstream is one
// the gateway keeps serving without.
//
// Exported because the integration fakestack mirrors the composition root by
// hand and must apply the same classification, not a copy of it that can
// silently drift from the table.
func ReadinessPolicy() ops.ReadinessPolicy {
	var optional []string
	for _, u := range breakerUpstreams {
		if u.optional {
			optional = append(optional, u.name)
		}
	}
	return ops.ReadinessPolicy{Optional: optional}
}

// moduleUnits returns the units that must be added to the module registry, in
// declaration order. Pages are excluded: the shell mounts those directly.
func moduleUnits() []unit {
	var out []unit
	for _, u := range registeredUnits {
		if u.kind == kindModule {
			out = append(out, u)
		}
	}
	return out
}

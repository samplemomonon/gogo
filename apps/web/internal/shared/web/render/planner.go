package render

import "time"

// RenderMode is the strategy used to produce the HTML of a single island.
// The names map to the widely used rendering strategies (SSG, ISR, SSR,
// CSR): a page is never "one strategy" — each component decides for itself.
type RenderMode int

const (
	// ModeStatic renders the island once and caches it until an explicit
	// invalidate (SSG semantics).
	ModeStatic RenderMode = iota

	// ModeRevalidate renders the island, serves it from the cache and
	// re-renders it once the entry expires (ISR semantics).
	ModeRevalidate

	// ModeDynamic renders the island on every request (SSR semantics).
	ModeDynamic

	// ModeClient renders a lightweight placeholder that the browser
	// fills by fetching the island endpoint (CSR semantics).
	ModeClient
)

// Freshness describes how up-to-date an island's data must be.
type Freshness int

const (
	FreshnessLow Freshness = iota
	FreshnessMedium
	FreshnessRealtime
)

// RenderingContext carries per-request facts the planner may consult.
type RenderingContext struct {
	Path            string
	IsAuthenticated bool
	IsHTMX          bool
}

// RenderingContract declares the rendering characteristics of a component.
// The planner derives the concrete mode from these characteristics, so
// handlers declare *what* the data needs (freshness, personalization,
// cache TTL) instead of hard-coding a per-component mode.
type RenderingContract struct {
	// Mode pins the strategy explicitly. When nil the planner decides
	// from the other characteristics.
	Mode *RenderMode

	// CacheTTL is the revalidation window. Zero means no revalidation
	// on its own.
	CacheTTL time.Duration

	// Public marks an island as shareable across users — a requirement
	// for server-side caching.
	Public bool

	// Personalized marks an island as depending on the viewer. Such
	// islands are never cached server-side.
	Personalized bool

	// RequiresSEO marks an island as important to crawlers, which favors
	// server-side HTML over a client placeholder.
	RequiresSEO bool

	// Freshness expresses how realtime the data must be. Decides whether
	// the island re-renders per request or can be cached/revalidated.
	Freshness Freshness
}

// RenderDecision is the planner output for one island: which strategy to
// use and, for revalidation, the TTL.
type RenderDecision struct {
	Mode     RenderMode
	CacheTTL time.Duration
}

// Planner decides how an island renders. Implementations are the rendering
// policy table: they map contracts (and context) to decisions.
type Planner interface {
	Decide(contract RenderingContract, ctx RenderingContext) RenderDecision
}

// ContractPlanner is the default policy. It is characteristic-driven — it
// switches on *what the data needs*, never on component names, so new
// islands are covered without touching the policy:
//
//	realtime + personalized → ModeClient (fetch on the client)
//	realtime                 → ModeDynamic (fresh per request)
//	TTL set                  → ModeRevalidate (cache + revalidate)
//	otherwise                → ModeStatic (render once, cache forever)
//
// An explicit contract.Mode always wins. Handlers that need a different
// policy can provide their own Planner via Composer.WithPlanner.
type ContractPlanner struct{}

func (ContractPlanner) Decide(contract RenderingContract, _ RenderingContext) RenderDecision {
	if contract.Mode != nil {
		return RenderDecision{
			Mode:     *contract.Mode,
			CacheTTL: contract.CacheTTL,
		}
	}

	switch {
	case contract.Freshness == FreshnessRealtime && contract.Personalized:
		return RenderDecision{Mode: ModeClient}
	case contract.Freshness == FreshnessRealtime:
		return RenderDecision{Mode: ModeDynamic}
	case contract.CacheTTL > 0:
		return RenderDecision{Mode: ModeRevalidate, CacheTTL: contract.CacheTTL}
	default:
		return RenderDecision{Mode: ModeStatic}
	}
}

// Mode returns a pointer to m for explicitly pinning a component's strategy
// via RenderingContract.Mode.
func Mode(m RenderMode) *RenderMode { return &m }

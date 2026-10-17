package render

import (
	"context"
	"errors"
	"expvar"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Per-island render statistics: how often each island rendered, degraded to
// its fallback, timed out or failed, and how long rendering it took in total.
// Cardinality is bounded by the curated island-name set (ValidateIslandName):
// Client-mode placeholders are deliberately excluded, because their names may
// embed ids (detail.<id>.stock) and would put an unbounded label set into the
// metrics.
//
// Published as the expvar Func "gogo_render_islands" and rendered into
// /metrics by the gateway, next to the cache hit/miss counters in cache.go.

// Render outcomes recorded per island.
const (
	resultOK      = "ok"
	resultTimeout = "timeout"
	resultError   = "error"
)

// DefaultIslandTimeout is the timeout a streamed island is given when it does
// not declare one. It follows Tailor's default for its fragments
// (https://github.com/zalando/tailor): a fragment may not hold the document
// hostage. Buffered islands keep their historical no-timeout behaviour
// (docs/ADR_FRAGMENT_DELIVERY_AND_MEASUREMENT.md §10 records the trigger to
// revisit that).
const DefaultIslandTimeout = 3000 * time.Millisecond

// islandStat accumulates one island name's counters.
type islandStat struct {
	renders    uint64
	fallbacks  uint64
	timeouts   uint64
	errors     uint64
	durationNs uint64
}

var (
	islandStatsMu sync.Mutex
	islandStats   = map[string]*islandStat{}
)

func init() {
	expvar.Publish("gogo_render_islands", expvar.Func(func() any {
		return IslandCounters()
	}))
}

// IslandStat is an immutable snapshot of one island's server-render counters.
type IslandStat struct {
	Renders   uint64
	Fallbacks uint64
	Timeouts  uint64
	Errors    uint64
	// DurationSeconds is the island's total render time, emitted by
	// /metrics as a counter-style sum (Renders is its implicit count).
	DurationSeconds float64
}

// IslandCounters returns a snapshot of the per-island render statistics,
// keyed by island name.
func IslandCounters() map[string]IslandStat {
	islandStatsMu.Lock()
	defer islandStatsMu.Unlock()

	out := make(map[string]IslandStat, len(islandStats))
	for name, s := range islandStats {
		out[name] = IslandStat{
			Renders:         s.renders,
			Fallbacks:       s.fallbacks,
			Timeouts:        s.timeouts,
			Errors:          s.errors,
			DurationSeconds: float64(s.durationNs) / 1e9,
		}
	}
	return out
}

// recordIsland counts one finished island render under its island family.
//
// The family — the name before the first ':' — is the aggregation key, not
// the full name: paged listings render under one name per page and search
// term (home.product-grid:t<hash>:p3), and a search term is attacker-controlled
// input, so the full name is an unbounded label. The family is the useful
// metric anyway: the question is which island is slow or failing, not which
// filter string a request happened to carry.
//
// result classifies the render (resultOK, resultTimeout, resultError) and the
// fallback flag marks that the island degraded to its substitute. The two are
// independent: a substituted render is counted under its cause *and* as a
// fallback, so the fallback rate decomposes into errors and timeouts.
func recordIsland(name string, duration time.Duration, result string, fallback bool) {
	family := islandFamily(name)

	islandStatsMu.Lock()
	defer islandStatsMu.Unlock()

	s := islandStats[family]
	if s == nil {
		s = &islandStat{}
		islandStats[family] = s
	}
	s.renders++
	s.durationNs += uint64(duration.Nanoseconds())

	switch result {
	case resultTimeout:
		s.timeouts++
	case resultError:
		s.errors++
	}
	if fallback {
		s.fallbacks++
	}
}

// islandFamily trims an island name to its aggregation key: the prefix
// before the first ':'. A name without one is its own family.
func islandFamily(name string) string {
	if i := strings.IndexByte(name, ':'); i >= 0 {
		return name[:i]
	}
	return name
}

// islandResult classifies a failed render for the counters: a deadline that
// fired is a timeout — the distinct signal an operator watching fallback
// rates needs — everything else is an error.
func islandResult(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return resultTimeout
	}
	return resultError
}

// islandTiming is one island's contribution to Server-Timing. A zero-value
// timing (empty name) means the island is not reported at all.
type islandTiming struct {
	name     string
	duration time.Duration
	fallback bool
}

// appendTiming adds a timing to the report, skipping islands that opt out
// (Client-mode placeholders; see renderIslandRegion).
func appendTiming(timings []islandTiming, timing islandTiming) []islandTiming {
	if timing.name == "" {
		return timings
	}
	return append(timings, timing)
}

// serverTimingHeader renders island timings as one Server-Timing field value,
// e.g. island-home.product-grid;dur=12.3, island-detail.reviews;dur=4.0.
// The metric name is sanitized to the HTTP token charset — island names may
// carry ':' (a paged listing's per-page keys), which is not a token
// character, and an invalid entry would make the whole field unparsable.
func serverTimingHeader(timings []islandTiming) string {
	parts := make([]string, 0, len(timings))
	for _, t := range timings {
		entry := fmt.Sprintf("island-%s;dur=%.1f", timingName(t.name), float64(t.duration.Microseconds())/1000)
		if t.fallback {
			entry += `;desc="fallback"`
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, ", ")
}

// timingName maps an island name into Server-Timing's token charset
// (RFC 9110 tchar): letters, digits and !#$%&'*+-.^_`|~ stay, everything else
// becomes '_'.
func timingName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
			return r
		default:
			return '_'
		}
	}, name)
}

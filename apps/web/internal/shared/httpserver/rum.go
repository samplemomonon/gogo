package httpserver

import (
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"gogo/internal/shared/web/render"
)

// Real-user monitoring: the beacon receiver for client-side web-vitals.
//
// The endpoint contract is recorded in
// docs/ADR_FRAGMENT_DELIVERY_AND_MEASUREMENT.md §5.4: POST only, JSON only,
// 4 KiB body cap, no auth, no cookies, no identifiers beyond the aggregated
// labels. The browser side lives in shell/partials/scripts.go (WebVitalsJS)
// and attributes each metric to the nearest [data-island] element, which the
// composer emits around every server island.
//
// Aggregates are `metric|island|rating → {count, value_sum}` keyed by island
// *family*: paged listings render one island per page and search term
// (home.product-grid:t<hash>:p2) and a search term is attacker-controlled
// input, so the full name would be an unbounded label. Anything the island
// grammar rejects, and any family beyond rumFamilyCap distinct labels,
// aggregates under "unknown" — the count is itself the drift signal.

const (
	// rumMaxBody caps the beacon body; a vitals payload is a few hundred
	// bytes, so anything near this is malformed or hostile.
	rumMaxBody = 4 << 10
	// rumMaxIsland caps the island label length before validation; island
	// names are element ids and cache keys, never sentences.
	rumMaxIsland = 96
	// rumFamilyCap bounds the number of distinct island families the
	// aggregator keeps. Real families are a curated handful; crossing the
	// cap means drift or abuse, and every further newcomer buckets into
	// "unknown" instead of growing memory.
	rumFamilyCap = 64
)

// rumMetricSet is the closed set of metrics the vendored web-vitals build
// reports. A metric outside it means the client is not the vendored script,
// so the request is rejected rather than aggregated.
var rumMetricSet = map[string]bool{
	"LCP":  true,
	"INP":  true,
	"CLS":  true,
	"FCP":  true,
	"TTFB": true,
}

// rumRatingSet is web-vitals' own rating vocabulary, plus the bucket unknown
// for anything else.
var rumRatingSet = map[string]bool{
	"good":              true,
	"needs-improvement": true,
	"poor":              true,
}

// rumPayload is the beacon body. Fields are validated, not trusted; unknown
// fields are ignored so a web-vitals upgrade adding one cannot break the
// endpoint. path, delta, id and navigationType are accepted for wire
// compatibility with the vendored web-vitals build and deliberately neither
// stored nor logged — the ratified aggregate schema is metric|island|rating,
// and nothing raw may be persisted here.

type rumPayload struct {
	Metric         string  `json:"metric"`
	Value          float64 `json:"value"`
	Rating         string  `json:"rating"`
	Delta          float64 `json:"delta"`
	ID             string  `json:"id"`
	NavigationType string  `json:"navigationType"`
	Island         string  `json:"island"`
	Path           string  `json:"path"`
}

// rumStat accumulates one metric|island|rating series.
type rumStat struct {
	count    uint64
	valueSum float64
}

var (
	rumStatsMu sync.Mutex
	rumStats   = map[string]*rumStat{}
	// rumFamilies remembers the island families admitted so far, so the
	// cap is enforced over time and not just per request.
	rumFamilies = map[string]bool{}
)

func init() {
	expvar.Publish("gogo_rum_web_vitals", expvar.Func(func() any {
		return RUMSnapshot()
	}))
}

// RUMStat is an immutable snapshot of one metric|island|rating series.
type RUMStat struct {
	Count    uint64
	ValueSum float64
}

// RUMSnapshot returns the RUM aggregates, keyed "metric|island|rating".
func RUMSnapshot() map[string]RUMStat {
	rumStatsMu.Lock()
	defer rumStatsMu.Unlock()

	out := make(map[string]RUMStat, len(rumStats))
	for key, s := range rumStats {
		out[key] = RUMStat{Count: s.count, ValueSum: s.valueSum}
	}
	return out
}

// rumHandler receives web-vitals beacons. It answers 204 on acceptance and
// a plain HTTP error otherwise; the beacon sender ignores the response either
// way, so nothing here is worth rendering.
func rumHandler() http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, rumMaxBody)
		var payload rumPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid JSON payload", http.StatusBadRequest)
			return
		}

		if !rumMetricSet[payload.Metric] {
			http.Error(w, "unknown metric", http.StatusBadRequest)
			return
		}

		rating := payload.Rating
		if !rumRatingSet[rating] {
			rating = "unknown"
		}

		recordRUM(payload.Metric, rumIsland(payload.Island), rating, payload.Value)

		w.WriteHeader(http.StatusNoContent)
	})
}

// recordRUM aggregates one accepted beacon under a capped island label set.
func recordRUM(metric, island, rating string, value float64) {
	rumStatsMu.Lock()
	defer rumStatsMu.Unlock()

	if island != "unknown" && !rumFamilies[island] {
		if len(rumFamilies) >= rumFamilyCap {
			island = "unknown"
		} else {
			rumFamilies[island] = true
		}
	}

	key := metric + "|" + island + "|" + rating
	s := rumStats[key]
	if s == nil {
		s = &rumStat{}
		rumStats[key] = s
	}
	s.count++
	s.valueSum += value
}

// rumIsland normalizes the client-reported island name into a bounded label.
//
// The rule, in order: "page" (or empty) stays "page"; anything that does not
// pass the island-name grammar is "unknown"; and the label is the name's
// *family* — the part before the first ':', which drops paged coordinates
// and cache keys. Digit-only dotted segments collapse to {id}, so a
// name keyed by product id (detail.123.stock) aggregates as
// (detail.{id}.stock) instead of one label per product. Names without a ':'
// and without digit segments are their own family.
func rumIsland(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "page" {
		return "page"
	}
	if len(raw) > rumMaxIsland || render.ValidateIslandName(raw) != nil {
		return "unknown"
	}

	family := raw
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		family = raw[:i]
	}

	segments := strings.Split(family, ".")
	for i, segment := range segments {
		// Segment 0 is the owner and is guaranteed non-numeric by the
		// island grammar; only later segments can be an embedded id.
		if i > 0 && isDigits(segment) {
			segments[i] = "{id}"
		}
	}
	return strings.Join(segments, ".")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// rumRows renders the RUM aggregates as Prometheus text rows. Sorted, so the
// output is stable and diffable.
func rumRows() []string {
	snapshot := RUMSnapshot()
	if len(snapshot) == 0 {
		return nil
	}

	rows := []string{
		"# HELP gogo_rum_web_vitals_count Client web-vitals beacons by metric, island family and rating.",
		"# TYPE gogo_rum_web_vitals_count counter",
		"# HELP gogo_rum_web_vitals_value_sum Sum of reported web-vitals values by metric, island family and rating.",
		"# TYPE gogo_rum_web_vitals_value_sum counter",
	}

	keys := make([]string, 0, len(snapshot))
	for key := range snapshot {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) != 3 {
			continue
		}
		stat := snapshot[key]
		rows = append(rows,
			fmt.Sprintf("gogo_rum_web_vitals_count{metric=%q,island=%q,rating=%q} %d", parts[0], parts[1], parts[2], stat.Count),
			fmt.Sprintf("gogo_rum_web_vitals_value_sum{metric=%q,island=%q,rating=%q} %v", parts[0], parts[1], parts[2], stat.ValueSum),
		)
	}
	return rows
}

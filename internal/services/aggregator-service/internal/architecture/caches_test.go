package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// OD-2: may this service hold a cache or a read model? Decided.
//
// # The decision
//
// Derived, non-authoritative state is allowed. Authoritative state — anything
// this service would be the only holder of, which would then have to be kept in
// sync with the service that owns the truth — is not.
//
// The distinction is not a stylistic preference. It is the whole failure mode.
// The catalogue has exactly one writer, product-service, and ARCHITECTURE.md
// D-018 exists because two authorities for one row drift. This service sits in
// front of all five catalogue services with ten write mutations. A cache makes
// no failure; a datastore makes a silent second authority, and the API keeps
// working while the two disagree.
//
// # Why the interesting half is now decidable
//
// When OD-2 was opened, the honest answer was that the evidence cut both ways and
// the industry draws the line inconsistently: Microsoft's CQRS guidance calls a
// materialized view "in effect a durable read-only cache of the data" that can
// be deleted and replayed, Kleppmann defines derived data by its rebuildability,
// Redis documents eviction as safe because entries are copies — and Netflix's
// RAW Hollow is called "an in-memory, co-located, compressed object database"
// while the Cassandra layer it replaced was called a database.
//
// What settles it is not a better argument. It is a fact about how caching works
// at a composition layer, from the closest published analogue there is: Apollo's
// GraphOS Router caches upstream subgraph responses in Redis and is the reference
// implementation of doing this well. Its TTL is not invented by the router. The
// router reads `Cache-Control: max-age` from the origin, derived from the origin's
// own `@cacheControl` directive, and where a write must be visible immediately it
// needs `@cacheTag` plus an authenticated invalidation endpoint that the owning
// service calls.
//
// Freshness is declared by the party that owns the data, never by the layer that
// caches it. A composition layer cannot derive a TTL for a hierarchy it does not
// own — this repository's own RequestCache comment says the same thing before any
// of this was written: "a longer-lived cache would need invalidation this service
// has no way to do correctly — the hierarchy belongs to category-service, not to
// the aggregator."
//
// # What that means here, today
//
// No service in this repository declares freshness. There is no Cache-Control, no
// max-age and no cache-tag anywhere in the five upstream services, because none
// of them has been asked to. So the precondition for caching upstream data across
// requests is unmet, and the constraint below is active rather than theoretical.
//
// The rule is therefore: upstream-derived data may be held for the duration of
// one request, and not beyond, until an owning service declares freshness. When
// that happens this file is re-decided — the failure message says so, rather than
// being a rule somebody relaxes because a test got in the way.
//
// # What this file enforces, and what it cannot
//
// It cannot tell a cache from a read model by looking, and it does not try to.
// It enforces two things that are knowable: every cache in this module is a
// declared one, so adding a fifth is an explicit act rather than a diff nobody
// reads; and no declared cache holds upstream-derived data for longer than a
// request.
//
// The authoritative-state half of the decision is enforced elsewhere and is not
// weaker for being in another file: `database/sql` is forbidden in
// architecture_test.go, which is the single choke point every pure-Go SQL path
// passes through.

// cacheDerivation records where a cache's contents come from, which is what
// decides whether holding it is cheap or dangerous.
type cacheDerivation string

const (
	// fromCaller: the caller supplied it. A parsed document, the text behind an
	// APQ hash. Deleting the cache loses nothing that was not sent again.
	fromCaller cacheDerivation = "caller"
	// fromUpstream: another service produced it. This is derived data, but its
	// staleness is governed by an owner that does not know the cache exists.
	fromUpstream cacheDerivation = "upstream"
)

// cacheLifetime records how long a cache outlives the request that filled it.
type cacheLifetime string

const (
	lifetimeRequest      cacheLifetime = "request"
	lifetimeProcess      cacheLifetime = "process"
	lifetimeCrossProcess cacheLifetime = "cross-process"
)

// declaredCache is one cache this module is allowed to have.
//
// Every field is load-bearing. `why` exists because a table of bare names is
// indistinguishable from a list of things someone forgot to justify.
type declaredCache struct {
	name     string
	derived  cacheDerivation
	lifetime cacheLifetime
	why      string
}

// declaredCaches is every cache in this module.
//
// It is exhaustive by construction: TestAggregatorCachesAreDeclared fails when
// the source contains a cache constructor that is not in this table, and when
// the table names one that no longer exists. So it cannot rot into a permanent
// pardon, which is the failure mode that makes allowlists decay — see
// ratchet_test.go, which reaches the same conclusion from the other direction.
//
// Adding an entry is the act of accepting a cache. That is deliberate: a cache
// added without a row here is a cache nobody decided on.
var declaredCaches = []declaredCache{
	{
		name:     "newDocumentCache",
		derived:  fromCaller,
		lifetime: lifetimeProcess,
		why: "Parsed query documents. The caller sent the document, so a cached copy is a " +
			"copy of the request rather than of the catalogue — evicting it loses no fact " +
			"about the system. The bound is lruDefaultSize because the key is caller-supplied.",
	},
	{
		name:     "newPersistedQueryCache",
		derived:  fromCaller,
		lifetime: lifetimeProcess,
		why: "Automatic Persisted Queries stores query text behind a client-supplied hash. " +
			"The APQ extension verifies the hash matches the text before Add, so a caller " +
			"cannot poison it with a document that does not match its own key.",
	},
	{
		name:     "RequestCache",
		derived:  fromUpstream,
		lifetime: lifetimeRequest,
		why: "Memoises the category hierarchy for one request, with single-flight so two " +
			"sibling fields marshalled at once do not both miss. Request-scoped on purpose: " +
			"a longer-lived cache would need invalidation this service has no way to do " +
			"correctly, because the hierarchy belongs to category-service.",
	},
}

// cacheConstructorPattern finds cache constructors by the naming convention this
// module already follows: new…Cache.
var cacheConstructorPattern = regexp.MustCompile(`^new[A-Za-z0-9]*Cache$`)

// findCacheConstructors returns every cache constructor defined in this module's
// production code.
//
// Parsed rather than grepped so that the set is exact: matching a call site would
// also count every use of a constructor, and matching a bare string would count
// this file's own table. Definitions only, in non-test files, excluding gen/.
//
// `RequestCache` is a struct rather than a constructor, so it is not found here.
// It is in the table anyway, and TestDeclaredCachesAllExist checks it is still
// present in graph/, which is what keeps that entry from quietly becoming a
// description of a type that was renamed away.
func findCacheConstructors(t *testing.T) []string {
	t.Helper()

	root := moduleRoot(t)
	fset := token.NewFileSet()

	var found []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		switch {
		case d.IsDir():
			// rel is "." for the root itself, which must not be skipped as hidden.
			// gen/ is protobuf output and .devenv is a module cache; neither is
			// hand-written architecture, and both would dwarf the real signal.
			hidden := rel != "." && strings.HasPrefix(rel, ".")
			if rel == "gen" || hidden || strings.Contains(rel, ".devenv") {
				return filepath.SkipDir
			}
			return nil
		case !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !cacheConstructorPattern.MatchString(fn.Name.Name) {
				continue
			}
			found = append(found, fn.Name.Name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	sort.Strings(found)
	return found
}

// TestAggregatorCachesAreDeclared closes the loop on the table.
//
// Two failures, and the second is what keeps it from being a list that rots:
//
//   - a constructor in the source that the table does not declare → somebody
//     added a cache, and it is not derived or bounded until they say so;
//   - a table entry with no constructor behind it → the cache is gone, and the
//     row would otherwise go on describing a thing that no longer exists.
func TestAggregatorCachesAreDeclared(t *testing.T) {
	declared := make(map[string]declaredCache, len(declaredCaches))
	for _, c := range declaredCaches {
		declared[c.name] = c
	}

	found := findCacheConstructors(t)
	if len(found) == 0 {
		t.Fatal("no cache constructor was found, so this test asserts nothing — " +
			"either the naming convention changed or the AST walk is broken")
	}

	for _, name := range found {
		if _, ok := declared[name]; !ok {
			t.Errorf("cache constructor %s is not declared in declaredCaches.\n\n"+
				"  This service now holds a cache nobody decided on. Add a row recording what it "+
				"derives from (caller or upstream), how long it lives (request, process or "+
				"cross-process), and why that is safe. If it is upstream-derived and outlives "+
				"the request, see the OD-2 rationale at the top of this file first — no upstream "+
				"service in this repository declares freshness, so a TTL would have to be invented.",
				name)
		}
	}

	for _, c := range declaredCaches {
		switch c.name {
		case "RequestCache":
			// A struct, not a constructor; presence is checked separately below.
			continue
		}
		if !contains(found, c.name) {
			t.Errorf("declaredCaches names %s, which no longer exists in this module.\n"+
				"  A stale row is not harmless bookkeeping: it pre-approves a cache that has "+
				"  been removed, so removing the cache and keeping the row would let the next "+
				"  change that reaches for it pass unchallenged.", c.name)
		}
		if strings.TrimSpace(c.why) == "" {
			t.Errorf("declaredCaches entry %s has no justification.", c.name)
		}
	}
}

// TestAggregatorDoesNotCacheUpstreamDataAcrossRequests is the constraint the
// decision turns on.
//
// Upstream-derived data may be held for one request. Holding it longer requires
// freshness declared by the owner of the data, and no service in this repository
// declares any — so this is enforced, not merely intended.
//
// If this ever needs to change, the change is a decision about invalidation
// authority, not a test to relax: the owning service has to start emitting
// Cache-Control or a cache-tag channel first.
func TestAggregatorDoesNotCacheUpstreamDataAcrossRequests(t *testing.T) {
	for _, c := range declaredCaches {
		if c.derived != fromUpstream || c.lifetime == lifetimeRequest {
			continue
		}
		t.Errorf("%s holds upstream-derived data with lifetime %q.\n\n"+
			"  Derived, non-authoritative state is allowed here (OD-2). Holding upstream data "+
			"longer than one request is a separate question, and its answer is currently no: "+
			"no service in this repository declares how fresh its data is.\n"+
			"  Apollo's router — the reference implementation of a caching composition layer — "+
			"takes its TTL from the origin's Cache-Control and needs an authenticated "+
			"invalidation endpoint from the owning service for writes. This service has no way "+
			"to do either correctly, because the hierarchy belongs to category-service, not here.\n"+
			"  To lift this: have the owning service declare freshness, then re-decide OD-2.",
			c.name, c.lifetime)
	}
}

// TestDeclaredCachesAllExist is the presence half for the one entry that is a
// type rather than a constructor.
func TestDeclaredCachesAllExist(t *testing.T) {
	root := moduleRoot(t)

	// RequestCache is the upstream-derived entry, and the only one whose removal
	// would silently drop the constraint above it, so its presence is checked
	// rather than assumed.
	path := filepath.Join(root, "graph", "resolvers_common.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if !strings.Contains(string(data), "type "+declaredCacheByName(t, "RequestCache").name+" struct") {
		t.Errorf("declaredCaches names RequestCache, but graph/resolvers_common.go no longer " +
			"declares it. It is the only upstream-derived cache in the table, so removing it " +
			"without updating this file would leave TestAggregatorDoesNotCacheUpstreamDataAcrossRequests " +
			"asserting nothing.")
	}
}

func declaredCacheByName(t *testing.T, name string) declaredCache {
	t.Helper()

	for _, c := range declaredCaches {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("declaredCaches has no entry named %s", name)
	return declaredCache{}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

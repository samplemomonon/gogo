// Package architecture_test pins the properties this service is allowed to
// have, and states where each rule comes from.
//
// # Why this file exists
//
// The aggregator's own README and main.go both say the same thing:
//
//	"GraphQL composition layer in front of the five platform services.
//	 It owns no data and no database."
//	 — README.md:1-5
//
//	"Everything it answers is read from another service at request time, which
//	 is the property that keeps it from becoming a sixth place where the
//	 catalogue has to be kept in sync."
//	 — cmd/server/main.go:1-6
//
// That property is load-bearing: the product catalogue has exactly one writer,
// product-service, and ARCHITECTURE.md D-018 exists because two authorities for
// one row drift. This service sits in front of all five catalogue services with
// ten write mutations. The moment it acquires a datastore it becomes a sixth
// place where the catalogue can be kept — and nothing about that failure would
// be loud. The API keeps working, the schema keeps working, the tests pass.
//
// # What "owns no data" means here, and what it does not
//
// Derived, disposable, read-only state is not a datastore, and this rule does
// not forbid it. Microsoft's CQRS guidance calls a materialized read model "in
// effect a durable read-only cache of the data", and states that because the
// event store is the source of information "it is possible to delete the
// materialized views and replay all past events to create a new representation
// of the current state". Kleppmann puts the same thing as derived data:
// "if you lose your cache, you can rebuild it from the underlying database; thus,
// the contents of the cache are derived from the database."
//
// So a cache or a read projection is allowed here, and the rule is about
// *authoritative* state: something this service is the only holder of, which
// would have to be kept in sync with another service.
//
// The same distinction is not academic in practice. Tokopedia's GraphQL gateway
// — structurally the closest published analogue to this service, fanning one
// page out to five upstream APIs — caches upstream data in Redis and describes
// it as caching, not as data it owns.
//
// # READ THIS BEFORE CHANGING WHAT THE RULE FORBIDS
//
// Both open decisions in this file are now closed. See
// docs/ADR_AGGREGATOR_BOUNDARIES.md for the reasoning and the evidence.
//
//   - OD-2: may this service ever hold a cache or a read model? DECIDED —
//     derived, non-authoritative state is allowed, and this file's single rule
//     is exactly that boundary rather than an accident of narrowness. Holding
//     upstream data beyond one request is separately refused, because no
//     service in this repository declares how fresh its data is. The caches this
//     service actually has are declared and checked in caches_test.go, so what
//     happens today is intended rather than merely permitted.
//
//   - OD-1: should enforcement be a fail-closed allowlist (depguard strict plus
//     forbidigo on sql.Open) rather than a one-item denylist? DECIDED — not
//     adopted, because an allowlist is a hand-maintained list and this
//     repository has three recorded instances of hand-maintained lists rotting.
//     The adopted mechanism is a two-way ratchet in ratchet_test.go, plus the
//     choke-point rule below, which closes the driver-arrival risk forbidigo was
//     proposed for without a new toolchain dependency.
//
// # What IS settled, and stated in this service's own README.md and
// cmd/server/main.go, is that it holds no authoritative state. This file
// enforces that, and nothing beyond it.
//
// # What is NOT checked, and why
//
// # 1. A denylist of database libraries.
//
// An earlier draft of this file denied ~19 module roots: pgx, lib/pq, gorm, ent,
// badger, buntdb, goleveldb, go-redis, etcd, bbolt, goose, golang-migrate and
// others. That list was written from memory, not from evidence, and research
// against published composition layers found that none of those items could be
// corroborated as present in any real one — no Go-language composition layer was
// found at all. Redis, the one capability with real support behind it, appears in
// Tokopedia's gateway as legitimate *caching*.
//
// A denylist is also the wrong shape. It is fail-open: every new driver, every
// vendored transitive dependency and every import alias is permitted until
// someone remembers to add it, and it must be maintained forever. depguard's own
// documentation frames deny-lists reactively — containerd's config comment reads
// "Checks for dependencies that should not be (re)introduced" — and its strict,
// allow-only mode is the documented fail-closed alternative.
//
// So this file denies the one import that is the actual choke point (see below)
// and enumerates no library names.
//
// # 2. A migrations directory.
//
// Also dropped. A migration tool is a function call; it does not need a directory,
// so the check is trivially bypassed and would only prove that nobody created a
// folder with a conventional name.
//
// # 3. Runtime assertions and network policy.
//
// Blocking database DSNs at boot, or denying egress on 5432, are sound
// complementary controls. None of them run in a pull request or on a laptop, so
// none of them is a substitute for a static check.
//
// # What replaced the proposed allowlist
//
// OD-1 proposed a fail-closed depguard strict rule plus forbidigo on
// ^sql\.(Open|OpenDB)$, and proposed it for this file. It was declined, and the
// reasoning is in ratchet_test.go: an allowlist is hand-maintained, and every
// hand-maintained list in this repository has decayed — nineteen module roots
// none of which existed, forty-four BUILD files for an uninstalled build system,
// an import graph that had rotted to a third of the repository.
//
// The specific risk OD-1 was raised about — a new database driver arriving
// unchallenged — is already closed here, and closed by construction rather than
// by enumeration: database/sql is the single choke point every pure-Go SQL path
// passes through, so a driver that does not exist yet still has nowhere to enter.
// forbidigo on the same two function names would reimplement, with a new
// toolchain dependency, a check one import line already performs.
package architecture_test

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// modulePrefix is this module's own import-path prefix.
const modulePrefix = "aggregator-service/"

// cgoPseudoPackage is the synthetic import that cgo-using packages report and
// `go list -deps` does not list. It reaches this module transitively through
// runtime/cgo -> net and os/user, and is not a package in any sense the rules
// here care about.
const cgoPseudoPackage = "C"

// forbiddenImports are imports this service's production code may not make.
//
// There is exactly one, and the choice is deliberate rather than an oversight.
//
// Every pure-Go SQL path — Postgres, MySQL, SQLite, and every driver built on
// them — funnels through database/sql, which provides sql.Open and sql.DB. That
// makes it the single choke point: one rule, one import path, one function,
// and it covers drivers that do not exist yet.
//
// It is also standard library, which no denylist of third-party module roots can
// match. That is precisely the gap the earlier ~19-item list had.
//
// Exact match, not prefix. database/sql/driver is interface-only — Valuer,
// Scanner, NamedValue — and github.com/google/uuid imports it for the sole
// purpose of asserting that UUID implements driver.Valuer. OpenTelemetry's SDK
// pulls uuid in, so this service reaches database/sql/driver today. A prefix rule
// would fire on a service with no database; an earlier draft of this file did
// exactly that, which is what a boundary rule that cries wolf teaches people to
// do — delete it.
var forbiddenImports = map[string]string{
	"database/sql": "this service owns no data; derive read state from upstream services, do not persist it. " +
		"If a datastore is genuinely needed here, that is an architecture decision, not a PR.",
}

// goListPackage is the subset of `go list -deps -json` this file needs.
type goListPackage struct {
	ImportPath string
	Standard   bool

	// Imports is the package's import set.
	//
	// ForTest is set on the synthetic test variant `go list` emits for a
	// dependency: the same ImportPath, but with the test files' imports included.
	// Both entries arrive with the same ImportPath, so collecting them into a
	// map naively lets whichever arrives last overwrite the other. That is not
	// cosmetic — the test variant of go.opentelemetry.io/otel/metric imports
	// database/sql, and letting it through would make this file fail on a
	// dependency the production code cannot reach.
	ForTest string

	Imports []string

	// path is the import chain that reached this package, filled in after the
	// walk. Used only to explain a violation.
	path string
}

// reachable returns every package this module's production code can reach by
// importing, transitively.
//
// It is a breadth-first walk from this module's own packages, following Imports
// only — never TestImports, per the note on goListPackage. Following the whole
// closure, not just this module's packages, because the offending import may
// arrive through a helper. The standard library is included, because the import
// that matters most is stdlib.
func reachable(t *testing.T) []goListPackage {
	t.Helper()

	cmd := exec.Command("go", "list", "-deps", "-json", "./...")
	cmd.Dir = moduleRoot(t)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("piping go list: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting go list: %v", err)
	}

	byPath := make(map[string]goListPackage)
	dec := json.NewDecoder(stdout)
	for {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			cmd.Wait()
			t.Fatalf("decoding go list output: %v", err)
		}
		// Keep the real package, not its test variant: both carry the same
		// ImportPath, so last-wins would be whichever go list emitted last.
		if existing, seen := byPath[pkg.ImportPath]; !seen || (existing.ForTest != "" && pkg.ForTest == "") {
			byPath[pkg.ImportPath] = pkg
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("go list: %v", err)
	}

	seen := make(map[string]bool)
	from := make(map[string]string) // child -> the importer that reached it
	var order []goListPackage

	var walk func(string)
	walk = func(path string) {
		if seen[path] {
			return
		}
		pkg, ok := byPath[path]
		if !ok {
			if path == cgoPseudoPackage {
				seen[path] = true
				return
			}
			t.Fatalf("%s is imported but absent from `go list -deps`; the dependency walk is incomplete", path)
		}
		seen[path] = true
		order = append(order, pkg)
		for _, dep := range pkg.Imports {
			from[dep] = path
			walk(dep)
		}
	}
	for path := range byPath {
		if strings.HasPrefix(path, modulePrefix) {
			walk(path)
		}
	}

	// A guard on the guard. Every rule here is phrased as "this must not
	// exist", so a walk returning almost nothing would turn this file into a
	// test that always passes — the one outcome it cannot be allowed to have.
	var own, external int
	for _, pkg := range order {
		if strings.HasPrefix(pkg.ImportPath, modulePrefix) {
			own++
		} else {
			external++
		}
	}
	if own == 0 {
		t.Fatalf("the walk found no packages under %q, so it asserts nothing", modulePrefix)
	}
	if external == 0 {
		t.Errorf("the walk followed no edges out of this module, so it could not detect a forbidden "+
			"import arriving through a helper — %d own packages were seen, which means Imports "+
			"was probably empty", own)
	}

	// from is retained for pathOf, which turns a flagged import into the chain
	// that introduced it. A guard that says "database/sql is not allowed" without
	// saying which helper dragged it in sends the reader to grep instead of
	// answering.
	for i := range order {
		order[i].path = pathOf(order[i].ImportPath, from)
	}
	return order
}

// pathOf walks `from` backwards to build the import chain that reached target.
func pathOf(target string, from map[string]string) string {
	var chain []string
	for n := target; n != ""; n = from[n] {
		chain = append(chain, n)
		if len(chain) > 32 {
			break // cycle guard; a real cycle would already have failed go list
		}
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return strings.Join(chain, " -> ")
}

// moduleRoot locates this module's directory, so the test runs from wherever
// `go test` places it.
func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the aggregator-service module root")
		}
		dir = parent
	}
}

// TestAggregatorOwnsNoDatastore is the tripwire.
//
// It is a tripwire rather than a design discussion: this service's one
// non-negotiable property is that it holds no authoritative state, and the
// cheapest way to keep a property that has been written down in prose is to make
// acquiring the thing it forbids a build failure.
func TestAggregatorOwnsNoDatastore(t *testing.T) {
	for _, pkg := range reachable(t) {
		why, forbidden := forbiddenImports[pkg.ImportPath]
		if !forbidden {
			continue
		}
		t.Errorf("%s is reachable by this service's production code.\n"+
			"  import chain: %s\n"+
			"  %s", pkg.ImportPath, pkg.path, why)
	}
}

// TestAggregatorHoldsNoDomainModel is the second half of "composition layer".
//
// No datastore and no business model together mean the service holds no model of
// the business it serves. A package that models products, categories or
// inventory here would be the first sign of it starting to answer questions it
// should be forwarding.
//
// This is the failure mode published composition layers actually report. Airbnb
// describes Viaduct's problem as "the slow creep toward monolith" — a growing
// GraphQL schema accumulated across teams — and Zalando's stated constraint on
// its GraphQL layer is a "No Business Logic" principle. Neither is about a
// datastore. A datastore would be the loud failure; this is the quiet one.
//
// The check is on the import graph rather than on a list of directory paths. A
// path allowlist guesses where the package would be put and passes silently when
// it is put somewhere else; a name match on the actual graph cannot.
func TestAggregatorHoldsNoDomainModel(t *testing.T) {
	const domainPackage = "domain"

	for _, pkg := range reachable(t) {
		name := pkg.ImportPath
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		if name != domainPackage || pkg.ImportPath == domainPackage {
			continue
		}
		t.Errorf("package %s exists in a service that holds no business model.\n"+
			"  import chain: %s\n"+
			"  This service reads from the five platform services at request time. A domain "+
			"package here is where it stops doing that and starts owning a model of the "+
			"business it forwards to — which is the shape Zalando's GraphQL layer was "+
			"constrained against with a 'No Business Logic' principle.",
			pkg.ImportPath, pkg.path)
	}
}

// TestAggregatorOwnsItsSchemaContract is the positive counterpart.
//
// "Owns no data" is not "owns nothing". The GraphQL schema is a published
// contract with ten write mutations and a real breaking-change surface, and this
// service is its sole provider. gqlgen ships no equivalent of `buf breaking`, so
// what can be checked mechanically is that the contract exists and has both
// roots.
//
// # OD-3: decided
//
// The question was what to do with three mutations — createCategory,
// updateCategory, deleteCategory — that are in the schema but cannot succeed.
// category.v1 exposes no write RPC, so all three answer DEPENDENCY_UNAVAILABLE
// unconditionally. A schema advertising surface that always fails is a contract
// lying about its own capability.
//
// The three options considered were: keep the Mutation root and justify it,
// remove it, or defer. The first two were wrong for the same reason. The
// Mutation root is not in question — createProduct, updateProduct, deleteProduct,
// registerMedia, completeMediaUpload and deleteMedia all work, and a service
// with working writes will always have a Mutation root. And removing the
// category mutations is not available: graphql.org's own governance guidance
// classes removing a field as a breaking change, which is precisely the thing to
// avoid introducing to resolve a problem caused by the field's presence.
//
// What remains is graphql.guide's answer to the underlying question — *build
// expected errors into the schema, so clients know what to handle* — combined
// with graphql.org's mechanism for a field that should not be used:
// `@deprecated` carrying a reason that names the alternative.
//
// So the fields stay, the root stays, and each of the three now declares, in the
// schema itself, that it cannot succeed and where to go instead. That is
// discoverable through ordinary introspection, which is where a client author
// looks, rather than only in this file or a README.
func TestAggregatorOwnsItsSchemaContract(t *testing.T) {
	root := moduleRoot(t)

	data, err := os.ReadFile(filepath.Join(root, "graph", "schema.graphql"))
	if err != nil {
		t.Fatalf("reading graph/schema.graphql: %v — this service's one owned artefact is its "+
			"GraphQL schema; without it the service provides no contract", err)
	}

	schema := string(data)
	if !strings.Contains(schema, "type Query") {
		t.Error("the GraphQL schema declares no Query root; the service would expose nothing")
	}

	// Six of the ten mutations reach a service that exposes the capability. The
	// Mutation root exists because they work, not because it is conventional.
	if !strings.Contains(schema, "type Mutation") {
		t.Error("the GraphQL schema declares no Mutation root.\n" +
			"  Six mutations in this schema reach a service that implements them " +
			"(createProduct, updateProduct, deleteProduct, registerMedia, completeMediaUpload, " +
			"deleteMedia). A GraphQL service with working writes has a Mutation root.")
	}

	// The other three cannot succeed, and the schema has to say so.
	for _, field := range categoryWriteMutations {
		declaration, found := mutationFieldDeclaration(schema, field)
		if !found {
			t.Errorf("Mutation.%s is not declared in the GraphQL schema.\n"+
				"  It is listed in categoryWriteMutations because it cannot succeed — "+
				"category.v1 exposes no write RPC — so either it was removed, which is a "+
				"breaking change clients may depend on, or the list here is wrong.", field)
			continue
		}
		if !strings.Contains(declaration, "@deprecated(") {
			t.Errorf("Mutation.%s cannot succeed but is not marked @deprecated in the schema.\n"+
				"  declaration: %s\n"+
				"  A mutation in the schema that always answers DEPENDENCY_UNAVAILABLE is a "+
				"contract advertising a capability it does not have. @deprecated with a reason "+
				"naming the alternative is the documented way to tell clients not to call it.",
				field, declaration)
			continue
		}
		if reason, found := deprecationReason(declaration); !found || strings.TrimSpace(reason) == "" {
			t.Errorf("Mutation.%s is @deprecated without a usable reason.\n"+
				"  declaration: %s\n"+
				"  Deprecation without a reason tells a client the field is going away and nothing "+
				"about what to use instead. The reason must say the capability is missing and name "+
				"the category service's REST API.", field, declaration)
		}
	}
}

// categoryWriteMutations are the mutations whose capability this service does not
// have. They are declared in the schema and resolve to DEPENDENCY_UNAVAILABLE.
var categoryWriteMutations = []string{"createCategory", "updateCategory", "deleteCategory"}

// deprecationReason extracts the reason argument from a @deprecated directive.
//
// Returns the text between the quotes. Checking for the presence of `reason: "`
// is not enough: an empty reason matches it too, which is the one case the
// assertion exists to catch.
func deprecationReason(declaration string) (string, bool) {
	const marker = `reason: "`
	idx := strings.Index(declaration, marker)
	if idx < 0 {
		return "", false
	}
	rest := declaration[idx+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// mutationFieldDeclaration returns the schema text declaring Mutation.<field>,
// up to and including the end of that field's declaration.
//
// Line-based, because a field's declaration is not one line: directives are
// conventionally placed on continuation lines indented further than the field
// itself. Matching on "\n  " instead of a newline boundary finds the first two
// spaces of a four-space continuation and truncates the declaration before its
// directives — which would silently pass every assertion below.
func mutationFieldDeclaration(schema, field string) (string, bool) {
	lines := strings.Split(schema, "\n")

	prefix := "  " + field + "("
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}

	declaration := []string{strings.TrimSpace(lines[start])}
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// A field declaration continues while the indentation is deeper than the
		// field's own two spaces. Anything at two spaces or less is the next field,
		// or the end of the type.
		if len(line)-len(strings.TrimLeft(line, " ")) <= 2 {
			break
		}
		declaration = append(declaration, trimmed)
	}
	return strings.Join(declaration, " "), true
}

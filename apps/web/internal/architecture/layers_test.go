package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// # Per-vertical dependency direction
//
// ARCHITECTURE.md states the hexagon rules in prose and says outright that no
// test enforces them:
//
//	"The classic hexagon rules short of the above still apply per module:
//	 presentation and infrastructure both depend on application ports, never
//	 the reverse; domain depends on nothing internal."
//
// So the architecture was right and unverified. These tests make it checked.
// The matrix below is data — layers, allowed callees — applied to whatever
// verticals build the hexagon; TestFeatureVerticalHexagonDependencyDirection in
// vertical_boundary_test.go is its one runner since D-023 Phase 7 retired
// internal/modules (the module-era runner is tombstoned below).
//
// The rules below are data rather than hand-written subtests, because they are a
// matrix: the same predicate over a list of (layer, allowed callee) pairs. That
// is the shape every comparable tool uses — dependency-cruiser's forbidden /
// allowed / required rules with an `extends` base set, Deptrac's collectors and
// ruleset, Nx's depConstraints, ArchUnit's layer descriptions. None of them
// hand-write one function per rule, because a hand-written function per rule is
// exactly how rules drift out of sync with each other.

// layerRule is one row of the dependency matrix: packages sitting in from may
// reach only packages sitting in within the same module.
type layerRule struct {
	module string // empty means "every module"

	// from is the layer directory name the rule applies to.
	from string

	// to lists the intra-module layers this layer is allowed to import. A
	// package may additionally import anything outside its module — that is a
	// different boundary, decided by the import rules in architecture_test.go.
	// to == nil means "nothing inside the module".
	to []string
}

// The layers a vertical may be built from. A vertical that does not use one of
// them is simply not constrained by that row, so a flat vertical — billing,
// ops — passes without special-casing.
var knownLayers = []string{
	"domain",
	"application",
	"application/ports",
	"infrastructure",
	"presentation",
	"compose",
}

// layerRules is the matrix. Read a row as: from may import any of to, plus
// anything outside the module.
//
// The two rows doing the most work are infrastructure and presentation. Both
// are allowed to reach application/ports and domain, and both are forbidden to
// reach application — the use-case layer. That is Dependency Inversion stated
// as an import rule: an adapter satisfies a port, it does not call a use case.
// An adapter that reached application would be able to start a transaction
// boundary or a workflow that the domain never sanctioned.
//
// domain has to == nil. A domain package that imports anything inside its own
// module is not a domain package; it is a collaborator that happens to be
// written in the right directory.
var layerRules = []layerRule{
	{from: "domain", to: nil},
	{from: "application/ports", to: []string{"domain"}},
	{from: "application", to: []string{"domain", "application/ports"}},
	{from: "infrastructure", to: []string{"domain", "application/ports"}},
	{from: "presentation", to: []string{"domain", "application/ports"}},
	// compose is the module's own composition root. It is allowed to reach
	// everything in the module, which is what makes it the composition root —
	// and it is the only layer allowed to.
	{from: "compose", to: []string{
		"domain",
		"application",
		"application/ports",
		"infrastructure",
		"presentation",
	}},
}

// ruleFor returns the rules that apply to a module/layer pair.
func rulesFor(module, layer string) []layerRule {
	var out []layerRule
	for _, r := range layerRules {
		if r.from != layer {
			continue
		}
		if r.module == "" || r.module == module {
			out = append(out, r)
		}
	}
	return out
}

// TestHexagonDependencyDirection (the module-era runner of the matrix below
// over internal/modules) was deleted in D-023 Phase 7. Every vertical now lives
// under internal/features, and TestFeatureVerticalHexagonDependencyDirection in
// vertical_boundary_test.go applies the same matrix to them; keeping a second
// runner for a retired root would be a rule with no subject.

func allowedList(layer string) string {
	for _, r := range layerRules {
		if r.from != layer {
			continue
		}
		if len(r.to) == 0 {
			return "nothing"
		}
		return strings.Join(r.to, ", ")
	}
	return "nothing"
}

// TestCompositionLayerReachesNoVerticalInternals is the machine form of the
// two-face ownership rule: the shell (the composition root) and each vertical's
// ui are the two faces that sit outside a vertical's backend, and neither owns
// the backend's vocabulary.
//
// The rule has two halves:
//
//  1. The shell may reach a vertical only through that vertical's own
//     composition root — its `compose` package, or its root package when it
//     contributes itself to the container — or through its `ui/`. It may not
//     reach into a vertical's domain, application, presentation or
//     infrastructure. Reaching in is not a Dependency Rule inversion (the shell
//     is outermost), but it costs coupling: the root ends up knowing a
//     vertical's internal wiring, which is exactly the knowledge that
//     vertical's `compose` package exists to keep to itself.
//
//  2. A vertical's `ui` reaches no backend internals — not its own, and not
//     another vertical's (the closed-module test forbids the latter outright).
//     The read-side consumes its backend over the wire contract exactly as it
//     consumes other verticals, so the two halves of one vertical still meet
//     over gRPC; lifting one out stays a config change rather than a rewrite.
//     The one cross-team surface a ui may name, `public`, does not exist yet
//     (D-021: it is extracted with its first real consumer).
//
// The pre-D-023 form of this test checked the module/pages split; the vertical
// model has one subject, so both halves are phrased on it and a retired root
// cannot pass by having nothing to check.
func TestCompositionLayerReachesNoVerticalInternals(t *testing.T) {
	skipIfDisabled(t)

	g := graphEdges()

	// matchesLayer reports which of the layers rest names, honoring the fact
	// that layers nest (infrastructure/postgres lives inside infrastructure).
	matchesLayer := func(rest string, layers []string) string {
		for _, layer := range layers {
			if rest == layer || strings.HasPrefix(rest, layer+"/") {
				return layer
			}
		}
		return ""
	}

	// These are the layers that carry a vertical's business: rules in domain,
	// use cases in application, rules again in the adapters that implement
	// them.
	backendLayers := []string{"domain", "application", "presentation", "infrastructure"}

	t.Run("the shell reaches a vertical only through its compose root or ui", func(t *testing.T) {
		for _, dep := range g["gogo/internal/shell"] {
			rest, ok := strings.CutPrefix(dep, featuresPrefix)
			if !ok {
				continue
			}
			_, sub, _ := strings.Cut(rest, "/")
			switch {
			case sub == "", sub == "ui", strings.HasPrefix(sub, "ui/"),
				sub == "compose", strings.HasPrefix(sub, "compose/"):
				continue // the vertical's entry points; wiring these is the shell's job
			}
			if layer := matchesLayer(sub, backendLayers); layer != "" {
				t.Errorf("gogo/internal/shell imports %s\n"+
					"  violated: the composition root may reach a vertical only through its own "+
					"compose package, its root package, or its ui. Reaching into %q makes the root "+
					"know the vertical's internal wiring, which is what that vertical's compose "+
					"package exists to keep to itself", dep, layer)
			}
		}
	})

	t.Run("a vertical's ui reaches no backend internals", func(t *testing.T) {
		checked := 0
		for _, feature := range featuresInGraph(g) {
			base := featuresPrefix + feature
			for _, pkg := range packagesUnder(g, base+"/ui") {
				checked++
				for _, dep := range closure(g, pkg) {
					rest, ok := strings.CutPrefix(dep, base+"/")
					if !ok {
						continue // another vertical or the platform; other rules decide
					}
					if rest == "" {
						t.Errorf("ui package %s imports its vertical's backend root %s; the read-side "+
							"consumes the backend over its gRPC contract, exactly as it consumes other "+
							"verticals (ARCHITECTURE.md rule 1; D-023)", pkg, dep)
						continue
					}
					if layer := matchesLayer(rest, append([]string{"compose"}, backendLayers...)); layer != "" {
						t.Errorf("ui package %s reaches backend internals %s\n"+
							"  violated: a vertical's ui may not import its own backend's %q — the two "+
							"halves meet over the wire contract, so a later lift-out is a config "+
							"change rather than a rewrite (D-023)", pkg, dep, layer)
					}
				}
			}
		}
		if checked == 0 {
			t.Fatal("no vertical ui packages found; the rule would assert nothing")
		}
		t.Logf("checked %d ui packages across %d verticals", checked, len(featuresInGraph(g)))
	})
}

// TestNoPlatformAggregateIsOwnedInProcess extends ARCHITECTURE.md rule 7 from
// product to every aggregate the platform owns.
//
// Rule 7 exists because two authorities for one row drift. The product
// aggregate got the rule when D-018 removed gogo's copy. This asserts the rule
// cannot be re-opened for any of the aggregates gogo is not allowed to own, and
// it is a package-existence check rather than an import check: the failure mode
// is a directory appearing, not an edge.
//
// media is here because it had exactly D-018's disease — an in-process module
// alongside the platform's media-service — and the rule did not mention it, so
// nothing stopped it. It was removed rather than ruled on, and this test is what
// keeps it removed.
//
// The check moved with the units (D-023): the tree that can hold an aggregate
// today is internal/features, and the retired roots cannot come back
// (TestRetiredRootsStayRetired), so a match here is a directory a person
// deliberately created.
func TestNoPlatformAggregateIsOwnedInProcess(t *testing.T) {
	skipIfDisabled(t)

	// Every aggregate the external platform owns. gogo may hold a client for
	// any of them and must hold state for none of them. Prefix match, so
	// `product`, `products` and `product-anything` are all refused.
	forbidden := []string{"catalog", "product", "listing", "media", "category", "search"}

	root := findGogoRoot(t)
	featuresDir := filepath.Join(root, "internal", "features")
	entries, err := os.ReadDir(featuresDir)
	if err != nil {
		t.Fatalf("reading %s: %v", featuresDir, err)
	}

	for _, e := range entries {
		for _, bad := range forbidden {
			if e.Name() == bad || strings.HasPrefix(e.Name(), bad) {
				t.Errorf("internal/features/%s exists: the platform owns this aggregate, and gogo holding a second "+
					"authority for one row is what D-011 and D-018 were written to prevent", e.Name())
			}
		}
	}
}

// TestPortNamesCarryNoTechnology is Cockburn's rule — "architect the system's
// interfaces by purpose rather than by technology" — made checkable.
//
// A port named StockGrpcClient tells a caller the transport, which couples the
// contract to a decision a caller should not depend on: replacing gRPC with
// in-process calls becomes a breaking change to every consumer. A port named
// StockAdjuster states what it is for, and both a gRPC adapter and an in-process
// one can satisfy it.
//
// The real code already obeys this — ports.StockService, ports.StockAdjuster,
// ports.Listing, ports.ProductCatalog — which is why this is a guard against a
// future port regressing rather than a fix for a present one.
func TestPortNamesCarryNoTechnology(t *testing.T) {
	skipIfDisabled(t)

	// Tokens that name a transport, a storage engine or a UI framework rather
	// than a capability.
	forbidden := []string{
		"Grpc", "Http", "Rest", "Rpc", "Sql", "Pg", "Postgres", "Kafka",
		"Templ", "Htmx", "Chi", "Client", "Server", "Handler", "Repo", "Dao",
	}

	root := findGogoRoot(t)
	fset := token.NewFileSet()

	for _, dir := range portsDirs(t, root) {
		pkgs, err := parser.ParseDir(fset, dir, nil, 0)
		if err != nil {
			continue
		}
		for _, pkg := range pkgs {
			for name, file := range pkg.Files {
				rel, relErr := filepath.Rel(root, dir)
				if relErr != nil {
					rel = dir
				}
				for _, spec := range interfaceSpecs(file) {
					checkPortName(t, rel, name, spec, forbidden)
				}
			}
		}
	}
}

// interfaceSpecs returns the exported interface names declared in one file.
//
// Only interfaces, and only exported ones, because that is where a public
// contract lives. A helper called newClient, an assertion called
// isNilServer, and a test function are implementation detail: naming them after
// a technology says nothing to a consumer, because no consumer can see them.
func interfaceSpecs(file *ast.File) []string {
	var out []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, isIface := ts.Type.(*ast.InterfaceType); !isIface {
				continue
			}
			if !ast.IsExported(ts.Name.Name) {
				continue
			}
			out = append(out, ts.Name.Name)
		}
	}
	sort.Strings(out)
	return out
}

func checkPortName(t *testing.T, pkgDir, fileName, name string, forbidden []string) {
	t.Helper()
	for _, token := range forbidden {
		if !strings.Contains(name, token) {
			continue
		}
		t.Errorf("%s: port %q names the %q technology; a port is named for the capability it "+
			"offers, so that the transport stays replaceable (hexagonal architecture: interfaces "+
			"architectured by purpose, not by technology)", filepath.Join(pkgDir, fileName), name, token)
	}
}

// portsDirs finds every package in the gateway module whose path marks it as a
// port declaration site.
//
// This walk stays inside the gateway module. Platform services live in the
// sibling top-level services/ directory and have their own architecture tests.
func portsDirs(t *testing.T, root string) []string {
	t.Helper()

	var out []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		base := d.Name()
		if base == "node_modules" || base == ".git" || base == ".devenv" || base == "vendor" {
			return filepath.SkipDir
		}
		if base == "ports" || strings.HasSuffix(base, "consumer") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatal("no ports packages found; the naming rule would assert nothing")
	}
	sort.Strings(out)
	return out
}

// graphEdges returns the committed graph, or fails the test that asked.
//
// It exists because several helpers below walk the graph from outside a
// *testing.T — the layer matrix, the module-name list — and threading t through
// all of them would mean either a panic-capable helper or a duplicate parser.
// The parse is identical to readGraph; only the failure path differs.
func graphEdges() graph {
	g := make(graph)
	data, err := os.ReadFile(graphFile)
	if err != nil {
		// Falling back to an empty graph would make every rule below pass on
		// nothing, which is the one outcome these tests exist to prevent.
		panic("reading " + graphFile + ": " + err.Error() +
			" (regenerate with: make -C gogo arch-graph)")
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		importPath, deps, hasDeps := strings.Cut(line, "\t")
		if !hasDeps {
			importPath, deps = line, ""
		}
		var list []string
		if deps != "" {
			list = strings.Split(deps, ",")
		}
		g[importPath] = list
	}
	return g
}

// skipIfDisabled honours GOGO_SKIP_ARCHITECTURE, which is how a developer turns
// these rules off locally. It stays a skip rather than a pass so a skipped run
// can never be mistaken for a clean one in a log someone reads later.
func skipIfDisabled(t *testing.T) {
	t.Helper()
	if os.Getenv("GOGO_SKIP_ARCHITECTURE") != "" {
		t.Skip("GOGO_SKIP_ARCHITECTURE is set")
	}
}

// # Render strategies must be complete
//
// render.Respond chooses between two representations of one URL: a document for
// a navigation and a fragment for an in-page swap. The strategy it chooses from
// carries both, and it fails closed on an unknown Kind — but it cannot fail on a
// *missing field*. A KindNavigation with no Fragment renders a full document for
// an htmx request, because the only alternative available is nil.
//
// That failure is silent: the response is 200, the body is a complete document,
// and the symptom appears as a page whose swapped region replaced itself with a
// whole page. So completeness is checked here rather than trusted to review.
//
// The gateway boundary test covers the other half — that the route which
// negotiates actually declares Vary — because which URLs negotiate is a property
// of the running router, not something a static pass can know.
func TestRenderStrategiesAreComplete(t *testing.T) {
	skipIfDisabled(t)

	// A strategy is complete when every field its Kind needs is present. Kind
	// Navigation needs both representations; Kind Mutation needs the fragment and
	// a Post/Redirect/Get destination for the non-htmx case.
	required := map[string][]string{
		"render.KindNavigation": {"Page", "Fragment"},
		"render.KindMutation":   {"Fragment", "Redirect"},
	}

	root := findGogoRoot(t)
	fset := token.NewFileSet()
	strategies := 0

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "node_modules" || rel == ".devenv" {
			return filepath.SkipDir
		}

		pkgs, perr := parser.ParseDir(fset, path, nil, 0)
		if perr != nil {
			return nil // not a Go package directory
		}
		for _, pkg := range pkgs {
			for _, file := range pkg.Files {
				for _, literal := range strategyLiterals(file) {
					strategies++
					assertStrategyComplete(t, rel, literal, required)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	if strategies == 0 {
		t.Fatal("found no render.Strategy literals; the completeness rule would assert nothing")
	}
	t.Logf("checked %d render strategies", strategies)
}

// renderStrategy is one `render.Strategy{...}` composite literal, reduced to
// what completeness needs: which fields are set, and which Kind it names.
type renderStrategy struct {
	fields    map[string]bool
	kind      string
	kindIsSet bool
}

func strategyLiterals(file *ast.File) []renderStrategy {
	var out []renderStrategy

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != "render" || sel.Sel.Name != "Strategy" {
			return true
		}

		s := renderStrategy{fields: map[string]bool{}}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			s.fields[key.Name] = true
			if key.Name == "Kind" {
				s.kindIsSet = true
				if vs, ok := kv.Value.(*ast.SelectorExpr); ok {
					if vident, ok := vs.X.(*ast.Ident); ok {
						s.kind = vident.Name + "." + vs.Sel.Name
					}
				}
			}
		}
		out = append(out, s)
		return true
	})
	return out
}

func assertStrategyComplete(t *testing.T, pkgDir string, s renderStrategy, required map[string][]string) {
	t.Helper()

	if !s.kindIsSet {
		t.Errorf("%s builds a render.Strategy with no Kind; render.Respond fails closed on an "+
			"unknown kind, so this would be a runtime error on the first request", pkgDir)
		return
	}

	need, known := required[s.kind]
	if !known {
		t.Errorf("%s builds a render.Strategy with unknown Kind %q; this rule knows %v, so "+
			"either the strategy is wrong or this check is out of date", pkgDir, s.kind, required)
		return
	}

	var missing []string
	for _, field := range need {
		if !s.fields[field] {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s builds an incomplete render.Strategy for %s: missing %v.\n"+
			"  render.Respond cannot fail on a missing field — it picks whichever representation "+
			"is set. An htmx request to a KindNavigation with no Fragment would receive a whole "+
			"document with status 200, and the symptom would be a page whose swapped region "+
			"replaced itself with a full page.", pkgDir, s.kind, missing)
	}
}

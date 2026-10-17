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

// # Per-module dependency direction
//
// ARCHITECTURE.md states the hexagon rules in prose and says outright that no
// test enforces them:
//
//	"The classic hexagon rules short of the above still apply per module:
//	 presentation and infrastructure both depend on application ports, never
//	 the reverse; domain depends on nothing internal."
//
// So the architecture was right and unverified. These tests make it checked.
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

// The layers a module may be built from. A module that does not use one of them
// is simply not constrained by that row, so a flat module — billing, ops, and
// every module the scaffolder generates — passes without special-casing.
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

// TestHexagonDependencyDirection enforces ARCHITECTURE.md's per-module rules.
//
// It only inspects modules that actually build the hexagon. A flat module has
// no layer directories, so there is nothing to constrain and the rule is not
// applied — which is correct, not a gap: the rule is about how layers may
// relate to each other, and a module with one layer cannot violate it.
func TestHexagonDependencyDirection(t *testing.T) {
	skipIfDisabled(t)

	checked := 0
	for _, module := range modulesInGraph() {
		for _, layer := range knownLayers {
			pkg := modulesPrefix + module + "/" + layer
			deps, ok := graphEdges()[pkg]
			if !ok {
				continue
			}
			rules := rulesFor(module, layer)
			if len(rules) == 0 {
				continue
			}
			checked++

			allowed := map[string]bool{}
			for _, r := range rules {
				for _, to := range r.to {
					allowed[to] = true
				}
			}

			for _, dep := range deps {
				if !strings.HasPrefix(dep, modulesPrefix+module+"/") {
					continue // crosses a module boundary; rule 3 decides that
				}
				// Layers nest — infrastructure/postgres is inside
				// infrastructure — so a rule naming infrastructure covers
				// everything under it.
				depLayer := strings.TrimPrefix(dep, modulesPrefix+module+"/")
				permitted := false
				for layer := range allowed {
					if depLayer == layer || strings.HasPrefix(depLayer, layer+"/") {
						permitted = true
						break
					}
				}
				if permitted {
					continue
				}
				t.Errorf("%s imports %s\n"+
					"  violated: layer %q may only import %s inside its module (ARCHITECTURE.md: "+
					"\"presentation and infrastructure both depend on application ports, never the reverse; "+
					"domain depends on nothing internal\")",
					pkg, dep, layer, allowedList(layer))
			}
		}
	}

	if checked == 0 {
		t.Fatal("no hexagon layers found; the matrix would assert nothing")
	}
	t.Logf("checked %d layer packages across %d modules", checked, len(modulesInGraph()))
}

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

// TestCompositionLayerReachesNoModuleInternals is the machine form of the
// four-zone model in ARCHITECTURE.md §1: the application composition layer owns
// the object graph and the response shape, and owns no business vocabulary.
//
// The rule: bootstrap may reach a module only through the module's own
// composition root — its `compose` package, or its root package when it
// contributes itself to the container. It may not reach into a module's
// domain, application, presentation or infrastructure.
//
// Reaching into a module is not a Dependency Rule inversion — bootstrap is the
// outermost layer, so pointing outward from it is fine. What it costs is
// coupling: the composition root ends up knowing a module's internal wiring,
// which is precisely the knowledge the module's `compose` package exists to
// keep to itself. A module that declares what it needs as a parameter, and gets
// the implementation from its own compose, needs no help from bootstrap.
//
// Pages are checked by the same shape: a page reaches no module internals, which
// is import rule 1 restated at layer granularity rather than package
// granularity. The one module package a page may reach is `public` — the
// cross-team surface granted by docs/ADR_CROSS_TEAM_UI_CONTRACT.md §4.1 — and it
// is therefore the one layer this test does not forbid.
func TestCompositionLayerReachesNoModuleInternals(t *testing.T) {
	skipIfDisabled(t)

	g := graphEdges()

	// compositionExceptions are the edges the rule is known to be wrong about,
	// recorded rather than forgotten.
	//
	// The list is a shrink-only ledger: an entry may be removed but never
	// added silently, and each one says why it exists and what would retire it.
	// A rule with a quiet exception is worse than no rule, because the next
	// person cannot tell which edges were checked.
	//
	// It is empty. The last entry — bootstrap importing the sales module's
	// inventoryclient adapter — was retired by moving inventoryclient.New into
	// sales/compose as compose.NewInventoryAdjuster, so bootstrap now reaches
	// sales only through its composition root, exactly as it reaches auth and
	// inventory. The ledger stays so the next exception is written down where
	// its rule is enforced, not so it can be filled again.
	compositionExceptions := map[string]string{}

	// These are the layers that carry a module's business: rules in domain, use
	// cases in application, rules again in the adapters that implement them.
	internalModuleLayers := []string{"domain", "application", "presentation", "infrastructure"}

	// A page must not reach any of those layers, and not the module's compose
	// root either. A page reaching compose would drag the module's whole object
	// graph — adapters, connections, wiring — into the page's build, which is
	// the dependency the four-zone model exists to cut. Only `public` is open to
	// pages, per docs/ADR_CROSS_TEAM_UI_CONTRACT.md §4.1.
	pageForbiddenModuleLayers := append([]string{"compose"}, internalModuleLayers...)

	t.Run("bootstrap reaches modules only through their composition roots", func(t *testing.T) {
		for _, dep := range g["gogo/internal/bootstrap"] {
			rest, ok := strings.CutPrefix(dep, modulesPrefix)
			if !ok {
				continue
			}
			_, layer, _ := strings.Cut(rest, "/")
			if layer == "" {
				continue // the module's root package
			}
			if why, ok := compositionExceptions[dep]; ok {
				t.Logf("known composition exception: %s\n  %s", dep, why)
				continue
			}
			for _, forbidden := range internalModuleLayers {
				if layer == forbidden || strings.HasPrefix(layer, forbidden+"/") {
					t.Errorf("gogo/internal/bootstrap imports %s\n"+
						"  violated: the composition root may reach a module only through its own "+
						"compose package or its root package. Reaching into %q makes the root know "+
						"the module's internal wiring, which is what that module's compose package "+
						"exists to keep to itself", dep, forbidden)
				}
			}
		}
	})

	t.Run("a page reaches no module internals", func(t *testing.T) {
		for pagePkg := range g {
			if !strings.HasPrefix(pagePkg, pagesPrefix) {
				continue
			}
			for _, dep := range closure(g, pagePkg) {
				rest, ok := strings.CutPrefix(dep, modulesPrefix)
				if !ok {
					continue
				}
				_, layer, _ := strings.Cut(rest, "/")
				for _, forbidden := range pageForbiddenModuleLayers {
					if layer == forbidden || strings.HasPrefix(layer, forbidden+"/") {
						t.Errorf("page %s reaches module internals %s; pages consume modules over "+
							"their remote contracts, never by importing them. A module's only "+
							"page-importable package is its `public` surface "+
							"(ARCHITECTURE.md rule 1; docs/ADR_CROSS_TEAM_UI_CONTRACT.md §4.1)",
							pagePkg, dep)
					}
				}
			}
		}
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
func TestNoPlatformAggregateIsOwnedInProcess(t *testing.T) {
	skipIfDisabled(t)

	// Every aggregate the external platform owns. gogo may hold a client for
	// any of them and must hold state for none of them.
	forbidden := []string{
		modulesPrefix + "catalog/",
		modulesPrefix + "product",
		modulesPrefix + "listing",
		modulesPrefix + "media",
		modulesPrefix + "category",
		modulesPrefix + "search",
	}

	root := findGogoRoot(t)
	for _, prefix := range forbidden {
		base := filepath.Join(root, strings.TrimPrefix(prefix, "gogo/"))
		if _, err := os.Stat(base); err == nil {
			t.Errorf("%s exists: the platform owns this aggregate, and gogo holding a second "+
				"authority for one row is what D-011 and D-018 were written to prevent", prefix)
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

// modulesInGraph lists the distinct module names present in the committed graph.
func modulesInGraph() []string {
	g := graphEdges()
	seen := make(map[string]bool)
	for _, pkg := range packagesUnder(g, modulesPrefix) {
		rest := strings.TrimPrefix(pkg, modulesPrefix)
		name, _, _ := strings.Cut(rest, "/")
		if name != "" {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
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

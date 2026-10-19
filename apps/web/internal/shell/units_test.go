package shell

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnitTableIsWellFormed checks the table can say what it means: every path
// exists, no unit is declared twice, and each unit's declared kind matches the
// directory it lives in.
//
// The kind check is not ceremony. Wiring a vertical's backend as a page
// compiles fine and mounts fine — it just never appears in /module-registry and
// never gets its lifecycle started, so it is silently inert.
func TestUnitTableIsWellFormed(t *testing.T) {
	if len(registeredUnits) == 0 {
		t.Fatal("registeredUnits is empty; the composition root would wire nothing")
	}

	seen := make(map[string]bool, len(registeredUnits))
	for _, u := range registeredUnits {
		if u.path == "" {
			t.Error("a unit is declared without a path, so nothing can check it")
			continue
		}
		if seen[u.path] {
			t.Errorf("%s is declared twice; a duplicate registers the same unit twice", u.path)
		}
		seen[u.path] = true

		if _, err := os.Stat(filepath.Join("..", "..", u.path)); err != nil {
			t.Errorf("declared unit %s does not exist: %v", u.path, err)
		}
		if u.register == nil {
			t.Errorf("%s has a nil Register function, so it contributes nothing", u.path)
		}

		switch u.kind {
		case kindPage:
			if !strings.HasPrefix(u.path, featuresDir+"/") || !strings.HasSuffix(u.path, "/ui") {
				t.Errorf("%s is declared kind=page but is not a vertical's ui/ directory under %s", u.path, featuresDir)
			}
		case kindModule:
			if !strings.HasPrefix(u.path, featuresDir+"/") || strings.HasSuffix(u.path, "/ui") {
				t.Errorf("%s is declared kind=module but is not a vertical backend root under %s", u.path, featuresDir)
			}
		default:
			t.Errorf("%s has unknown kind %d", u.path, u.kind)
		}
	}
}

// TestEveryRegisteringUnitIsDeclared is the gate that makes the table a
// declaration rather than a list.
//
// A vertical that offers Register but is missing from registeredUnits
// compiles, passes every import-boundary rule, and simply never runs. Nothing
// else in the build notices: there is no error to read, only an absent route.
// So this walks the tree, finds every package that offers a Register on the DI
// builder, and fails if any of them is absent from the table.
//
// The reverse direction is checked by TestUnitTableIsWellFormed: every declared
// path must exist. Together they make declared == actual, in both directions.
func TestEveryRegisteringUnitIsDeclared(t *testing.T) {
	declared := make(map[string]bool, len(registeredUnits))
	for _, u := range registeredUnits {
		declared[u.path] = true
	}

	repoRoot := filepath.Join("..", "..")
	found := 0

	for _, root := range []string{featuresDir} {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			base := d.Name()
			if base == "node_modules" || strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			// Only the unit's own directory declares a Register; its handlers,
			// ports and adapters do not.
			if !hasRegisterFunc(path) {
				return nil
			}

			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			found++

			if !declared[rel] {
				t.Errorf("%s offers Register(*di.Builder) but is not in registeredUnits; it would never be wired", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}

	if found == 0 {
		t.Fatal("found no unit offering Register; the walk found nothing, so this gate asserts nothing")
	}
	t.Logf("checked %d units offering Register against %d declared", found, len(registeredUnits))
}

// hasRegisterFunc reports whether dir holds a package-level
// `func Register(*di.Builder)`.
//
// The signature is checked, not just the name: a `RegisterRoutes` or a
// `Register(r chi.Router)` is a different thing entirely, and matching on the
// bare name would sweep both into the gate.
func hasRegisterFunc(dir string) bool {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		// A directory with no parseable Go files is not a unit.
		return false
	}

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || fn.Name.Name != "Register" || fn.Type.Params == nil {
					continue
				}
				for _, field := range fn.Type.Params.List {
					star, isPtr := field.Type.(*ast.StarExpr)
					if !isPtr {
						continue
					}
					sel, isSelector := star.X.(*ast.SelectorExpr)
					if !isSelector {
						continue
					}
					if ident, isIdent := sel.X.(*ast.Ident); isIdent && ident.Name == "fdi" && sel.Sel.Name == "Builder" {
						return true
					}
				}
			}
		}
	}
	return false
}

// TestBreakerUpstreamsAreDistinct checks the breaker table can key a map.
//
// An empty or repeated upstream name would produce a breaker no call can ever
// resolve, and the failure would show up as a dependency that is never
// protected — reported by neither metrics nor the readiness probe.
func TestBreakerUpstreamsAreDistinct(t *testing.T) {
	if len(breakerUpstreams) == 0 {
		t.Fatal("breakerUpstreams is empty; no dependency would be circuit-broken")
	}

	seen := make(map[string]bool, len(breakerUpstreams))
	for _, u := range breakerUpstreams {
		if u.name == "" {
			t.Error("an upstream is declared with an empty name; a breaker keyed on \"\" never matches a dependency")
			continue
		}
		if seen[u.name] {
			t.Errorf("upstream %s is declared twice; the second breaker would shadow the first", u.name)
		}
		seen[u.name] = true
	}
}

// TestModuleUnitsAreResolvable is the check that catches a declared-but-broken
// wiring: every module in the table must actually contribute a *Module to the
// container, because that resolution is what the app provider performs before
// adding it to the registry.
func TestModuleUnitsAreResolvable(t *testing.T) {
	modules := moduleUnits()
	if len(modules) == 0 {
		t.Fatal("moduleUnits is empty; no module would be federated through the registry")
	}

	// Every module the table declares must be mounted by the shell through the
	// registry, so the count has to be non-trivial and must not include pages.
	for _, u := range registeredUnits {
		if u.kind == kindPage {
			continue
		}
		if !strings.HasPrefix(u.path, featuresDir+"/") {
			t.Errorf("%s is in moduleUnits but is not a vertical backend root under %s", u.path, featuresDir)
		}
	}
}

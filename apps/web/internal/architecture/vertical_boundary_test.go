package architecture_test

import (
	"strings"
	"testing"
)

// featuresPrefix is the team-owned-vertical home (D-023): a team owns one
// directory that holds its backend and its UI together, in one image.
const featuresPrefix = "gogo/internal/features/"

// TestFeatureVerticalIsAClosedModule is the single boundary guard for the
// team-owned-vertical model. A package inside internal/features/<f> may import
// only its own vertical (its own subtree) or the platform layer
// (internal/shared) — never another vertical's internals, and never a retired
// root. Cross-team contact, when a real one exists, is the declared
// <feature>/public surface (D-021); until then this test deliberately allows no
// cross-vertical edge at all.
//
// The re-home is complete (D-023 Phases 4–7), so the guard covers the whole
// tree and an empty tree is a failure rather than a skip: the moment it could
// assert nothing, it says so.
func TestFeatureVerticalIsAClosedModule(t *testing.T) {
	skipIfDisabled(t)

	g := readGraph(t)
	features := packagesUnder(g, featuresPrefix)
	if len(features) == 0 {
		t.Fatal("no internal/features package found; the closed-module guard would assert nothing (see D-023)")
	}

	for _, pkg := range features {
		rest, _ := strings.CutPrefix(pkg, featuresPrefix)
		feature, _, _ := strings.Cut(rest, "/")
		owner := featuresPrefix + feature

		for _, dep := range g[pkg] {
			if !strings.HasPrefix(dep, mainRepoPrefix) {
				continue
			}
			if strings.HasPrefix(dep, owner) {
				continue // own vertical
			}
			if strings.HasPrefix(dep, "gogo/internal/shared/") {
				continue // the platform layer
			}
			if strings.HasPrefix(dep, "gogo/contracts/") {
				continue // the neutral wire contracts (D-018)
			}
			t.Errorf(
				"%s imports %s: a team-owned vertical is a closed module — allowed only its own subtree, internal/shared, or gogo/contracts (D-023)",
				pkg, dep,
			)
		}
	}
}

// featuresInGraph lists the distinct team-owned verticals present in the
// committed graph: the first path segment under internal/features/.
func featuresInGraph(g graph) []string {
	seen := make(map[string]bool)
	for _, pkg := range packagesUnder(g, featuresPrefix) {
		rest, _ := strings.CutPrefix(pkg, featuresPrefix)
		name, _, _ := strings.Cut(rest, "/")
		if name != "" {
			seen[name] = true
		}
	}
	return sortedKeys(seen)
}

// TestFeatureVerticalHexagonDependencyDirection is the one runner of the layer
// matrix in layers_test.go. The matrix applies per vertical to whatever layers
// that vertical builds; the module-era runner over internal/modules was retired
// with the root (D-023 Phase 7), so a vertical cannot escape the matrix by
// moving out of a directory the old runner watched. ops, billing and
// contentcms are flat (no layers) and are untouched by every rule; sales, auth
// and inventory carry the full hexagon and must satisfy
// domain → application/ports → application/infrastructure/presentation, with
// compose the only layer able to reach all of them.
func TestFeatureVerticalHexagonDependencyDirection(t *testing.T) {
	skipIfDisabled(t)

	g := graphEdges()
	checked := 0

	for _, feature := range featuresInGraph(g) {
		base := featuresPrefix + feature + "/"
		for _, layer := range knownLayers {
			pkg := featuresPrefix + feature + "/" + layer
			deps, ok := g[pkg]
			if !ok {
				continue
			}
			rules := rulesFor(feature, layer)
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
				if !strings.HasPrefix(dep, base) {
					continue // crosses the vertical boundary; the closed-module test decides that
				}
				depLayer := strings.TrimPrefix(dep, base)
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
				t.Errorf(
					"%s imports %s: violated layer %q may only import %s inside its vertical (ARCHITECTURE.md: \"presentation and infrastructure both depend on application ports, never the reverse; domain depends on nothing internal\")",
					pkg, dep, layer, allowedList(layer),
				)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no feature hexagon layers found; the layer matrix would assert nothing")
	}
	t.Logf("checked %d layer packages across %d feature verticals", checked, len(featuresInGraph(g)))
}

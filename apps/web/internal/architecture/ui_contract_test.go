package architecture_test

import (
	"reflect"
	"testing"
)

// # The cross-team UI contract, asserted on synthetic graphs
//
// docs/ADR_CROSS_TEAM_UI_CONTRACT.md §4.1 grants pages exactly one kind of
// module import — `<module>/public` — and forbids everything else inside a
// module. The real import graph contains no `public` package yet, by design:
// D-007 says a contract is extracted with its first real consumer, not
// speculatively. So the rules that grant and forbid the edge are exercised here
// instead, on small hand-built graphs.
//
// The reason this file exists at all: every predicate in architecture_test.go is
// phrased as "this edge must not exist", and a negative rule that has never seen
// a positive case cannot be distinguished from a broken parser. The real-graph
// subtests prove the rules fire on nothing; these fixtures prove they would fire
// on the right thing — including the edges the ADR deliberately permits.

// pageToPublic is the graph every case below is a variation of: one page, one
// module, and whatever edge is under test. Leaf packages are written with no
// imports, which is how the snapshot format represents them.
func pageToPublic(pageDeps ...string) graph {
	g := graph{
		"gogo/internal/pages/home":           pageDeps,
		"gogo/internal/modules/sales/public": nil,
		"gogo/internal/modules/sales/domain": nil,
		"gogo/internal/modules/sales/compose": {
			"gogo/internal/modules/sales/domain",
		},
		"gogo/internal/modules/sales": {
			"gogo/internal/modules/sales/compose",
		},
	}
	return g
}

func TestModulePublicSurfaceRuleFixtures(t *testing.T) {
	page := "gogo/internal/pages/home"

	for _, tc := range []struct {
		name string
		dep  string
		want []string
	}{
		{
			name: "a page may import a module's public package",
			dep:  "gogo/internal/modules/sales/public",
			want: nil,
		},
		{
			name: "a page may import a package under a module's public package",
			dep:  "gogo/internal/modules/sales/public/views",
			want: nil,
		},
		{
			name: "a page may not import a module's domain",
			dep:  "gogo/internal/modules/sales/domain",
			want: []string{"gogo/internal/modules/sales/domain"},
		},
		{
			name: "a page may not import a module's compose root",
			dep:  "gogo/internal/modules/sales/compose",
			want: []string{"gogo/internal/modules/sales/compose", "gogo/internal/modules/sales/domain"},
		},
		{
			name: "a page may not import a flat module's root package",
			dep:  "gogo/internal/modules/sales",
			want: []string{"gogo/internal/modules/sales", "gogo/internal/modules/sales/compose", "gogo/internal/modules/sales/domain"},
		},
		{
			// The trap a naive strings.HasPrefix(dep, ".../public") check falls
			// into: a sibling package whose name merely starts with "public" is
			// not the contract.
			name: "a page may not import a package that merely shares the public prefix",
			dep:  "gogo/internal/modules/sales/publicity",
			want: []string{"gogo/internal/modules/sales/publicity"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := pageToPublic(tc.dep)
			if got := moduleReachOffenses(g, page); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("moduleReachOffenses = %v, want %v", got, tc.want)
			}
		})
	}

	// The rule is transitive on purpose: it checks the page's *closure*. A
	// `public` package that itself imports its module's domain makes the domain
	// part of every page that renders the contract, so the contract leaks what
	// it was supposed to hide. §4.1 says the page's closure "may not contain"
	// those paths, and a closure is transitive.
	t.Run("a public package that reaches its own module's internals taints the page", func(t *testing.T) {
		g := pageToPublic("gogo/internal/modules/sales/public")
		g["gogo/internal/modules/sales/public"] = []string{"gogo/internal/modules/sales/domain"}

		want := []string{"gogo/internal/modules/sales/domain"}
		if got := moduleReachOffenses(g, page); !reflect.DeepEqual(got, want) {
			t.Errorf("moduleReachOffenses = %v, want %v", got, want)
		}
	})

	// The clean graph must stay clean: a fixture suite that expected offenses
	// everywhere would pass on a predicate that flags everything.
	t.Run("the public edge alone is clean", func(t *testing.T) {
		g := pageToPublic("gogo/internal/modules/sales/public")
		if got := moduleReachOffenses(g, page); got != nil {
			t.Errorf("moduleReachOffenses = %v, want none", got)
		}
	})
}

func TestModuleToModulePublicIsNotGranted(t *testing.T) {
	// §8 item 3: the public surface is granted to pages only. Module-to-module
	// communication stays event-based, so these fixtures pin rule 3's predicate
	// against the one edge the UI ADR could be misread to allow.
	sales := "gogo/internal/modules/sales"
	other := "gogo/internal/modules/inventory"

	for _, tc := range []struct {
		name string
		dep  string
		want []string
	}{
		{
			name: "a module may not import another module's public package",
			dep:  other + "/public",
			want: []string{sales + " -> " + other + "/public"},
		},
		{
			name: "a module may not import another module's domain",
			dep:  other + "/domain",
			want: []string{sales + " -> " + other + "/domain"},
		},
		{
			name: "a module may import its own public package",
			dep:  sales + "/public",
			want: nil,
		},
		{
			name: "a module may import the shared contract module",
			dep:  "gogo/contracts/inventory/v1",
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := graph{
				sales:                         {tc.dep},
				sales + "/public":             nil,
				other + "/public":             nil,
				other + "/domain":             nil,
				"gogo/contracts/inventory/v1": nil,
			}
			if got := crossModuleOffenses(g, []string{sales}); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("crossModuleOffenses = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPagesDoNotComposePagesFixtures(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    graph
		want []string
	}{
		{
			name: "a page importing its own subtree is clean",
			g: graph{
				"gogo/internal/pages/home":             {"gogo/internal/pages/home/handlers"},
				"gogo/internal/pages/home/handlers":    {"gogo/internal/pages/home/views/pages"},
				"gogo/internal/pages/home/views/pages": nil,
			},
			want: nil,
		},
		{
			name: "a page importing shared internals is clean",
			g: graph{
				"gogo/internal/pages/home": {"gogo/internal/shared/web/render"},
			},
			want: nil,
		},
		{
			name: "a page importing another page is an offense",
			g: graph{
				"gogo/internal/pages/home/handlers":       {"gogo/internal/pages/profile/views/pages"},
				"gogo/internal/pages/profile/views/pages": nil,
			},
			want: []string{"gogo/internal/pages/home/handlers -> gogo/internal/pages/profile/views/pages"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roots := packagesUnder(tc.g, pagesPrefix)
			if got := pageCompositionOffenses(tc.g, roots); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pageCompositionOffenses = %v, want %v", got, tc.want)
			}
		})
	}
}

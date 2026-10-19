// Package islandkeys_test pins the home page's island key contract: the key
// the navigation handler renders under, the bus subscriber invalidates, and
// the renderer composes the page from must all be the same owned name.
package islandkeys_test

import (
	"strings"
	"testing"

	"gogo/internal/features/home/ui/islandkeys"
	"gogo/internal/shared/web/render"
)

// TestHomeGridIsAnOwnedIslandKey applies the ownership rule
// (docs/DECISION_UI_OWNERSHIP.md §5) to the real production key: the grid
// island must name its owner, and the owner must be the page that declares
// it — the home page. An unowned key here would be rejected by
// render.Island at composition time; this test names the failure at the
// declaration site instead.
func TestHomeGridIsAnOwnedIslandKey(t *testing.T) {
	t.Parallel()

	if err := render.ValidateIslandName(islandkeys.HomeGrid); err != nil {
		t.Fatalf("HomeGrid = %q is not a valid owned island key: %v", islandkeys.HomeGrid, err)
	}
	if !strings.HasPrefix(islandkeys.HomeGrid, "home.") {
		t.Errorf("HomeGrid = %q must be namespaced to the owning page (prefix %q)", islandkeys.HomeGrid, "home.")
	}
}

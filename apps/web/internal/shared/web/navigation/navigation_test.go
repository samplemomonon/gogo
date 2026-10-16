package navigation

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNavContextRoundTrip(t *testing.T) {
	t.Parallel()

	nav := []Item{{Label: "Home", Path: "/"}, {Label: "Billing", Path: "/billing"}}
	ctx := WithNav(context.Background(), nav)

	got := NavFromContext(ctx)
	if len(got) != len(nav) {
		t.Fatalf("NavFromContext returned %d items, want %d", len(got), len(nav))
	}
	for i := range nav {
		if got[i] != nav[i] {
			t.Errorf("item %d = %+v, want %+v", i, got[i], nav[i])
		}
	}
}

func TestNavFromContextWithoutValue(t *testing.T) {
	t.Parallel()

	if got := NavFromContext(context.Background()); got != nil {
		t.Errorf("NavFromContext on empty context = %v, want nil", got)
	}
}

func TestPathContextRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := WithPath(context.Background(), "/catalog/42")
	if got := PathFromContext(ctx); got != "/catalog/42" {
		t.Errorf("PathFromContext = %q, want /catalog/42", got)
	}
}

func TestPathFromContextWithoutValue(t *testing.T) {
	t.Parallel()

	if got := PathFromContext(context.Background()); got != "" {
		t.Errorf("PathFromContext on empty context = %q, want \"\"", got)
	}
}

func TestItemJSONShapeIsStable(t *testing.T) {
	t.Parallel()

	// The navigation model is part of the federated module contract
	// (module.Descriptor.Nav), so its JSON shape must stay stable.
	want := `{"label":"Store","path":"/catalog"}`

	raw, err := json.Marshal(Item{Label: "Store", Path: "/catalog"})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if got := string(raw); got != want {
		t.Errorf("Item JSON = %s, want %s", got, want)
	}
}

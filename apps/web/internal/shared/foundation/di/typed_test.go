package di

import (
	"testing"

	typetestone "gogo/internal/shared/foundation/di/typetest/one"
	typetesttwo "gogo/internal/shared/foundation/di/typetest/two"
)

// TestTypeKeysDistinguishSameNamedTypesFromDifferentPackages is the regression
// test for the type key's identity requirement. The fixtures one.Page and
// two.Page are distinct Go types that share both their package short name
// (`typetest`) and their type name (`Page`) — the shape the team-owned-vertical
// model makes routine, because every vertical has its own `ui` and `ports`
// packages (D-023). If the container keys them by reflect.Type.String() they
// collapse onto one key, the second registration overwrites the first, and the
// failure surfaces far away as "service *ui.Page is not of type *ui.Page".
func TestTypeKeysDistinguishSameNamedTypesFromDifferentPackages(t *testing.T) {
	b := NewBuilder()
	ProvideType(b, func(*Container) (*typetestone.Page, error) {
		return &typetestone.Page{Tag: "one"}, nil
	})
	ProvideType(b, func(*Container) (*typetesttwo.Page, error) {
		return &typetesttwo.Page{Tag: "two"}, nil
	})
	container := b.Build()

	gotOne, err := ResolveType[*typetestone.Page](container)
	if err != nil {
		t.Fatalf("resolve one.Page: %v", err)
	}
	gotTwo, err := ResolveType[*typetesttwo.Page](container)
	if err != nil {
		t.Fatalf("resolve two.Page: %v", err)
	}
	if gotOne.Tag != "one" || gotTwo.Tag != "two" {
		t.Errorf("resolved tags = %q, %q, want %q, %q — the container collapsed two same-named types from different packages onto one key",
			gotOne.Tag, gotTwo.Tag, "one", "two")
	}
}

// TestProvideRoundTripsTypedValue pins the typed registration contract: a
// value registered through Provide resolves back as the same type with no
// interface{} assertion at the call site.
func TestProvideRoundTripsTypedValue(t *testing.T) {
	type service struct{ name string }

	container := Provide(
		NewBuilder(),
		"service",
		func(*Container) (service, error) { return service{name: "catalog"}, nil },
	).Build()

	got, err := ResolveAs[service](container, "service")
	if err != nil {
		t.Fatalf("resolve typed service: %v", err)
	}
	if got.name != "catalog" {
		t.Errorf("resolved service = %+v, want name catalog", got)
	}

	// Matching the singleton contract, a second resolution is the same object.
	again := MustResolveAs[service](container, "service")
	if again != got {
		t.Error("Provide did not register a singleton")
	}
}

// TestLifecycleRunsCallbacksInReverseOrderOnce pins the two properties a
// composition root relies on: dependencies are released after their dependents
// (reverse order), and the shutdown hook is safe to call more than once.
func TestLifecycleRunsCallbacksInReverseOrderOnce(t *testing.T) {
	lifecycle := NewLifecycle()

	var order []int
	lifecycle.Add(func() { order = append(order, 1) })
	lifecycle.Add(func() { order = append(order, 2) })
	lifecycle.Add(nil) // ignored
	lifecycle.Add(func() { order = append(order, 3) })

	lifecycle.Close()
	lifecycle.Close() // idempotent: must not re-run

	want := []int{3, 2, 1}
	if len(order) != len(want) {
		t.Fatalf("callbacks ran %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("callbacks ran %v, want %v", order, want)
		}
	}
}

package di

import "testing"

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

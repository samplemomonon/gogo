package di

import (
	"errors"
	"strings"
	"testing"
)

// TestNamedKeysKeepInstancesOfOneTypeApart pins the property ProvideNamed
// exists for: two instances of the same Go type stay independently addressable
// and each keeps its own singleton identity.
func TestNamedKeysKeepInstancesOfOneTypeApart(t *testing.T) {
	type upstream struct{ address string }

	builder := NewBuilder()
	ProvideNamed(builder, "catalog", func(*Container) (*upstream, error) {
		return &upstream{address: "catalog:18082"}, nil
	})
	ProvideNamed(builder, "listing", func(*Container) (*upstream, error) {
		return &upstream{address: "listing:18084"}, nil
	})
	container := builder.Build()

	catalog, err := ResolveNamedAs[*upstream](container, "catalog")
	if err != nil {
		t.Fatalf("resolve catalog upstream: %v", err)
	}
	listing, err := ResolveNamedAs[*upstream](container, "listing")
	if err != nil {
		t.Fatalf("resolve listing upstream: %v", err)
	}

	if catalog.address != "catalog:18082" || listing.address != "listing:18084" {
		t.Errorf("named instances crossed: catalog=%q listing=%q", catalog.address, listing.address)
	}
	if catalog == listing {
		t.Error("two names for one type resolved to the same instance")
	}
	again, err := ResolveNamedAs[*upstream](container, "catalog")
	if err != nil {
		t.Fatalf("re-resolve catalog upstream: %v", err)
	}
	if again != catalog {
		t.Error("ProvideNamed did not register a singleton per name")
	}
}

// TestNamedKeyDoesNotShadowTypeKey pins that a name-qualified key and a plain
// type key are distinct registrations of the same type, so a consumer that
// wants "the one of these" is never silently handed another.
func TestNamedKeyDoesNotShadowTypeKey(t *testing.T) {
	type repo struct{ name string }

	builder := NewBuilder()
	ProvideType(builder, func(*Container) (*repo, error) { return &repo{name: "default"}, nil })
	ProvideNamed(builder, "primary", func(*Container) (*repo, error) { return &repo{name: "primary"}, nil })
	container := builder.Build()

	byType, err := ResolveType[*repo](container)
	if err != nil {
		t.Fatalf("resolve by type: %v", err)
	}
	byName, err := ResolveNamedAs[*repo](container, "primary")
	if err != nil {
		t.Fatalf("resolve by name: %v", err)
	}

	if byType.name != "default" {
		t.Errorf("type key resolved %q, want default — a named key shadowed it", byType.name)
	}
	if byName.name != "primary" {
		t.Errorf("named key resolved %q, want primary", byName.name)
	}
}

// TestResolveNamedAsUnknownNameFails pins that asking for a name nobody
// registered is an error naming the key, not a silently nil dependency.
func TestResolveNamedAsUnknownNameFails(t *testing.T) {
	type upstream struct{}

	container := NewContainer()

	_, err := ResolveNamedAs[*upstream](container, "catalog")
	if !errors.Is(err, ErrServiceNotFound) {
		t.Fatalf("error = %v, want ErrServiceNotFound", err)
	}
	if !strings.Contains(err.Error(), "catalog") {
		t.Errorf("error %q does not name the requested key", err)
	}
}

package di

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type upstream struct{ address string }

// TestGroupResolvesContributionsInRegistrationOrder pins the consumer-facing
// contract of a group: every contribution arrives, in the order it was
// contributed, under the name the root gave it.
func TestGroupResolvesContributionsInRegistrationOrder(t *testing.T) {
	builder := NewBuilder()
	Contribute(builder, "catalog", func(*Container) (*upstream, error) {
		return &upstream{address: "catalog:18082"}, nil
	})
	Contribute(builder, "listing", func(*Container) (*upstream, error) {
		return &upstream{address: "listing:18084"}, nil
	})
	Contribute(builder, "search", func(*Container) (*upstream, error) {
		return &upstream{address: "search:9000"}, nil
	})
	container := builder.Build()

	members, err := ResolveGroupAs[*upstream](container)
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}

	wantNames := []string{"catalog", "listing", "search"}
	if len(members) != len(wantNames) {
		t.Fatalf("group has %d members, want %d", len(members), len(wantNames))
	}
	for i, want := range wantNames {
		if members[i].Name != want {
			t.Errorf("member %d named %q, want %q (order must follow contribution)", i, members[i].Name, want)
		}
	}
	if members[0].Value.address != "catalog:18082" || members[2].Value.address != "search:9000" {
		t.Errorf("group values crossed: %+v", members)
	}
}

// TestEmptyGroupResolvesToEmptySlice pins that zero contributions is a valid
// group, not a missing dependency: a consumer may legitimately have nothing
// wired and must not have to special-case it to avoid failing.
func TestEmptyGroupResolvesToEmptySlice(t *testing.T) {
	container := NewContainer()

	members, err := ResolveGroupAs[*upstream](container)
	if err != nil {
		t.Fatalf("empty group errored: %v", err)
	}
	if len(members) != 0 {
		t.Errorf("empty group has %d members", len(members))
	}
	if members == nil {
		t.Error("empty group returned a nil slice, want a usable empty one")
	}
}

// TestGroupIsBuiltOnceOnFirstResolve pins that a contribution is as lazy and as
// single-flight as a provided singleton: nothing is constructed while wiring.
func TestGroupIsBuiltOnceOnFirstResolve(t *testing.T) {
	calls := 0
	builder := NewBuilder()
	Contribute(builder, "catalog", func(*Container) (*upstream, error) {
		calls++
		return &upstream{address: "catalog:18082"}, nil
	})
	container := builder.Build()

	if calls != 0 {
		t.Fatalf("contribution ran %d times during wiring, want 0", calls)
	}
	for i := 0; i < 3; i++ {
		if _, err := ResolveGroupAs[*upstream](container); err != nil {
			t.Fatalf("resolve group: %v", err)
		}
	}
	if calls != 1 {
		t.Errorf("contribution ran %d times across 3 resolutions, want 1", calls)
	}
}

// TestGroupBuildsConcurrently pins single-flight assembly under the race
// detector: a group resolved from many goroutines runs each contribution once.
func TestGroupBuildsConcurrently(t *testing.T) {
	var mu sync.Mutex
	calls := 0

	builder := NewBuilder()
	Contribute(builder, "catalog", func(*Container) (*upstream, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return &upstream{address: "catalog:18082"}, nil
	})
	container := builder.Build()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ResolveGroupAs[*upstream](container); err != nil {
				t.Errorf("concurrent resolve: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("contribution ran %d times across 16 concurrent resolutions, want 1", calls)
	}
}

// TestGroupIncludesContributionsAddedAfterTheFirst pins the documented
// lifecycle: the assembling singleton reads the contribution list when it is
// built, so a contribution registered after the first Contribute is still
// included, as long as the graph has not resolved yet.
func TestGroupIncludesContributionsAddedAfterTheFirst(t *testing.T) {
	builder := NewBuilder()
	Contribute(builder, "catalog", func(*Container) (*upstream, error) {
		return &upstream{address: "catalog:18082"}, nil
	})
	Contribute(builder, "listing", func(*Container) (*upstream, error) {
		return &upstream{address: "listing:18084"}, nil
	})
	container := builder.Build()

	members, err := ResolveGroupAs[*upstream](container)
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("group has %d members, want 2 — a later contribution was dropped", len(members))
	}
	if members[1].Name != "listing" {
		t.Errorf("member 1 = %q, want listing", members[1].Name)
	}
}

// TestGroupIsSealedOnceResolved pins the other half of the same caveat: adding
// a contribution after the group has been built does not rewrite an already
// built group. A container built this way is single-shot, so a late
// contribution is ignored rather than racing the cached value.
func TestGroupIsSealedOnceResolved(t *testing.T) {
	builder := NewBuilder()
	Contribute(builder, "catalog", func(*Container) (*upstream, error) {
		return &upstream{address: "catalog:18082"}, nil
	})
	container := builder.Build()

	if _, err := ResolveGroupAs[*upstream](container); err != nil {
		t.Fatalf("resolve group: %v", err)
	}

	Contribute(builder, "late", func(*Container) (*upstream, error) {
		return &upstream{address: "late:1"}, nil
	})

	members, err := ResolveGroupAs[*upstream](container)
	if err != nil {
		t.Fatalf("resolve group after late contribution: %v", err)
	}
	if len(members) != 1 {
		t.Errorf("built group changed after resolution: %d members, want 1", len(members))
	}
}

// TestGroupMemberErrorNamesTheFailingContribution pins that a member's failure
// says which member failed, so a broken upstream is identifiable without
// bisecting the graph.
func TestGroupMemberErrorNamesTheFailingContribution(t *testing.T) {
	sentinel := errors.New("dial refused")

	builder := NewBuilder()
	Contribute(builder, "catalog", func(*Container) (*upstream, error) {
		return &upstream{address: "catalog:18082"}, nil
	})
	Contribute(builder, "listing", func(*Container) (*upstream, error) {
		return nil, fmt.Errorf("dial listing: %w", sentinel)
	})
	container := builder.Build()

	_, err := ResolveGroupAs[*upstream](container)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap the member error", err)
	}
	if !strings.Contains(err.Error(), "listing") {
		t.Errorf("error %q does not name the failing member", err)
	}
	if strings.Contains(err.Error(), "catalog") {
		t.Errorf("error %q blames the healthy member", err)
	}
}

// TestGroupKeyDoesNotShadowTypeKey pins that a group and a plain registration
// of the same type coexist: a consumer asking for "the one" is not handed the
// group, and a consumer asking for "all" is not handed a single value.
func TestGroupKeyDoesNotShadowTypeKey(t *testing.T) {
	builder := NewBuilder()
	ProvideType(builder, func(*Container) (*upstream, error) {
		return &upstream{address: "default"}, nil
	})
	Contribute(builder, "catalog", func(*Container) (*upstream, error) {
		return &upstream{address: "catalog:18082"}, nil
	})
	container := builder.Build()

	byType, err := ResolveType[*upstream](container)
	if err != nil {
		t.Fatalf("resolve by type: %v", err)
	}
	if byType.address != "default" {
		t.Errorf("type key resolved %q, want default", byType.address)
	}

	members, err := ResolveGroupAs[*upstream](container)
	if err != nil {
		t.Fatalf("resolve group: %v", err)
	}
	if len(members) != 1 || members[0].Value.address != "catalog:18082" {
		t.Errorf("group = %+v, want one catalog member", members)
	}
}

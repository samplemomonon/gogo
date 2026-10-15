package di

import (
	"errors"
	"fmt"
)

// Member is one contribution to a group: the value a contribution built,
// under the name the composition root gave it.
//
// The name is what lets a consumer report *which* dependency is unhealthy
// ("catalog circuit breaker is open") instead of only that one is, so a group
// consumer never has to hardcode the set of names it might be handed.
type Member[T any] struct {
	Name  string
	Value T
}

// groupEntry is one contribution as the builder holds it. Entries live in a
// map keyed by a type-erased string, so the factory is stored erased too; the
// group assembles members with the T known from the key it is building.
type groupEntry struct {
	name    string
	factory Factory
}

// groupPrefix namespaces group keys so a group can never collide with a plain
// type key (ProvideType) or a name-qualified one (ProvideNamed).
const groupPrefix = "group:"

func groupKey[T any]() string {
	return groupPrefix + typeKey[T]()
}

// Contribute adds factory's result to the group of T, under name. The group is
// resolved as []Member[T] in registration order.
//
// This is the primitive for "every implementation of X": a consumer depends on
// the whole group instead of naming each member, so adding an implementation
// never edits the consumer and a consumer never needs to know the set of
// implementations up front.
//
//	di.Contribute(b, "catalog", func(c *di.Container) (*breaker.Breaker, error) { ... })
//	di.Contribute(b, "listing", func(c *di.Container) (*breaker.Breaker, error) { ... })
//	upstreams, err := di.ResolveGroupAs[*breaker.Breaker](c)
//
// A contribution is as lazy as a provided singleton: the group is built on
// first resolution, and each member's factory runs at most once. Contribute
// must therefore be called while wiring, before the graph is resolved — a
// contribution added after the group has been built is not picked up.
func Contribute[T any](b *Builder, name string, factory func(*Container) (T, error)) *Builder {
	if b.groups == nil {
		b.groups = make(map[string][]groupEntry)
	}
	if b.installedGroups == nil {
		b.installedGroups = make(map[string]struct{})
	}

	key := groupKey[T]()
	b.groups[key] = append(b.groups[key], groupEntry{
		name: name,
		factory: func(c *Container) (interface{}, error) {
			return factory(c)
		},
	})

	if _, installed := b.installedGroups[key]; !installed {
		b.installedGroups[key] = struct{}{}
		// The assembling singleton is installed by the first contribution
		// because that is the only place T is still known. It reads the
		// contribution list when it is built, not now, so contributions
		// registered after this point are still included.
		b.container.RegisterSingleton(key, func(c *Container) (interface{}, error) {
			entries := b.groups[key]
			members := make([]Member[T], 0, len(entries))
			for _, entry := range entries {
				raw, err := entry.factory(c)
				if err != nil {
					return nil, fmt.Errorf("group %s member %q: %w", typeKey[T](), entry.name, err)
				}
				value, ok := raw.(T)
				if !ok {
					return nil, fmt.Errorf("group %s member %q: contributed %T, want %s",
						typeKey[T](), entry.name, raw, typeKey[T]())
				}
				members = append(members, Member[T]{Name: entry.name, Value: value})
			}
			return members, nil
		})
	}
	return b
}

// ResolveGroupAs returns every contribution to the group of T, in
// registration order.
//
// A group nobody contributed to resolves to an empty slice rather than an
// error, so a consumer may legitimately have zero of a dependency and does not
// have to special-case the zero case to avoid failing.
//
// There is no Must variant, unlike the ResolveAs family: a panicking member
// factory spends the group's sync.Once, so a later resolve reports a
// misleading "singleton built no value" rather than the original fault.
func ResolveGroupAs[T any](c *Container) ([]Member[T], error) {
	resolved, err := c.Resolve(groupKey[T]())
	if err != nil {
		if errors.Is(err, ErrServiceNotFound) {
			return []Member[T]{}, nil
		}
		return nil, err
	}
	members, ok := resolved.([]Member[T])
	if !ok {
		return nil, fmt.Errorf("service %s is not a group of %s", groupKey[T](), typeKey[T]())
	}
	return members, nil
}

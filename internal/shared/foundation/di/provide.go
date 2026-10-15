package di

import "reflect"

// Provide registers a typed singleton factory on the builder. It is the
// type-safe complement to Builder.AddSingleton: the factory returns the
// concrete (or interface) type T, so a registration site never writes the
// `(interface{}, error)` boxing itself and the resolved type cannot drift from
// what the factory produces.
//
//	di.Provide(b, "kernel.Logger", func(c *di.Container) (kernel.Logger, error) {
//	    return adapter.NewLoggerAdapter(logger), nil
//	})
func Provide[T any](b *Builder, name string, factory func(*Container) (T, error)) *Builder {
	b.container.RegisterSingleton(name, func(c *Container) (interface{}, error) {
		return factory(c)
	})
	return b
}

// ProvideInstance registers an already-built typed singleton. It mirrors
// Builder.AddInstance with the value's type made explicit, so a registration
// list reads uniformly whether a dependency is built (Provide) or pre-existing
// (ProvideInstance).
func ProvideInstance[T any](b *Builder, name string, value T) *Builder {
	b.container.Register(name, value)
	return b
}

// ProvideType registers a typed singleton keyed by its Go type instead of a
// string name. A unit can publish a collaborator — or itself — without any
// package inventing a shared name constant, so registration stays encapsulated
// in the package that owns the type.
//
//	di.ProvideType(b, func(c *di.Container) (*home.Page, error) { ... })
//	page := di.MustResolveType[*home.Page](c)
func ProvideType[T any](b *Builder, factory func(*Container) (T, error)) *Builder {
	return Provide(b, typeKey[T](), factory)
}

// ProvideInstanceType registers an existing value keyed by its Go type.
func ProvideInstanceType[T any](b *Builder, value T) *Builder {
	return ProvideInstance(b, typeKey[T](), value)
}

// ResolveType resolves a service registered by ProvideType.
func ResolveType[T any](c *Container) (T, error) {
	return ResolveAs[T](c, typeKey[T]())
}

// MustResolveType resolves a service registered by ProvideType, panicking on
// error.
func MustResolveType[T any](c *Container) T {
	return MustResolveAs[T](c, typeKey[T]())
}

func typeKey[T any]() string {
	var zero T
	return reflect.TypeOf(&zero).Elem().String()
}

package di

// ProvideNamed registers a typed singleton qualified by a name. It is
// ProvideType for the case a type alone cannot express: several instances of
// one Go type that must stay individually addressable.
//
// A name-qualified key stays type-checked, so a consumer cannot typo its way
// to a different dependency the way a bare string name can — the mistake
// surfaces as a compile error at ProvideNamed/ResolveNamedAs instead of a
// runtime "service not found" against an unrelated registration.
//
//	di.ProvideNamed(b, "catalog", func(c *di.Container) (*grpc.ClientConn, error) { ... })
//	di.ProvideNamed(b, "listing", func(c *di.Container) (*grpc.ClientConn, error) { ... })
//	conn, err := di.ResolveNamedAs[*grpc.ClientConn](c, "catalog")
func ProvideNamed[T any](b *Builder, name string, factory func(*Container) (T, error)) *Builder {
	return Provide(b, namedTypeKey[T](name), factory)
}

// ResolveNamedAs resolves the instance of T registered under name. It has no
// Must twin, unlike ResolveAs: a panicking provider spends the container's
// sync.Once, so a retry reports a misleading "singleton built no value" rather
// than the original fault.
func ResolveNamedAs[T any](c *Container, name string) (T, error) {
	return ResolveAs[T](c, namedTypeKey[T](name))
}

// namedTypeKey qualifies a type key with a name. A Go type string never
// contains "|", so a name-qualified key cannot collide with a plain typeKey
// (ProvideType) and two names for one type cannot collide with each other.
func namedTypeKey[T any](name string) string {
	return name + "|" + typeKey[T]()
}

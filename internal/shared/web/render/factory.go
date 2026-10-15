package render

// ComposerFactory creates Composer instances with consistent configuration.
// This centralizes composer creation so cache policies and island behavior
// stay uniform across modules and pages.
type ComposerFactory struct {
	cacheFactory func() Cache
}

// NewComposerFactory returns a factory that creates composers with the given
// cache strategy. Pass NewTTLCache for production (bounded, time-limited
// cache) or nil for testing (unbounded, never expires).
func NewComposerFactory(cacheFactory func() Cache) *ComposerFactory {
	return &ComposerFactory{
		cacheFactory: cacheFactory,
	}
}

// NewComposer creates a composer with the factory's configured cache strategy.
func (f *ComposerFactory) NewComposer() *Composer {
	var cache Cache
	if f.cacheFactory != nil {
		cache = f.cacheFactory()
	}
	return NewComposer(cache)
}

// DefaultFactory returns a factory configured with TTL cache — the standard
// production configuration.
func DefaultFactory() *ComposerFactory {
	return NewComposerFactory(func() Cache {
		return NewTTLCache()
	})
}

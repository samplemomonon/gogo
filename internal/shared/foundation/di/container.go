// Package di provides a lightweight dependency injection container for
// managing service dependencies and reducing coupling in the application.
package di

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

var (
	ErrServiceNotFound = errors.New("service not found")
	ErrCircularDep     = errors.New("circular dependency detected")
)

// Factory creates an instance of a service.
type Factory func(c *Container) (interface{}, error)

// buildResult caches a singleton's outcome — value or error — so the factory
// runs exactly once regardless of how many callers race for it.
type buildResult struct {
	value interface{}
	err   error
}

// registry is the state every Container shares. A resolution hands factories a
// child Container carrying the ancestor path (for cycle detection) but the same
// registry, so registrations and singletons are process-wide.
type registry struct {
	mu                 sync.RWMutex
	services           map[string]interface{}
	factories          map[string]Factory
	singletonFactories map[string]Factory
	onces              map[string]*sync.Once
	results            map[string]buildResult
}

func newRegistry() *registry {
	return &registry{
		services:           make(map[string]interface{}),
		factories:          make(map[string]Factory),
		singletonFactories: make(map[string]Factory),
		onces:              make(map[string]*sync.Once),
		results:            make(map[string]buildResult),
	}
}

// Container manages service registration and resolution. Registration methods
// are used on the root; Resolve hands each factory a child view that records
// the resolution path, so a cycle is reported without global mutable state.
type Container struct {
	reg  *registry
	path []string
}

// NewContainer creates a new dependency injection container.
func NewContainer() *Container {
	return &Container{reg: newRegistry()}
}

// Register adds an already-built singleton instance to the container.
func (c *Container) Register(name string, instance interface{}) {
	c.reg.mu.Lock()
	defer c.reg.mu.Unlock()
	c.reg.services[name] = instance
}

// RegisterFactory adds a factory evaluated on every resolution.
func (c *Container) RegisterFactory(name string, factory Factory) {
	c.reg.mu.Lock()
	defer c.reg.mu.Unlock()
	c.reg.factories[name] = factory
}

// RegisterSingleton adds a factory whose result is built once and cached.
func (c *Container) RegisterSingleton(name string, factory Factory) {
	c.reg.mu.Lock()
	defer c.reg.mu.Unlock()
	c.reg.singletonFactories[name] = factory
}

// Resolve retrieves a service by name.
func (c *Container) Resolve(name string) (interface{}, error) {
	return c.resolve(name)
}

func (c *Container) resolve(name string) (interface{}, error) {
	reg := c.reg

	reg.mu.RLock()
	instance, isInstance := reg.services[name]
	transient, isTransient := reg.factories[name]
	singleton, isSingleton := reg.singletonFactories[name]
	reg.mu.RUnlock()

	if isInstance {
		return instance, nil
	}
	if !isTransient && !isSingleton {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotFound, name)
	}
	if c.onPath(name) {
		return nil, fmt.Errorf("%w: %s", ErrCircularDep, name)
	}

	child := &Container{reg: reg, path: extendPath(c.path, name)}
	if isSingleton {
		return reg.singleton(name, singleton, child)
	}
	return transient(child)
}

func (c *Container) onPath(name string) bool {
	for _, p := range c.path {
		if p == name {
			return true
		}
	}
	return false
}

func extendPath(path []string, name string) []string {
	next := make([]string, len(path)+1)
	copy(next, path)
	next[len(path)] = name
	return next
}

// singleton runs the factory exactly once per name (sync.Once is single-flight)
// and returns the cached outcome to every caller.
func (r *registry) singleton(name string, factory Factory, child *Container) (interface{}, error) {
	r.mu.Lock()
	once, ok := r.onces[name]
	if !ok {
		once = &sync.Once{}
		r.onces[name] = once
	}
	r.mu.Unlock()

	once.Do(func() {
		value, err := factory(child)
		r.mu.Lock()
		r.results[name] = buildResult{value: value, err: err}
		r.mu.Unlock()
	})

	r.mu.RLock()
	result, ok := r.results[name]
	r.mu.RUnlock()
	if !ok {
		// The Once ran but stored nothing: the factory panicked, or Clear raced
		// the build. Surface it rather than a silent (nil, nil).
		return nil, fmt.Errorf("service %s: singleton built no value", name)
	}
	return result.value, result.err
}

// MustResolve retrieves a service by name, panicking on error.
func (c *Container) MustResolve(name string) interface{} {
	instance, err := c.Resolve(name)
	if err != nil {
		panic(err)
	}
	return instance
}

// ResolveAs retrieves a service and type-asserts it.
func ResolveAs[T any](c *Container, name string) (T, error) {
	var zero T
	instance, err := c.Resolve(name)
	if err != nil {
		return zero, err
	}

	typed, ok := instance.(T)
	if !ok {
		return zero, fmt.Errorf("service %s is not of type %s", name, reflect.TypeOf(zero))
	}
	return typed, nil
}

// MustResolveAs retrieves and type-asserts a service, panicking on error.
func MustResolveAs[T any](c *Container, name string) T {
	result, err := ResolveAs[T](c, name)
	if err != nil {
		panic(err)
	}
	return result
}

// Clear removes every registration and cached singleton. Equivalent to a fresh
// container, so a second Resolve after Clear re-runs the factories.
func (c *Container) Clear() {
	c.reg.mu.Lock()
	defer c.reg.mu.Unlock()
	c.reg.services = make(map[string]interface{})
	c.reg.factories = make(map[string]Factory)
	c.reg.singletonFactories = make(map[string]Factory)
	c.reg.onces = make(map[string]*sync.Once)
	c.reg.results = make(map[string]buildResult)
}

// Builder provides a fluent API for container configuration.
type Builder struct {
	container *Container
	// groups is keyed by a type-erased group key, so entries are held erased
	// too. The assembling singleton Contribute installs reads b.groups when it
	// is built, not when it is registered — so a group includes contributions
	// added later, and Contribute must not be called after the graph resolves.
	groups          map[string][]groupEntry
	installedGroups map[string]struct{}
}

// NewBuilder creates a new container builder.
func NewBuilder() *Builder {
	return &Builder{
		container: NewContainer(),
	}
}

// AddSingleton registers a singleton service.
func (b *Builder) AddSingleton(name string, factory Factory) *Builder {
	b.container.RegisterSingleton(name, factory)
	return b
}

// AddTransient registers a transient service.
func (b *Builder) AddTransient(name string, factory Factory) *Builder {
	b.container.RegisterFactory(name, factory)
	return b
}

// AddInstance registers an existing instance.
func (b *Builder) AddInstance(name string, instance interface{}) *Builder {
	b.container.Register(name, instance)
	return b
}

// Build returns the configured container.
func (b *Builder) Build() *Container {
	return b.container
}

// Injector performs automatic dependency injection.
type Injector struct {
	container *Container
}

// NewInjector creates a new injector.
func NewInjector(container *Container) *Injector {
	return &Injector{container: container}
}

// Inject resolves and injects dependencies into a struct's fields.
func (i *Injector) Inject(target interface{}) error {
	val := reflect.ValueOf(target)
	if val.Kind() != reflect.Ptr {
		return errors.New("target must be a pointer")
	}

	elem := val.Elem()
	if elem.Kind() != reflect.Struct {
		return errors.New("target must be a pointer to struct")
	}

	typ := elem.Type()
	for j := 0; j < elem.NumField(); j++ {
		field := elem.Field(j)
		fieldType := typ.Field(j)

		tag := fieldType.Tag.Get("inject")
		if tag == "" {
			continue
		}

		if !field.CanSet() {
			return fmt.Errorf("cannot set field %s", fieldType.Name)
		}

		service, err := i.container.Resolve(tag)
		if err != nil {
			return fmt.Errorf("failed to inject %s: %w", fieldType.Name, err)
		}

		serviceVal := reflect.ValueOf(service)
		if !serviceVal.Type().AssignableTo(field.Type()) {
			return fmt.Errorf("service %s is not assignable to field %s", tag, fieldType.Name)
		}

		field.Set(serviceVal)
	}

	return nil
}

// Scope represents a scoped container for request-level dependencies.
type Scope struct {
	parent   *Container
	services map[string]interface{}
	mu       sync.RWMutex
}

// NewScope creates a new scope from a parent container.
func (c *Container) NewScope() *Scope {
	return &Scope{
		parent:   c,
		services: make(map[string]interface{}),
	}
}

// Register adds a service to the scope.
func (s *Scope) Register(name string, instance interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.services[name] = instance
}

// Resolve retrieves a service from the scope or falls back to the parent.
func (s *Scope) Resolve(name string) (interface{}, error) {
	s.mu.RLock()
	if instance, exists := s.services[name]; exists {
		s.mu.RUnlock()
		return instance, nil
	}
	s.mu.RUnlock()

	return s.parent.Resolve(name)
}

// WithScope runs fn with a fresh child scope, unless ctx is already cancelled.
func (c *Container) WithScope(ctx context.Context, fn func(*Scope) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(c.NewScope())
}

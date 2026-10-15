package di_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"gogo/internal/shared/foundation/di"
)

// TestSingletonBuiltOnceConcurrently pins the single-flight contract: many
// goroutines racing for one singleton build it exactly once and all receive
// the same instance, with no error.
func TestSingletonBuiltOnceConcurrently(t *testing.T) {
	container := di.NewContainer()

	var builds atomic.Int32
	container.RegisterSingleton("singleton", func(*di.Container) (interface{}, error) {
		builds.Add(1)
		return &struct{}{}, nil
	})

	const goroutines = 64
	results := make([]interface{}, goroutines)
	errs := make([]error, goroutines)

	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = container.Resolve("singleton")
		}(i)
	}
	wg.Wait()

	if got := builds.Load(); got != 1 {
		t.Errorf("singleton built %d times, want 1", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: resolve error: %v", i, err)
		}
		if results[i] != results[0] {
			t.Fatalf("goroutine %d got a different instance", i)
		}
	}
}

// TestClearEmptiesContainer pins that Clear returns the container to a fresh
// state: registrations and cached singletons are gone, and a re-registered
// factory runs again.
func TestClearEmptiesContainer(t *testing.T) {
	container := di.NewContainer()

	var builds atomic.Int32
	register := func() {
		container.RegisterSingleton("singleton", func(*di.Container) (interface{}, error) {
			return builds.Add(1), nil
		})
	}

	register()
	if _, err := container.Resolve("singleton"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	container.Clear()
	if _, err := container.Resolve("singleton"); !errors.Is(err, di.ErrServiceNotFound) {
		t.Fatalf("resolve after Clear = %v, want ErrServiceNotFound", err)
	}

	register()
	if _, err := container.Resolve("singleton"); err != nil {
		t.Fatalf("resolve after re-register: %v", err)
	}
	if got := builds.Load(); got != 2 {
		t.Errorf("factories ran %d times, want 2", got)
	}
}

func TestWithScopeRespectsCancelledContext(t *testing.T) {
	container := di.NewContainer()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	err := container.WithScope(ctx, func(*di.Scope) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithScope error = %v, want context.Canceled", err)
	}
	if called {
		t.Error("WithScope ran fn despite a cancelled context")
	}
}

func TestCircularDependencyDetected(t *testing.T) {
	container := di.NewContainer()
	container.RegisterSingleton("a", func(c *di.Container) (interface{}, error) {
		return c.Resolve("b")
	})
	container.RegisterSingleton("b", func(c *di.Container) (interface{}, error) {
		return c.Resolve("a")
	})

	if _, err := container.Resolve("a"); !errors.Is(err, di.ErrCircularDep) {
		t.Fatalf("resolve error = %v, want ErrCircularDep", err)
	}
}

// TestPanickingSingletonDoesNotYieldNil pins that a factory panic (which marks
// the sync.Once done without storing a result) surfaces as an error on the next
// resolve instead of a silent (nil, nil).
func TestPanickingSingletonDoesNotYieldNil(t *testing.T) {
	container := di.NewContainer()
	container.RegisterSingleton("boom", func(*di.Container) (interface{}, error) {
		panic("boom")
	})

	func() {
		defer func() { _ = recover() }()
		_, _ = container.Resolve("boom")
	}()

	value, err := container.Resolve("boom")
	if err == nil || value != nil {
		t.Fatalf("resolve after panic = (%v, %v), want (nil, error)", value, err)
	}
}

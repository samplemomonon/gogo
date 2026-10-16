package di_test

import (
	"testing"

	"gogo/internal/shared/foundation/di"
)

func TestContainer_RegisterAndResolve(t *testing.T) {
	container := di.NewContainer()

	// Register a simple service
	expected := "test-service"
	container.Register("service", expected)

	// Resolve the service
	result, err := container.Resolve("service")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result != expected {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestContainer_RegisterFactory(t *testing.T) {
	container := di.NewContainer()

	// Register a factory
	callCount := 0
	container.RegisterFactory("factory", func(c *di.Container) (interface{}, error) {
		callCount++
		return "instance", nil
	})

	// Resolve twice - factory should be called each time
	container.Resolve("factory")
	container.Resolve("factory")

	if callCount != 2 {
		t.Errorf("expected factory to be called 2 times, got %d", callCount)
	}
}

func TestContainer_RegisterSingleton(t *testing.T) {
	container := di.NewContainer()

	// Register a singleton
	callCount := 0
	container.RegisterSingleton("singleton", func(c *di.Container) (interface{}, error) {
		callCount++
		return "instance", nil
	})

	// Resolve twice - factory should be called only once
	result1, _ := container.Resolve("singleton")
	result2, _ := container.Resolve("singleton")

	if callCount != 1 {
		t.Errorf("expected singleton factory to be called 1 time, got %d", callCount)
	}

	// Should return same instance
	if result1 != result2 {
		t.Error("expected same instance for singleton")
	}
}

func TestContainer_ResolveNotFound(t *testing.T) {
	container := di.NewContainer()

	_, err := container.Resolve("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent service")
	}
}

func TestResolveAs(t *testing.T) {
	container := di.NewContainer()
	container.Register("string", "test")

	result, err := di.ResolveAs[string](container, "string")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if result != "test" {
		t.Errorf("expected 'test', got %v", result)
	}
}

func TestResolveAs_WrongType(t *testing.T) {
	container := di.NewContainer()
	container.Register("string", "test")

	_, err := di.ResolveAs[int](container, "string")
	if err == nil {
		t.Error("expected error for wrong type")
	}
}

func TestBuilder(t *testing.T) {
	builder := di.NewBuilder()

	container := builder.
		AddInstance("config", "test-config").
		AddSingleton("singleton", func(c *di.Container) (interface{}, error) {
			return "singleton-instance", nil
		}).
		AddTransient("transient", func(c *di.Container) (interface{}, error) {
			return "transient-instance", nil
		}).
		Build()

	config, _ := container.Resolve("config")
	if config != "test-config" {
		t.Errorf("expected 'test-config', got %v", config)
	}
}

func TestInjector(t *testing.T) {
	container := di.NewContainer()
	container.Register("logger", "test-logger")

	type Service struct {
		Logger interface{} `inject:"logger"`
	}

	svc := &Service{}
	injector := di.NewInjector(container)

	err := injector.Inject(svc)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if svc.Logger != "test-logger" {
		t.Errorf("expected 'test-logger', got %v", svc.Logger)
	}
}

func TestScope(t *testing.T) {
	container := di.NewContainer()
	container.Register("global", "global-service")

	scope := container.NewScope()
	scope.Register("scoped", "scoped-service")

	// Scoped service should be available
	result, err := scope.Resolve("scoped")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "scoped-service" {
		t.Errorf("expected 'scoped-service', got %v", result)
	}

	// Global service should be available through fallback
	result, err = scope.Resolve("global")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "global-service" {
		t.Errorf("expected 'global-service', got %v", result)
	}
}

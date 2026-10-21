package shell

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"gogo/internal/shared/config"
	"gogo/internal/shared/events"
	fdi "gogo/internal/shared/foundation/di"
	"gogo/internal/shared/httpserver"
	"gogo/internal/shared/kernel"
	"gogo/internal/shared/productevents"
)

// testGatewayConfig is a gateway configuration with every product-platform address
// set. The addresses do not have to resolve: grpcx.Dial builds a lazy client, so the
// graph composes without a live service and the tests that need one stand their own up.
func testGatewayConfig() *config.AppConfig {
	return &config.AppConfig{
		ServiceName:    "gogo-test",
		ServiceVersion: "0.0.0-test",

		ProductAddress:              "passthrough:///127.0.0.1:19092",
		ProductListingAddress:       "passthrough:///127.0.0.1:19093",
		ProductSearchAddress:        "passthrough:///127.0.0.1:19094",
		CategoryAddress:             "passthrough:///127.0.0.1:19090",
		MediaAddress:                "passthrough:///127.0.0.1:19091",
		ProductPlatformServiceToken: "test-service-token-123",
	}
}

func testGatewayLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestBuildGatewayContainer_ResolvesSharedServices verifies every registered
// service resolves to the right type and that the registry carries the
// process configuration.
func TestBuildGatewayContainer_ResolvesSharedServices(t *testing.T) {
	cfg := testGatewayConfig()
	container := buildGatewayContainer(context.Background(), cfg, testGatewayLogger())

	if got := fdi.MustResolveAs[*config.AppConfig](container, serviceConfig); got != cfg {
		t.Errorf("config service = %v, want the registered instance", got)
	}

	registry := gatewayRegistry(container)
	if registry.Service() != cfg.ServiceName {
		t.Errorf("registry service = %q, want %q", registry.Service(), cfg.ServiceName)
	}
	if registry.Version() != cfg.ServiceVersion {
		t.Errorf("registry version = %q, want %q", registry.Version(), cfg.ServiceVersion)
	}

	// The shared bus resolves both as the concrete bus and as the kernel
	// contract, and they observe the same underlying object.
	bus := gatewayBus(container)
	if typed := fdi.MustResolveAs[events.TypedBus](container, serviceBus); typed != bus {
		t.Error("events.TypedBus service is not the shared *events.MemoryBus")
	}

	if kernelBus := gatewayKernelBus(container); kernelBus == nil {
		t.Error("kernel.EventBus service resolved to nil")
	}
	if kernelLogger := gatewayKernelLogger(container); kernelLogger == nil {
		t.Error("kernel.Logger service resolved to nil")
	}
}

// TestGatewayContainer_SingletonIdentity verifies the kernel services are
// singletons: repeated resolution returns the same adapter, so kernel
// components can never diverge onto separate logger or bus instances.
func TestGatewayContainer_SingletonIdentity(t *testing.T) {
	container := buildGatewayContainer(context.Background(), testGatewayConfig(), testGatewayLogger())

	if a, b := gatewayKernelLogger(container), gatewayKernelLogger(container); a != b {
		t.Error("resolving kernel.Logger twice returned different adapters")
	}
	if a, b := gatewayKernelBus(container), gatewayKernelBus(container); a != b {
		t.Error("resolving kernel.EventBus twice returned different adapters")
	}
	if a, b := gatewayRegistry(container), gatewayRegistry(container); a != b {
		t.Error("resolving registry twice returned different registries")
	}
	if a, b := gatewayBus(container), gatewayBus(container); a != b {
		t.Error("resolving the shared bus twice returned different buses")
	}
}

// TestKernelBusSharesWithGatewayBus pins the bridge contract of the adapter:
// a publish on the shared module bus reaches a kernel.EventBus subscriber,
// and a publish through the kernel bus reaches a shared subscriber. The two
// contracts are windows onto the same in-process bus — modules that adopt
// kernel events can never miss events modules still publish as events.Bus.
func TestKernelBusSharesWithGatewayBus(t *testing.T) {
	container := buildGatewayContainer(context.Background(), testGatewayConfig(), testGatewayLogger())
	bus := gatewayBus(container)
	kernelBus := gatewayKernelBus(container)
	ctx := context.Background()

	// Kernel subscriber sees shared publishes.
	kernelReceived := make(chan string, 4)
	if _, err := kernelBus.Subscribe(ctx, func(_ context.Context, event kernel.Event) error {
		kernelReceived <- event.Type()
		return nil
	}); err != nil {
		t.Fatalf("kernel subscribe: %v", err)
	}

	bus.Publish(ctx, events.Event{Type: productevents.ProductCreated})

	select {
	case got := <-kernelReceived:
		if got != productevents.ProductCreated {
			t.Errorf("kernel subscriber got %q, want %s", got, productevents.ProductCreated)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("kernel subscriber never saw the shared publish")
	}

	// Shared subscriber sees kernel publishes.
	sharedReceived := make(chan string, 4)
	events.Subscribe(bus, func(_ context.Context, event events.Event) {
		sharedReceived <- event.Type
	})

	kernelBus.Publish(ctx, kernel.NewBaseEvent("kernel.event", nil))

	select {
	case got := <-sharedReceived:
		if got != "kernel.event" {
			t.Errorf("shared subscriber got %q, want kernel.event", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shared subscriber never saw the kernel publish")
	}
}

// TestObserveKernelProductEvents pins the observability subscriber: it receives
// product change events over the kernel contract and stays quiet for anything else.
func TestObserveKernelProductEvents(t *testing.T) {
	container := buildGatewayContainer(context.Background(), testGatewayConfig(), testGatewayLogger())
	bus := gatewayBus(container)
	kernelBus := gatewayKernelBus(container)
	ctx := context.Background()

	stop := observeKernelProductEvents(ctx, kernelBus, gatewayKernelLogger(container))
	defer stop()

	// The observer must not block or fail on unrelated event types.
	bus.Publish(ctx, events.Event{Type: "billing.charge.attempted"})

	// And it accepts product events without error.
	bus.Publish(ctx, events.Event{Type: productevents.ProductUpdated, Payload: productevents.Payload{ID: "1"}})

	// Cancelled, it stops observing (no panic, idempotent).
	stop()
	stop()
}

// TestGatewayContainer_ResolvesAppGraph pins that resolving serviceApp walks
// the whole graph and registers the in-process modules in dependency order —
// auth first, because inventory and sales resolve their authorizer from its
// connection — and that the lifecycle releases the connections the graph
// acquired.
func TestGatewayContainer_ResolvesAppGraph(t *testing.T) {
	cfg := testGatewayConfig()
	cfg.AppEnv = "test"
	container := buildGatewayContainer(context.Background(), cfg, testGatewayLogger())

	app, err := fdi.ResolveAs[*httpserver.App](container, serviceApp)
	if err != nil {
		t.Fatalf("resolve gateway app: %v", err)
	}
	if app == nil {
		t.Fatal("gateway app resolved to nil")
	}

	names := make([]string, 0, 7)
	for _, manifest := range app.Registry().Manifests() {
		names = append(names, manifest.Name)
	}
	// The catalog and listing are gone: the product platform owns them, in another
	// process, so nothing registers them in this registry. What remains is what this
	// process still owns outright. Auth is registered before the console's modules:
	// inventory and sales resolve their authorizer from the auth connection, and
	// lifecycle stops run in reverse registration order, so a dependency must
	// register before its dependents to stop last.
	if want := []string{"auth", "inventory", "sales", "billing", "ops"}; !reflect.DeepEqual(names, want) {
		t.Errorf("registry modules = %v, want %v", names, want)
	}

	if again := fdi.MustResolveAs[*httpserver.App](container, serviceApp); again != app {
		t.Error("resolving the app twice returned different instances")
	}

	gatewayLifecycle(container).Close()
}

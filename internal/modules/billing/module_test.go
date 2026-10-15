// Package billing_test covers the billing module contract and its cross-module
// behaviour: billing consumes product change events on the shared module event bus,
// exactly like an htmx island reacts to a custom DOM event from elsewhere on the page.
package billing_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"gogo/internal/modules/billing"
	"gogo/internal/shared/events"
	"gogo/internal/shared/foundation/adapter"
	"gogo/internal/shared/kernel"
	"gogo/internal/shared/productevents"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newModule builds billing over a bare bus and registers cleanup, so every test gets a
// module whose subscription is drained.
func newModule(t *testing.T) (*billing.Module, *events.MemoryBus) {
	t.Helper()

	bus := events.NewMemoryBus()
	module, err := billing.NewModule(adapter.NewEventBusAdapter(bus), testLogger())
	if err != nil {
		t.Fatalf("billing.NewModule returned error: %v", err)
	}
	t.Cleanup(func() { _ = module.Stop(context.Background()) })
	return module, bus
}

func newRouter(t *testing.T) (*chi.Mux, *events.MemoryBus) {
	t.Helper()

	module, bus := newModule(t)
	router := chi.NewRouter()
	module.RegisterRoutes(router)
	return router, bus
}

func TestManifest(t *testing.T) {
	t.Parallel()

	module, _ := newModule(t)
	manifest := module.Manifest()

	if manifest.Name != "billing" {
		t.Errorf("Name = %q, want billing", manifest.Name)
	}
	if manifest.Entry != "/billing" {
		t.Errorf("Entry = %q, want /billing", manifest.Entry)
	}
	if _, ok := manifest.Routes["fragment"]; !ok {
		t.Error("manifest should declare the fragment route")
	}
}

func TestIndexPage(t *testing.T) {
	t.Parallel()

	router, _ := newRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/billing", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, want := range []string{"<!doctype html>", "Tagihan"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("landing page missing %q", want)
		}
	}
}

func TestFragment(t *testing.T) {
	t.Parallel()

	router, _ := newRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/billing/fragment", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<html") {
		t.Error("fragment endpoint should return a fragment, not a full document")
	}
	if !strings.Contains(rec.Body.String(), "billing-fragment") {
		t.Error("fragment should carry the module fragment marker")
	}
}

// TestConsumesProductEvents is the cross-module behaviour: product changes published by
// another unit of the system reach the feed without billing importing whoever produced
// them.
func TestConsumesProductEvents(t *testing.T) {
	t.Parallel()

	router, bus := newRouter(t)
	ctx := context.Background()

	bus.Publish(ctx, events.Event{Type: productevents.ProductCreated, Payload: productevents.Payload{ID: "42"}})
	bus.Publish(ctx, events.Event{Type: productevents.ProductUpdated, Payload: productevents.Payload{ID: "42"}})
	bus.Publish(ctx, events.Event{Type: productevents.ProductDeleted, Payload: productevents.Payload{ID: "42"}})

	req := httptest.NewRequest(http.MethodGet, "/billing/fragment", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"42", "created", "updated", "deleted"} {
		if !strings.Contains(body, want) {
			t.Errorf("activity feed missing %q", want)
		}
	}
}

// A stock adjustment is a change to a product and must reach the feed; it is the event a
// seller watches most closely.
func TestConsumesStockAdjustments(t *testing.T) {
	t.Parallel()

	router, bus := newRouter(t)

	bus.Publish(context.Background(), events.Event{
		Type: productevents.ProductStockAdjusted, Payload: productevents.Payload{ID: "7"},
	})

	req := httptest.NewRequest(http.MethodGet, "/billing/fragment", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "7") {
		t.Error("the activity feed does not show a stock adjustment")
	}
}

// Stop must actually end the subscription. Without it the feed lives for the whole
// process and keeps appending after the module has been drained — the leak the module's
// Stop exists to prevent.
func TestStopEndsTheSubscription(t *testing.T) {
	t.Parallel()

	module, bus := newModule(t)
	router := chi.NewRouter()
	module.RegisterRoutes(router)
	ctx := context.Background()

	bus.Publish(ctx, events.Event{Type: productevents.ProductCreated, Payload: productevents.Payload{ID: "before"}})

	if err := module.Stop(ctx); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
	bus.Publish(ctx, events.Event{Type: productevents.ProductCreated, Payload: productevents.Payload{ID: "after"}})

	req := httptest.NewRequest(http.MethodGet, "/billing/fragment", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "before") {
		t.Error("the entry published before Stop is missing; Stop dropped something it should not have")
	}
	if strings.Contains(body, "after") {
		t.Error("an event published after Stop was still recorded")
	}
}

// Stop is idempotent: the lifecycle may drain a module more than once, and a second call
// must not panic on an already-cancelled subscription.
func TestStopIsIdempotent(t *testing.T) {
	t.Parallel()

	module, _ := newModule(t)
	ctx := context.Background()

	if err := module.Stop(ctx); err != nil {
		t.Fatalf("first Stop = %v, want nil", err)
	}
	if err := module.Stop(ctx); err != nil {
		t.Errorf("second Stop = %v, want nil", err)
	}
}

// A module that cannot subscribe must fail to build rather than mount a feed that
// silently never updates — that failure mode was why the subscription error is handled
// at all.
func TestNewModuleRejectsABusThatCannotSubscribe(t *testing.T) {
	t.Parallel()

	if _, err := billing.NewModule(unsubscriber{}, testLogger()); err == nil {
		t.Error("NewModule with a bus that cannot subscribe = nil error, want a failure")
	}
}

// unsubscriber is a kernel.EventBus whose subscriptions always fail.
type unsubscriber struct{}

var errUnsub = errors.New("test: subscription refused")

func (unsubscriber) Subscribe(context.Context, kernel.EventHandler) (kernel.Subscription, error) {
	return nil, errUnsub
}

func (unsubscriber) SubscribeTypes(context.Context, kernel.EventHandler, ...string) (kernel.Subscription, error) {
	return nil, errUnsub
}

func (unsubscriber) Publish(context.Context, kernel.Event) {}

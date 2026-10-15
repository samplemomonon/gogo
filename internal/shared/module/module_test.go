package module

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"gogo/internal/shared/web/navigation"
)

// fixtureModule is a minimal module used across the registry tests.
type fixtureModule struct {
	manifest Descriptor
}

func (f fixtureModule) Manifest() Descriptor { return f.manifest }

func (f fixtureModule) RegisterRoutes(r chi.Router) {
	r.Get(f.manifest.Entry, func(http.ResponseWriter, *http.Request) {})
}

func testModules() []Interface {
	return []Interface{
		fixtureModule{manifest: Descriptor{Name: "catalog", Version: "1.4.0", Entry: "/"}},
		fixtureModule{manifest: Descriptor{Name: "ops", Version: "1.0.0", Entry: "/healthz"}},
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	registry := New("gogo", "dev", testModules()...)

	got := registry.Manifests()
	if len(got) != 2 {
		t.Fatalf("len(Manifests) = %d, want 2", len(got))
	}
	if got[0].Name != "catalog" || got[1].Name != "ops" {
		t.Errorf("registration order not preserved: %v %v", got[0].Name, got[1].Name)
	}
}

func TestRegistryGet(t *testing.T) {
	t.Parallel()

	registry := New("gogo", "dev", testModules()...)

	manifest, ok := registry.Get("ops")
	if !ok {
		t.Fatal("Get(ops) should hit")
	}
	if manifest.Version != "1.0.0" || manifest.Entry != "/healthz" {
		t.Errorf("manifest = %+v, want version 1.0.0 entry /healthz", manifest)
	}

	if _, ok := registry.Get("nope"); ok {
		t.Error("Get(nope) should not hit")
	}
}

func TestRegistryAddDuplicatePanics(t *testing.T) {
	t.Parallel()

	registry := NewRegistry("gogo", "dev")
	registry.Add(testModules()[0])

	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration should panic")
		}
	}()
	registry.Add(testModules()[0])
}

func TestRegistryAddWithoutNamePanics(t *testing.T) {
	t.Parallel()

	registry := NewRegistry("gogo", "dev")

	defer func() {
		if recover() == nil {
			t.Fatal("registering an unnamed module should panic")
		}
	}()
	registry.Add(fixtureModule{})
}

func TestRegistryNav(t *testing.T) {
	t.Parallel()

	modules := []Interface{
		fixtureModule{manifest: Descriptor{
			Name: "catalog", Version: "1.4.0", Entry: "/",
			Nav: []navigation.Item{{Label: "Catalog", Path: "/"}, {Label: "Admin", Path: "/admin"}},
		}},
		fixtureModule{manifest: Descriptor{
			Name: "billing", Version: "0.1.0", Entry: "/billing",
			Nav: []navigation.Item{{Label: "Billing", Path: "/billing"}},
		}},
		fixtureModule{manifest: Descriptor{Name: "ops", Version: "1.0.0", Entry: "/healthz"}},
	}

	registry := New("gogo", "dev", modules...)

	got := registry.Nav()
	want := []navigation.Item{
		{Label: "Catalog", Path: "/"},
		{Label: "Admin", Path: "/admin"},
		{Label: "Billing", Path: "/billing"},
	}
	if len(got) != len(want) {
		t.Fatalf("len(Nav) = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Nav[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestModuleRegistryJSONEndpoint(t *testing.T) {
	t.Parallel()

	registry := New("gogo", "dev", testModules()...)
	server := httptest.NewServer(registry.Handler())
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/module-registry")
	if err != nil {
		t.Fatalf("GET %s failed: %v", server.URL, err)
	}
	defer resp.Body.Close()

	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	var body struct {
		Service string       `json:"service"`
		Modules []Descriptor `json:"modules"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode registry JSON failed: %v", err)
	}
	if body.Service != "gogo" {
		t.Errorf("service = %q, want gogo", body.Service)
	}
	if len(body.Modules) != 2 {
		t.Fatalf("len(modules) = %d, want 2", len(body.Modules))
	}
	if body.Modules[0].Name != "catalog" || body.Modules[0].Version != "1.4.0" {
		t.Errorf("first module = %+v, want catalog 1.4.0", body.Modules[0])
	}
}

func TestNavContext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if nav := navigation.NavFromContext(ctx); nav != nil {
		t.Fatalf("NavFromContext(empty) = %v, want nil", nav)
	}

	items := []navigation.Item{{Label: "Ops", Path: "/healthz"}}
	ctx = navigation.WithNav(ctx, items)

	got := navigation.NavFromContext(ctx)
	if len(got) != 1 || got[0] != items[0] {
		t.Errorf("NavFromContext = %v, want %v", got, items)
	}
}

func TestRegistryHandlerRejectsPost(t *testing.T) {
	t.Parallel()

	registry := New("gogo", "dev", testModules()...)
	server := httptest.NewServer(registry.Handler())
	t.Cleanup(server.Close)

	resp, err := http.Post(server.URL+"/module-registry", "text/plain", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

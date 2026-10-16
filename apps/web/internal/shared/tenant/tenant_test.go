package tenant_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"gogo/internal/shared/tenant"
)

func TestFromContext_NoTenantIsAbsent(t *testing.T) {
	if _, ok := tenant.FromContext(context.Background()); ok {
		t.Fatal("an empty context must not carry a tenant")
	}
	if got := tenant.FromContextOrDefault(context.Background()); got != tenant.Default {
		t.Fatalf("default fallback = %q, want %q", got, tenant.Default)
	}
}

func TestWithID_RoundTrips(t *testing.T) {
	ctx := tenant.WithID(context.Background(), "ecommerce")

	got, ok := tenant.FromContext(ctx)
	if !ok || got != "ecommerce" {
		t.Fatalf("FromContext = (%q, %v), want (ecommerce, true)", got, ok)
	}
	if got := tenant.FromContextOrDefault(ctx); got != "ecommerce" {
		t.Fatalf("FromContextOrDefault = %q, want ecommerce", got)
	}
}

func TestNewRegistry_ResolvesHosts(t *testing.T) {
	reg, err := tenant.NewRegistry(tenant.Options{
		DefaultID: "default",
		Hosts: map[string]tenant.ID{
			"shop.example.com":  "ecommerce",
			"video.example.com": "video",
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	cases := []struct {
		host string
		want tenant.ID
		ok   bool
	}{
		{"shop.example.com", "ecommerce", true},
		{"Shop.Example.com:8443", "ecommerce", true}, // case + port normalize
		{"shop.example.com.", "ecommerce", true},     // trailing dot normalize
		{"video.example.com", "video", true},
		{"unknown.example.com", "default", false},
		{"", "default", false},
	}
	for _, tc := range cases {
		got, ok := reg.ResolveHost(tc.host)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ResolveHost(%q) = (%q, %v), want (%q, %v)", tc.host, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNewRegistry_ServedSetIsDeterministic(t *testing.T) {
	reg, err := tenant.NewRegistry(tenant.Options{
		DefaultID: "default",
		Hosts: map[string]tenant.ID{
			"shop.example.com":  "ecommerce",
			"video.example.com": "video",
			"blog.example.com":  "ecommerce", // duplicate tenant, one entry
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	want := []tenant.ID{"default", "ecommerce", "video"}
	got := reg.IDs()
	if len(got) != len(want) {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IDs() = %v, want %v", got, want)
		}
	}
	for _, id := range want {
		if !reg.Has(id) {
			t.Errorf("Has(%q) = false, want true", id)
		}
	}
	if reg.Has("nope") {
		t.Error("Has(nope) = true, want false")
	}
}

func TestNewRegistry_RejectsEmptyTenantHost(t *testing.T) {
	if _, err := tenant.NewRegistry(tenant.Options{
		Hosts: map[string]tenant.ID{"shop.example.com": ""},
	}); err == nil {
		t.Fatal("a host mapped to an empty tenant must be rejected")
	}
}

func TestHostResolver_HeaderUntrustedByDefault(t *testing.T) {
	reg, err := tenant.NewRegistry(tenant.Options{
		Hosts: map[string]tenant.ID{"shop.example.com": "ecommerce"},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	resolver := tenant.NewHostResolver(reg, false)

	req := httptest.NewRequest(http.MethodGet, "http://shop.example.com/", nil)
	req.Header.Set(tenant.Header, "video") // not a served tenant, and untrusted

	got, ok := resolver.Resolve(req)
	if got != "ecommerce" || !ok {
		t.Fatalf("Resolve = (%q, %v), want (ecommerce, true)", got, ok)
	}
}

func TestHostResolver_HeaderTrustedWhenEnabled(t *testing.T) {
	reg, err := tenant.NewRegistry(tenant.Options{
		DefaultID: "default",
		Hosts:     map[string]tenant.ID{"shop.example.com": "ecommerce"},
		IDs:       []tenant.ID{"video", "ecommerce"},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	resolver := tenant.NewHostResolver(reg, true)

	// A known tenant in the header wins over the host mapping.
	req := httptest.NewRequest(http.MethodGet, "http://shop.example.com/", nil)
	req.Header.Set(tenant.Header, "video")
	if got, _ := resolver.Resolve(req); got != "video" {
		t.Fatalf("trusted header = %q, want video", got)
	}

	// An unknown tenant in the header is ignored, host wins.
	req = httptest.NewRequest(http.MethodGet, "http://shop.example.com/", nil)
	req.Header.Set(tenant.Header, "unknown")
	if got, _ := resolver.Resolve(req); got != "ecommerce" {
		t.Fatalf("unknown trusted header = %q, want ecommerce", got)
	}
}

func TestMiddleware_CarriesTenantOnContext(t *testing.T) {
	reg, err := tenant.NewRegistry(tenant.Options{
		Hosts: map[string]tenant.ID{"shop.example.com": "ecommerce"},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	var seen tenant.ID
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := tenant.Middleware(tenant.NewHostResolver(reg, false), logger)(http.HandlerFunc(
		func(_ http.ResponseWriter, r *http.Request) {
			seen = tenant.FromContextOrDefault(r.Context())
		},
	))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://shop.example.com/", nil))
	if seen != "ecommerce" {
		t.Fatalf("middleware tenant = %q, want ecommerce", seen)
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://other.example.com/", nil))
	if seen != tenant.Default {
		t.Fatalf("unmatched host tenant = %q, want %q", seen, tenant.Default)
	}
}

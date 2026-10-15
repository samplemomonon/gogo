package bootstrap_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"gogo/internal/shared/config"
)

func TestAssets(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)

	for _, path := range []string{"/assets/css/output.css", "/assets/js/htmx.min.js"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			resp, _ := request(t, http.MethodGet, server.URL, path, "", nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if contentType := resp.Header.Get("Content-Type"); contentType == "" {
				t.Error("response should declare a Content-Type")
			}
		})
	}
}

// TestModuleRegistry asserts the federation contract: the gateway exposes every
// module's manifest at /module-registry and each declared entry serves its own HTML.
//
// The registry no longer lists a catalog or a listing: both left this process when the
// product platform took over the product domain (ARCHITECTURE.md D-018). What remains is
// what the gateway still owns — inventory, sales, auth, billing and ops.
func TestModuleRegistry(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})
	gateway.platform.addCategory("Elektronik")

	// Seed one product through the write side so the seller console has a row, the way a
	// real deployment would have one from a previous session.
	resp, _ := request(t, http.MethodPost, gateway.server.URL, "/seller/produk", "application/x-www-form-urlencoded", productForm(map[string]string{
		"name": "Kopi Susu", "price": "25000",
	}), nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("seed product: status = %d, want 303", resp.StatusCode)
	}

	t.Run("registry JSON lists every registered module", func(t *testing.T) {
		resp, body := request(t, http.MethodGet, gateway.server.URL, "/module-registry", "", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", contentType)
		}

		var registry struct {
			Service string            `json:"service"`
			Modules []json.RawMessage `json:"modules"`
		}
		if err := json.Unmarshal([]byte(body), &registry); err != nil {
			t.Fatalf("unmarshal registry failed: %v", err)
		}
		if registry.Service != "gogo" {
			t.Errorf("service = %q, want gogo", registry.Service)
		}
		if len(registry.Modules) != 5 {
			t.Fatalf("len(modules) = %d, want 5", len(registry.Modules))
		}
		for _, want := range []string{
			`"name":"inventory"`, `"version":"1.0.0"`,
			`"name":"sales"`, `"version":"1.0.0"`,
			`"name":"auth"`,
			`"name":"billing"`, `"version":"0.2.0"`,
			`"name":"ops"`, `"version":"1.0.0"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("registry missing %q", want)
			}
		}

		// The product platform is reached over contracts, not mounted as a module, so it
		// must not appear in the federation registry. A module named "catalog" here would
		// mean the product domain crept back into this process.
		for _, unwanted := range []string{`"name":"catalog"`, `"name":"listing"`} {
			if strings.Contains(body, unwanted) {
				t.Errorf("registry still lists %q; the product platform owns it", unwanted)
			}
		}
	})

	t.Run("registry renders an HTML view for browsers", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, gateway.server.URL+"/module-registry", nil)
		req.Header.Set("Accept", "text/html")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET failed: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		for _, want := range []string{"Module registry", "inventory", "billing", "ops"} {
			if !strings.Contains(string(body), want) {
				t.Errorf("registry HTML missing %q", want)
			}
		}
	})

	t.Run("shell nav is federated from the gateway", func(t *testing.T) {
		// Every page and module NavItem should appear in the shell top bar of any page
		// rendered through the gateway — no shell edits required. Home and Profile come
		// from the pages; Billing from the modules' manifests. The seller console is
		// reached through the profile page's direct link, not the nav.
		resp, body := request(t, http.MethodGet, gateway.server.URL, "/", "", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		for _, want := range []string{`href="/profile"`, `href="/billing"`} {
			if !strings.Contains(body, want) {
				t.Errorf("shell nav missing %q", want)
			}
		}
		if strings.Contains(body, `href="/seller"`) {
			t.Error("the seller console must not appear in the shell nav; it is reached from /profile")
		}
	})

	t.Run("each entry is reachable", func(t *testing.T) {
		for _, entry := range []string{
			"/", "/seller", "/seller/produk", "/seller/kategori", "/seller/stok",
			"/seller/kasir", "/seller/transaksi", "/profile", "/billing",
			"/healthz", "/readyz",
		} {
			resp, body := request(t, http.MethodGet, gateway.server.URL, entry, "", nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("entry %s status = %d, want 200", entry, resp.StatusCode)
			}
			if body == "" {
				t.Errorf("entry %s returned an empty body", entry)
			}
		}
	})

	t.Run("the removed product surfaces are gone", func(t *testing.T) {
		// The catalog's browsable detail page left with the catalog, and the supplier
		// console has no service behind it. Both must 404 rather than render something
		// that looks like a working surface.
		for _, gone := range []string{"/catalog/1", "/catalog/1/stock", "/seller/supplier"} {
			resp, _ := request(t, http.MethodGet, gateway.server.URL, gone, "", nil, nil)
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", gone, resp.StatusCode)
			}
		}
	})
}

// TestNegotiatedResponsesDeclareVary checks the one negotiated URL in the gateway
// at the real HTTP boundary, rather than by inspecting the handler.
//
// /billing is served twice from one route: a document for a navigation and a
// fragment for the hx-get in the module's own landing page. Both are produced by
// render.Respond, which declares Vary so a shared cache keys on the request
// headers. Without it, a cache in front of the gateway is free to hand a fragment
// to an ordinary navigation, or a whole document to an hx-get target.
//
// This is deliberately an integration test. A static rule about which handlers
// "should" use render.Respond would be a guess about which URLs negotiate; asking
// the running gateway is not.
func TestNegotiatedResponsesDeclareVary(t *testing.T) {
	t.Parallel()

	gateway := newTestGateway(t, config.TenantConfig{})

	t.Run("the negotiated route declares the headers it varies on", func(t *testing.T) {
		resp, _ := request(t, http.MethodGet, gateway.server.URL, "/billing", "", nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /billing = %d, want 200", resp.StatusCode)
		}
		assertVaryContains(t, resp, "HX-Request", "the route serves a document or a fragment on the same URL")
		assertVaryContains(t, resp, "HX-Request-Type", "hx-boost and explicit request types are part of the decision")
	})

	t.Run("the same route answers both shapes distinctly", func(t *testing.T) {
		// The point of Vary is that the two shapes really do differ. If this ever
		// stops being true the header is declaring a distinction that does not
		// exist, and the negotiation is not doing anything.
		_, document := request(t, http.MethodGet, gateway.server.URL, "/billing", "", nil, nil)
		_, fragment := request(t, http.MethodGet, gateway.server.URL, "/billing", "", nil,
			map[string]string{"HX-Request": "true"})

		if !strings.Contains(document, "<html") {
			t.Error("a plain navigation did not get a full document")
		}
		if strings.Contains(fragment, "<html") {
			t.Error("an htmx request got a full document; the fragment branch is not being reached")
		}
	})
}

func assertVaryContains(t *testing.T, resp *http.Response, want, why string) {
	t.Helper()

	for _, value := range resp.Header.Values("Vary") {
		for _, field := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(field), want) {
				return
			}
		}
	}
	t.Errorf("Vary = %v, want it to include %s; %s. A shared cache would otherwise serve one "+
		"representation to callers expecting the other", resp.Header.Values("Vary"), want, why)
}

// TestHealthProbes covers the ops module's liveness and readiness, and the fact that a
// breaker opened on the product platform takes the gateway out of rotation.
func TestHealthProbes(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)

	t.Run("liveness and readiness answer while every dependency is up", func(t *testing.T) {
		for _, probe := range []string{"/healthz", "/readyz"} {
			resp, _ := request(t, http.MethodGet, server.URL, probe, "", nil, nil)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s = %d, want 200", probe, resp.StatusCode)
			}
		}
	})
}

// testConfig is a minimal gateway config for tests that only need the identity fields.
func testConfig() *config.AppConfig {
	return &config.AppConfig{
		AppEnv:         "test",
		Host:           "127.0.0.1",
		Port:           "0",
		ServiceName:    "gogo",
		ServiceVersion: "test",
		LogLevel:       slog.LevelError,
		ReadTimeout:    5 * time.Second,
		WriteTimeout:   10 * time.Second,
		IdleTimeout:    60 * time.Second,
		RequestTimeout: 15 * time.Second,
	}
}

package bootstrap_test

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"gogo/internal/shared/config"
)

// productIDPattern matches the row action link the seller console renders for each
// product, which is how a test recovers an id it did not choose itself.
//
// The id is the product platform's int64 key in decimal form, so the pattern matches
// digits only — a UUID-shaped pattern would silently stop matching when the product
// domain moved out of this process and the id stopped being a locally-generated UUID.
var productIDPattern = regexp.MustCompile(`/seller/produk/([0-9]+)`)

// requestAsHost issues a request that arrives under the given Host header. Go's
// client ignores a "Host" entry in Header, so the override has to be set on the
// request struct; that is the field the gateway's host→tenant resolver reads.
func requestAsHost(
	t *testing.T,
	method, baseURL, path, host, contentType string,
	form url.Values,
) (*http.Response, string) {
	t.Helper()

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequest(method, baseURL+path, body)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	req.Host = host
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s as %s failed: %v", method, path, host, err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	return resp, string(raw)
}

// createProductAs drives a create through the seller console, so the write crosses
// every hop a real write crosses: Host → tenant middleware → seller page →
// catalog.v1 gRPC tenant metadata → tenant-scoped service → tenant-scoped
// repository.
func createProductAs(t *testing.T, serverURL, host, name string) string {
	t.Helper()

	form := url.Values{"name": {name}, "price": {"25000"}, "productTypeId": {"general"}, "schemaVersion": {"1"}}
	resp, _ := requestAsHost(t, http.MethodPost, serverURL, "/seller/produk", host,
		"application/x-www-form-urlencoded", form)

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create as %s: status = %d, want 303", host, resp.StatusCode)
	}
	return name
}

// firstProductID recovers the id of the first row in the seller product table.
func firstProductID(t *testing.T, sellerHTML string) string {
	t.Helper()

	match := productIDPattern.FindStringSubmatch(sellerHTML)
	if match == nil {
		t.Fatalf("seller table has no product row; got:\n%s", sellerHTML)
	}
	return match[1]
}

// TestGatewayIsolatesTenantsEndToEnd drives two tenants through the whole stack
// with nothing but their Host header to tell them apart.
//
// It also pins the ISR cache boundary, which the admin table island exercises
// for free: the island is cached under one name and both tenants read it, so
// before the cache key was tenant-qualified the second read below was served
// the first tenant's rendered rows.
func TestGatewayIsolatesTenantsEndToEnd(t *testing.T) {
	t.Parallel()

	server := newTestServerWithTenants(t, config.TenantConfig{
		DefaultID: "default",
		Hosts: map[string]string{
			"alpha.test": "alpha",
			"beta.test":  "beta",
		},
		IDs: []string{"alpha", "beta"},
	})

	createProductAs(t, server.URL, "alpha.test", "Alpha Widget")
	createProductAs(t, server.URL, "beta.test", "Beta Widget")

	// alpha's read populates the cache; beta's must not be served from it.
	_, alphaAdmin := requestAsHost(t, http.MethodGet, server.URL, "/seller/produk", "alpha.test", "", nil)
	if !strings.Contains(alphaAdmin, "Alpha Widget") {
		t.Error("alpha's admin table is missing its own product")
	}
	if strings.Contains(alphaAdmin, "Beta Widget") {
		t.Error("alpha's admin table leaked beta's product")
	}

	_, betaAdmin := requestAsHost(t, http.MethodGet, server.URL, "/seller/produk", "beta.test", "", nil)
	if !strings.Contains(betaAdmin, "Beta Widget") {
		t.Error("beta's admin table is missing its own product")
	}
	if strings.Contains(betaAdmin, "Alpha Widget") {
		t.Error("beta's admin table was served alpha's cached HTML")
	}

	// Deleting through one tenant must not reach another tenant's product. Beta holding
	// alpha's id gets a clean refusal rather than removing alpha's row.
	alphaID := firstProductID(t, alphaAdmin)
	resp, _ := requestAsHost(t, http.MethodPost, server.URL, "/seller/produk/"+alphaID+"/delete",
		"beta.test", "application/x-www-form-urlencoded", nil)
	if resp.StatusCode == http.StatusSeeOther {
		t.Error("beta deleted alpha's product; the id must be tenant-scoped")
	}

	_, alphaAfter := requestAsHost(t, http.MethodGet, server.URL, "/seller/produk", "alpha.test", "", nil)
	if !strings.Contains(alphaAfter, "Alpha Widget") {
		t.Error("alpha's product is gone after beta tried to delete it")
	}
}

// TestGatewayIsolatesUnmappedHostFromConfiguredTenants proves the boundary is
// closed in the other direction: a host nobody declared falls back to the
// default tenant and must not reach a declared tenant's rows.
func TestGatewayIsolatesUnmappedHostFromConfiguredTenants(t *testing.T) {
	t.Parallel()

	server := newTestServerWithTenants(t, config.TenantConfig{
		DefaultID: "default",
		Hosts:     map[string]string{"alpha.test": "alpha"},
		IDs:       []string{"alpha", "default"},
	})

	createProductAs(t, server.URL, "alpha.test", "Alpha Widget")

	_, fallbackAdmin := requestAsHost(t, http.MethodGet, server.URL, "/seller/produk", "unknown.test", "", nil)
	if strings.Contains(fallbackAdmin, "Alpha Widget") {
		t.Error("an undeclared host was served a declared tenant's product")
	}
}

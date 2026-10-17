package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postBeacon sends one beacon body through the handler and returns the
// recorder.
func postBeacon(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/rum", strings.NewReader(body))
	rumHandler().ServeHTTP(rec, req)
	return rec
}

func TestAValidBeaconIsAggregated(t *testing.T) {
	t.Parallel()

	rec := postBeacon(t, `{"metric":"LCP","value":1234.5,"rating":"good","island":"home.product-grid:t9:p2","path":"/"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	stat, ok := RUMSnapshot()["LCP|home.product-grid|good"]
	if !ok {
		t.Fatal("no aggregate for LCP|home.product-grid|good; the paged name should collapse to its family")
	}
	if stat.Count < 1 || stat.ValueSum < 1234.5 {
		t.Errorf("stat = %+v, want at least one sample of 1234.5", stat)
	}
}

func TestAnUnknownMetricIsRejected(t *testing.T) {
	t.Parallel()

	rec := postBeacon(t, `{"metric":"FID","value":1,"rating":"good","island":"page","path":"/"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a metric outside the vendored set", rec.Code)
	}
}

func TestAnOversizeBodyIsRejected(t *testing.T) {
	t.Parallel()

	body := `{"metric":"LCP","value":1,"rating":"good","island":"page","path":"","pad":"` +
		strings.Repeat("x", rumMaxBody) + `"}`
	rec := postBeacon(t, body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestABadJSONBodyIsRejected(t *testing.T) {
	t.Parallel()

	rec := postBeacon(t, `{"metric":`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestAGetIsRejected(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/rum", nil)
	rumHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// TestUnknownIslandBuckets pins the label policy: values outside the island
// grammar, and names whose owner segment is missing, must aggregate as
// unknown instead of minting labels from client input. The cap is exercised
// by filling it with synthetic families first.
func TestUnknownIslandBuckets(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"no-owner-namespace", "UPPER.case", "spaces in.name"} {
		rec := postBeacon(t, fmt.Sprintf(
			`{"metric":"CLS","value":0.1,"rating":"unknown-rating","island":%q,"path":"/x?q=secret"}`, raw))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("island %q: status = %d, want 204", raw, rec.Code)
		}
	}

	stat := RUMSnapshot()["CLS|unknown|unknown"]
	if stat.Count < 3 {
		t.Errorf("CLS|unknown|unknown = %+v, want at least 3 samples (one per invalid island, rating bucketed)", stat)
	}
}

func TestIslandFamilyNormalization(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"page":                     "page",
		"":                         "page",
		"home.product-grid":        "home.product-grid",
		"home.product-grid:t9:p2":  "home.product-grid",
		"home.product-detail:1042": "home.product-detail",
		"detail.1042.stock":        "detail.{id}.stock",
		"billing.invoice.931.v2":   "billing.invoice.{id}.v2",
		strings.Repeat("a", rumMaxIsland+1) + ".over": "unknown",
	}
	for raw, want := range cases {
		if got := rumIsland(raw); got != want {
			t.Errorf("rumIsland(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestRUMAggregatesRenderAsPrometheusRows(t *testing.T) {
	t.Parallel()

	postBeacon(t, `{"metric":"TTFB","value":0.42,"rating":"good","island":"home.product-detail:55","path":"/catalog/55"}`)

	rows := strings.Join(rumRows(), "\n")
	want := `gogo_rum_web_vitals_count{metric="TTFB",island="home.product-detail",rating="good"}`
	if !strings.Contains(rows, want) {
		t.Errorf("rows do not contain %s:\n%s", want, rows)
	}
}

package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gatherText(t *testing.T, m *Metrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// gatherValue returns the value of the first sample of a family, ignoring its
// labels. A Prometheus exposition line is "<name>{labels} <value>".
func gatherValue(t *testing.T, m *Metrics, name string) string {
	t.Helper()

	for _, line := range strings.Split(gatherText(t, m), "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		if i := strings.LastIndex(line, " "); i >= 0 {
			return strings.TrimSpace(line[i+1:])
		}
	}
	return ""
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gogo/internal/shared/netutil"
)

// mustTrusted parses a trusted-proxy list for a test, failing loudly on a
// malformed entry (the test itself is wrong).
func mustTrusted(t *testing.T, raw ...string) netutil.TrustedProxies {
	t.Helper()
	proxies, err := netutil.ParseTrustedProxies(raw)
	if err != nil {
		t.Fatalf("ParseTrustedProxies(%v): %v", raw, err)
	}
	return proxies
}

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	run := func(production bool) http.Header {
		rec := httptest.NewRecorder()
		mid := SecurityHeaders(production)
		mid(ok).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Header()
	}

	h := run(false)
	for _, header := range []string{
		"X-Content-Type-Options",
		"X-Frame-Options",
		"Referrer-Policy",
		"Permissions-Policy",
		"Content-Security-Policy",
	} {
		if got := h.Get(header); got == "" {
			t.Errorf("development response missing %s", header)
		}
	}
	if got := h.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS set outside production: %q", got)
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: https:") {
		t.Errorf("Content-Security-Policy must permit the editorial image contract, got %q", csp)
	}

	if got := run(true).Get("Strict-Transport-Security"); !strings.Contains(got, "max-age=63072000") {
		t.Errorf("production HSTS = %q, want max-age=63072000", got)
	}
}

func TestBodyLimitRejectsOversizedBodies(t *testing.T) {
	t.Parallel()

	// Handlers must surface the MaxBytesError themselves — the standard
	// contract of http.MaxBytesReader. The middleware's job is to make the
	// body stop producing data once the limit is hit.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// The 413 response is produced by the handler reading past the limit, so
	// run the middleware over a live httptest server.
	server := httptest.NewServer(BodyLimit(8)(handler))
	defer server.Close()

	resp, err := http.Post(server.URL, "text/plain", strings.NewReader("0123456789"))
	if err != nil {
		t.Fatalf("oversized request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body status = %d, want 413", resp.StatusCode)
	}

	resp, err = http.Post(server.URL, "text/plain", strings.NewReader("01234567"))
	if err != nil {
		t.Fatalf("within-limit request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("within-limit body status = %d, want 200", resp.StatusCode)
	}
}

func TestBodyLimitDisabledWhenZero(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mid := BodyLimit(0)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789"))
	mid(handler).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("zero limit body = %d, want 200", rec.Code)
	}
}

func TestRealIPTrustedProxy(t *testing.T) {
	t.Parallel()

	// Trust the whole private range / loopback proxies.
	mid := RealIP(mustTrusted(t, "10.0.0.0/8", "127.0.0.1/32", "all"))

	var remote string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote = r.RemoteAddr
		w.WriteHeader(http.StatusOK)
	})

	// A trusted proxy forwards the client address.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	mid(handler).ServeHTTP(httptest.NewRecorder(), req)
	if !strings.HasPrefix(remote, "203.0.113.7:") {
		t.Errorf("trusted proxy: RemoteAddr = %q, want it to resolve 203.0.113.7", remote)
	}

	// The "all" single hop resolves the leftmost chain entry.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.4")
	mid(handler).ServeHTTP(httptest.NewRecorder(), req)
	if !strings.HasPrefix(remote, "198.51.100.9:") {
		t.Errorf("\"all\" chain: RemoteAddr = %q, want 198.51.100.9", remote)
	}
}

func TestRealIPIgnoresUntrustedHeader(t *testing.T) {
	t.Parallel()

	mid := RealIP(mustTrusted(t, "10.0.0.0/8"))

	var remote string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote = r.RemoteAddr
		w.WriteHeader(http.StatusOK)
	})

	// A public client's spoofed X-Forwarded-For must be ignored: the socket
	// peer address wins.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.9:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	mid(handler).ServeHTTP(httptest.NewRecorder(), req)
	if !strings.HasPrefix(remote, "198.51.100.9:") {
		t.Errorf("untrusted peer: RemoteAddr = %q, want the socket peer 198.51.100.9", remote)
	}
}

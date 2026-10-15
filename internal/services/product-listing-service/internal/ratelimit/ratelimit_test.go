package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterAllow(t *testing.T) {
	t.Parallel()

	l := New(Options{Rate: 10, Burst: 5})
	defer l.Stop()

	for i := 0; i < 5; i++ {
		if !l.Allow("client1") {
			t.Errorf("request %d should be allowed", i)
		}
	}
	if l.Allow("client1") {
		t.Error("request 6 should be rejected (burst exceeded)")
	}
}

func TestLimiterTokenRefill(t *testing.T) {
	t.Parallel()

	l := New(Options{Rate: 100, Burst: 1})
	defer l.Stop()

	if !l.Allow("client1") {
		t.Fatal("first request should be allowed")
	}
	if l.Allow("client1") {
		t.Fatal("second request should be rejected")
	}

	time.Sleep(20 * time.Millisecond)

	if !l.Allow("client1") {
		t.Error("request after refill should be allowed")
	}
}

func TestLimiterPerClient(t *testing.T) {
	t.Parallel()

	l := New(Options{Rate: 10, Burst: 1})
	defer l.Stop()

	if !l.Allow("client1") {
		t.Fatal("client1 first request should be allowed")
	}
	if !l.Allow("client2") {
		t.Fatal("client2 first request should be allowed")
	}
	if l.Allow("client1") {
		t.Error("client1 second request should be rejected")
	}
}

func TestLimiterMiddleware(t *testing.T) {
	t.Parallel()

	l := New(Options{Rate: 10, Burst: 1})
	defer l.Stop()

	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:12345"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("first request: got %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("second request: got %d, want 429", rec.Code)
	}
}

// TestLimiterMiddlewareAdvertisesLimit asserts the 429 response tells the client
// what the limit actually is.
//
// The value used to be built with string(rune(burst)), which converts an int to a
// code point rather than to digits. Burst 1 produced U+0001 and burst 150
// produced U+0096, so the header always carried a control character. Nothing
// caught it because the only middleware test asserted the status code.
func TestLimiterMiddlewareAdvertisesLimit(t *testing.T) {
	t.Parallel()

	const burst = 150
	l := New(Options{Rate: 10, Burst: burst})
	defer l.Stop()

	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.7:54321"

	// Drain the bucket so the next request is refused.
	for i := 0; i < burst; i++ {
		l.Allow("10.0.0.7")
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429", rec.Code)
	}

	got := rec.Header().Get("X-RateLimit-Limit")
	if got != "150" {
		t.Errorf("X-RateLimit-Limit = %q (bytes %v), want %q", got, []byte(got), "150")
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Errorf("X-RateLimit-Remaining = %q, want %q", got, "0")
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("Retry-After must be set on a 429")
	}
}

func TestLimiterCleanup(t *testing.T) {
	t.Parallel()

	l := New(Options{Rate: 10, Burst: 5, CleanupInterval: 10 * time.Millisecond})
	defer l.Stop()

	l.Allow("client1")
	l.Allow("client2")

	time.Sleep(20 * time.Millisecond)

	l.cleanupIdle()

	l.mu.Lock()
	clients := len(l.clients)
	l.mu.Unlock()

	if clients != 0 {
		t.Errorf("expected 0 clients after cleanup, got %d", clients)
	}
}

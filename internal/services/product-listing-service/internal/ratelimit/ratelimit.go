// Package ratelimit provides a token bucket rate limiter for HTTP requests.
//
// The rate limiter is per-client, keyed by IP address, and is designed to
// protect the service from accidental or malicious overload. It is not a
// substitute for a proper API gateway rate limiter, but it provides a first
// line of defense for the service when deployed without one.
package ratelimit

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Limiter is a token bucket rate limiter.
type Limiter struct {
	mu       sync.Mutex
	clients  map[string]*client
	rate     float64
	burst    int
	cleanup  time.Duration
	stopOnce sync.Once
	stopCh   chan struct{}
}

type client struct {
	tokens   float64
	lastSeen time.Time
}

// Options configures a Limiter.
type Options struct {
	// Rate is the number of tokens added per second.
	Rate float64
	// Burst is the maximum number of tokens a client can accumulate.
	Burst int
	// CleanupInterval is how often to remove idle clients.
	CleanupInterval time.Duration
}

// New creates a new rate limiter.
func New(opts Options) *Limiter {
	if opts.Rate <= 0 {
		opts.Rate = 10
	}
	if opts.Burst <= 0 {
		opts.Burst = 20
	}
	if opts.CleanupInterval <= 0 {
		opts.CleanupInterval = time.Minute
	}

	l := &Limiter{
		clients: make(map[string]*client),
		rate:    opts.Rate,
		burst:   opts.Burst,
		cleanup: opts.CleanupInterval,
		stopCh:  make(chan struct{}),
	}

	go l.cleanupLoop()
	return l
}

// Stop stops the cleanup goroutine.
func (l *Limiter) Stop() {
	l.stopOnce.Do(func() { close(l.stopCh) })
}

// Allow checks if a request from the given key is allowed.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	c, exists := l.clients[key]
	if !exists {
		c = &client{tokens: float64(l.burst)}
		l.clients[key] = c
	}

	// Add tokens based on time elapsed.
	elapsed := now.Sub(c.lastSeen).Seconds()
	c.tokens += elapsed * l.rate
	if c.tokens > float64(l.burst) {
		c.tokens = float64(l.burst)
	}
	c.lastSeen = now

	if c.tokens >= 1 {
		c.tokens--
		return true
	}
	return false
}

// cleanupLoop periodically removes idle clients.
func (l *Limiter) cleanupLoop() {
	ticker := time.NewTicker(l.cleanup)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			l.cleanupIdle()
		}
	}
}

func (l *Limiter) cleanupIdle() {
	l.mu.Lock()
	defer l.mu.Unlock()

	threshold := time.Now().Add(-l.cleanup)
	for key, c := range l.clients {
		if c.lastSeen.Before(threshold) {
			delete(l.clients, key)
		}
	}
}

// Middleware returns an HTTP middleware that rate limits requests.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := clientKey(r)
		if !l.Allow(key) {
			w.Header().Set("Retry-After", "1")
			// strconv.Itoa, not string(rune(burst)): the latter converts an int to
			// a code point, so a burst of 150 emitted the byte 0x96. The header
			// looked plausible in a test that only checked it was present.
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(l.burst))
			w.Header().Set("X-RateLimit-Remaining", "0")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientKey extracts a client identifier from the request.
func clientKey(r *http.Request) string {
	// Prefer X-Forwarded-For if present, as it identifies the real client
	// behind a proxy.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

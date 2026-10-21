package httpserver

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"gogo/internal/shared/netutil"
)

// Logger returns a middleware that logs every request with its request ID,
// status code, response size and duration. Requests taking longer than the
// slowThreshold are additionally logged at WARN so latency regressions are
// visible without noisy debug output. A zero threshold disables the slow
// path.
func Logger(logger *slog.Logger, slowThreshold time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			recorder := middleware.NewWrapResponseWriter(
				w,
				r.ProtoMajor,
			)

			next.ServeHTTP(recorder, r)

			attrs := []any{
				"request_id", middleware.GetReqID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.Status(),
				"bytes", recorder.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
			}

			if slowThreshold > 0 && time.Since(start) >= slowThreshold {
				logger.Warn("slow request", attrs...)
				return
			}
			logger.Info("http request", attrs...)
		})
	}
}

// RealIP resolves the client IP honoring the forwarding headers only from
// trusted proxies. trusted holds CIDR prefixes (e.g. "10.0.0.0/8",
// "192.168.1.0/24") or the literal "all" to trust every peer. IPs from any
// other source are ignored and the socket peer address wins, so a public
// internet client cannot spoof its way into logs or rate-limit buckets.
//
// The resolved address is written back into req.RemoteAddr and a canonical
// X-Real-IP header is set, matching how cloud load balancers forward the
// original client address.
//
// The trusted-proxy rules come from netutil.TrustedProxies, the same type
// config.Validate checks, so a CIDR the loader rejects can never be silently
// dropped here.
func RealIP(trusted netutil.TrustedProxies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if clientIP := resolveClientIP(r, trusted.Contains); clientIP != "" {
				r.RemoteAddr = net.JoinHostPort(clientIP, "0")
				r.Header.Set("X-Real-IP", clientIP)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// resolveClientIP walks the forwarding chain in X-Forwarded-For (rightmost
// hop first) and descends toward the client only while the current hop is a
// trusted proxy. The first untrusted address observed becomes the client;
// when no hop is trusted the socket peer is returned unchanged.
func resolveClientIP(r *http.Request, isTrusted func(netip.Addr) bool) string {
	current := parseIP(r.RemoteAddr)
	client := current

	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) == 0 {
		return current.String()
	}

	// Proxies append to the rightmost header, so only that one carries the
	// chain that led to this socket peer.
	hopList := forwarded[len(forwarded)-1]
	hops := strings.Split(hopList, ",")

	for i := len(hops) - 1; i >= 0; i-- {
		entry := strings.TrimSpace(hops[i])
		if entry == "" {
			continue
		}
		if !isTrusted(current) {
			break
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			break
		}
		client = addr
		current = addr
	}

	return client.String()
}

// parseIP extracts the host (IP) portion of a RemoteAddr like
// "1.2.3.4:5678". Unparseable input yields an invalid netip.Addr, which the
// trust check rejects.
func parseIP(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr
	}
	return netip.Addr{}
}

// SecurityHeaders sets the standard hardening response headers on every
// response. In production an HSTS header is additionally applied so browsers
// upgrade to HTTPS (safe because production TLS is terminated at the
// ingress).
func SecurityHeaders(production bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			h.Set("Content-Security-Policy", strings.Join([]string{
				"default-src 'self'",
				// htmx executes hx-on attribute handlers from the page's
				// inline scripts, so script-src is relaxed until those inline
				// uses are moved into real modules.
				"script-src 'self' 'unsafe-inline' 'unsafe-eval'",
				"style-src 'self' 'unsafe-inline'",
				// Editorial image fields accept safe HTTPS sources as well as
				// same-origin paths; <img> cannot execute scripts from SVG.
				"img-src 'self' data: https:",
				"connect-src 'self'",
				"object-src 'none'",
				"frame-ancestors 'none'",
				"base-uri 'self'",
				"form-action 'self'",
			}, "; "))
			if production {
				h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit rejects request bodies larger than maxBytes with 413 before the
// handler reads them. Zero disables the limit.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// maxRequestBody caps the GraphQL request body.
//
// 1 MiB is generous for a query document plus variables: an introspection query
// against a schema of this size is a few kilobytes. The cap exists so a body of
// arbitrary size cannot be buffered into memory by a single request.
const maxRequestBody = 1 << 20

// RequestBudget bounds how long one HTTP request may take.
//
// This is the outermost deadline in the service and it is what keeps the others
// honest. Every downstream call is bounded by the gRPC dependency timeout, but a
// query resolving twenty fields against five services has twenty calls and the
// sum of their individual budgets is not a bound on anything. Without a request
// budget a fan-out of slow-but-not-timing-out calls holds the request open until
// the client gives up, and the aggregator accumulates them.
//
// The deadline is attached to the request context, so it propagates to every
// downstream call without each resolver having to know about it.
func RequestBudget(timeout time.Duration, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if timeout <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))

			// Reported only on breach. A request that finished inside the budget
			// says nothing useful about its own health, and logging every one would
			// bury the ones that did.
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				log.Warn("request budget exhausted",
					"method", r.Method,
					"path", r.URL.Path,
					"timeout", timeout.String(),
				)
			}
		})
	}
}

// BodyLimit caps the request body before anything reads it.
//
// This runs before the GraphQL handler because gqlgen's default transport has no
// body cap, and a MaxBytesReader placed inside a resolver would already have
// buffered the body.
func BodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Logging records one structured line per request.
func Logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			// GraphQL answers 200 for most application errors, so the status alone
			// says little. The operation name and query length are logged too,
			// because "which query did this" is the first question asked of any
			// slow or failing request.
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"bytes", recorder.bytes,
				"duration_ms", time.Since(started(start)).Milliseconds(),
				"operation", r.Header.Get("X-GraphQL-Operation-Name"),
				"query_bytes", querySize(r),
			)
		})
	}
}

// started is a seam so the logging middleware reads its clock in one place.
func started(t time.Time) time.Time { return t }

// querySize reports the request body length when it is known and cheap to know.
func querySize(r *http.Request) int {
	if r.ContentLength < 0 || r.ContentLength > maxRequestBody {
		return 0
	}
	return int(r.ContentLength)
}

// Recover turns a panicking handler into a 500 instead of tearing down the
// process.
//
// The stack trace goes to the log, never to the client: it names internal
// packages, file paths, and function signatures, none of which a caller needs.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				err := recover()
				if err == nil {
					return
				}
				// net/http already recovers panics per connection, so this is belt
				// and braces for panics in middleware that runs outside its handler
				// goroutine's protection, and for the panic value itself being a
				// type that breaks the default handler's re-panic.
				log.Error("panic in handler",
					"error", err,
					"method", r.Method,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				writeJSON(w, http.StatusInternalServerError,
					errorResponse(http.StatusInternalServerError, "internal server error", "INTERNAL"))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets conservative response headers and enforces the origin
// policy.
//
// The origin check is here rather than in the GraphQL handler because a browser
// will happily send a cross-site request with a JSON content type only after a
// preflight, and this rejects the ones that arrive without it. A request with no
// Origin header is not a browser request and is allowed through.
func SecurityHeaders(allowed func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "no-referrer")

			if origin := r.Header.Get("Origin"); origin != "" && !allowed(origin) {
				writeJSON(w, http.StatusForbidden,
					errorResponse(http.StatusForbidden, "origin not allowed", "FORBIDDEN"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the response status and size for the logging middleware.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	bytes   int
	written bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.written {
		// A second WriteHeader is a handler bug; net/http would log it and ignore
		// it, and the status would silently be the first one.
		return
	}
	r.status = status
	r.written = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.written {
		// A handler that writes without setting a status implies 200.
		r.status = http.StatusOK
		r.written = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer, which the
// WebSocket and SSE transports need in order to hijack or flush.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Flush forwards to the underlying writer so streaming transports work through
// the logging middleware.
func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// decodeGraphQLRequest reads a GraphQL request envelope with a bounded body.
//
// Used by the GET transport check and by tests; the POST path goes through
// gqlgen, which has its own decoding. It exists so the size limit has one
// implementation rather than one per transport.
func decodeGraphQLRequest(body io.Reader) (query string, err error) {
	var payload struct {
		Query         string         `json:"query"`
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(io.LimitReader(body, maxRequestBody)).Decode(&payload); err != nil {
		return "", err
	}
	return payload.Query, nil
}

// isJSONRequest reports whether the request carries a JSON body, which the POST
// transport requires.
func isJSONRequest(r *http.Request) bool {
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		return false
	}
	mediaType, err := parseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

// parseMediaType splits a Content-Type header into its media type, ignoring
// parameters. A charset is the common one and does not change the answer.
func parseMediaType(contentType string) (string, error) {
	base, _, _ := strings.Cut(contentType, ";")
	return strings.ToLower(strings.TrimSpace(base)), nil
}

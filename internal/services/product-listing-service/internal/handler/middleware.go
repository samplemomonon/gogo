package handler

import (
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/httpx"
)

// statusRecorder captures the response status so the logging middleware can
// report it after the handler chain has run.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		// A handler that writes without setting a status implies 200.
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Logging returns middleware that records one structured line per request.
func Logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			// The request id is only here if RequestID is outside this
			// middleware; that is the whole reason for its position in the chain.
			// Without it the id in the X-Request-ID response header leads nowhere.
			log.Info("request",
				"request_id", GetRequestID(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"bytes", recorder.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// Recovery returns middleware that turns a panicking handler into a 500 instead
// of tearing down the process.
func Recovery(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					// The stack trace goes to the log, never to the client.
					// The request id is what makes the report actionable: it is
					// the same id the client was given, so the failing request can
					// be replayed and correlated.
					log.Error("panic in handler",
						"error", err,
						"request_id", GetRequestID(r.Context()),
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					httpx.JSON(w, http.StatusInternalServerError,
						httpx.ErrorResponse{Error: "internal server error"},
					)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders returns middleware that sets conservative response headers.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// testLogger returns a logger that discards output, so handler tests exercise the
// real middleware without writing to the test output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

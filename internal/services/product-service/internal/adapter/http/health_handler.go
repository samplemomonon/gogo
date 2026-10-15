// Package http provides HTTP handlers for health checks and middleware.
package http

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// HealthHandler handles health and readiness checks.
type HealthHandler struct {
	check func(ctx context.Context) error
	log   *slog.Logger
}

// NewHealthHandler creates a new health handler.
func NewHealthHandler(check func(ctx context.Context) error, log *slog.Logger) *HealthHandler {
	return &HealthHandler{check: check, log: log}
}

// RegisterRoutes mounts the health routes.
func (h *HealthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", h.Liveness)
	mux.HandleFunc("GET /healthz", h.Liveness)
	mux.HandleFunc("GET /readyz", h.Readiness)
	mux.HandleFunc("GET /version", h.Version)
}

// Liveness handles GET /health.
func (h *HealthHandler) Liveness(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readiness handles GET /readyz.
func (h *HealthHandler) Readiness(w http.ResponseWriter, r *http.Request) {
	if err := h.check(r.Context()); err != nil {
		h.log.Warn("readiness check failed", "error", err)
		jsonResponse(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Version handles GET /version.
func (h *HealthHandler) Version(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]string{
		"version": "dev",
	})
}

// Logging returns middleware that logs requests.
func Logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"bytes", recorder.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// Recovery returns middleware that recovers from panics.
func Recovery(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if err := recover(); err != nil {
					log.Error("panic in handler",
						"error", err,
						"method", r.Method,
						"path", r.URL.Path,
						"stack", string(debug.Stack()),
					)
					jsonResponse(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders returns middleware that sets security headers.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

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
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

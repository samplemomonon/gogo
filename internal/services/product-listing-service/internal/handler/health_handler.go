package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/httpx"
	"github.com/ruangbercerita182-tech/product-listing-service/internal/version"
)

// readinessProbe reports whether the service can serve traffic, which for this
// service means the database answers.
type readinessProbe func(ctx context.Context) error

// HealthHandler serves the liveness and readiness endpoints.
type HealthHandler struct {
	probe readinessProbe
	log   *slog.Logger
}

// NewHealthHandler returns a health handler whose readiness check calls probe.
func NewHealthHandler(probe readinessProbe, log *slog.Logger) *HealthHandler {
	return &HealthHandler{probe: probe, log: log}
}

// RegisterRoutes mounts the health endpoints on mux.
func (h *HealthHandler) RegisterRoutes(mux *http.ServeMux) {
	// Liveness: answers as long as the process is up. It must not depend on the
	// database, or a database outage would trigger a restart loop.
	mux.HandleFunc("GET /health", h.Livez)
	mux.HandleFunc("GET /healthz", h.Livez)
	// Readiness: answers whether dependencies are usable.
	mux.HandleFunc("GET /readyz", h.Readyz)
	mux.HandleFunc("GET /version", h.Version)
}

// Livez handles the liveness probe.
func (h *HealthHandler) Livez(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

// Readyz handles the readiness probe.
func (h *HealthHandler) Readyz(w http.ResponseWriter, r *http.Request) {
	// Bound the check so a hanging database cannot hang the probe endpoint.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	status := HealthStatus{
		Status:    "ready",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	if err := h.probe(ctx); err != nil {
		h.log.Warn("readiness probe failed", "error", err)
		status.Status = "unavailable"
		status.Error = err.Error()
		httpx.JSON(w, http.StatusServiceUnavailable, status)
		return
	}

	httpx.JSON(w, http.StatusOK, status)
}

// HealthStatus represents the detailed health status of the service.
type HealthStatus struct {
	Status    string            `json:"status"`
	Timestamp string            `json:"timestamp"`
	Error     string            `json:"error,omitempty"`
	Details   map[string]string `json:"details,omitempty"`
}

// Version handles the build information endpoint.
func (h *HealthHandler) Version(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{
		"version": version.Short(),
		"build":   version.String(),
	})
}

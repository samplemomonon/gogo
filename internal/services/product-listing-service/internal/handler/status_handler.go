package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ruangbercerita182-tech/product-listing-service/internal/httpx"
)

// StatusReporter describes how current the read model is.
type StatusReporter interface {
	// Products is how many products the projection holds.
	Products(ctx context.Context) (int64, error)
	// AppliedEvents is how many events have been projected.
	AppliedEvents(ctx context.Context) (int64, error)
}

// StatusHandler serves the projection status endpoint.
//
// The point of this endpoint is that a read service can be answering 200s with
// stale or empty data, and nothing about a health check would reveal it. An empty
// projection looks exactly like a healthy one from the outside, so the numbers
// that distinguish the two are exposed deliberately.
type StatusHandler struct {
	reporter StatusReporter
	log      *slog.Logger
}

// NewStatusHandler returns a handler backed by reporter.
func NewStatusHandler(reporter StatusReporter, log *slog.Logger) *StatusHandler {
	return &StatusHandler{reporter: reporter, log: log}
}

// RegisterRoutes mounts the status endpoint on mux.
func (h *StatusHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/listing/status", h.Status)
}

// StatusResponse is the body of GET /api/v1/listing/status.
type StatusResponse struct {
	// Products is how many products the read model currently holds.
	Products int64 `json:"products"`
	// AppliedEvents is how many events have been projected. A large gap against
	// the producer's event count means the consumer is behind.
	AppliedEvents int64 `json:"applied_events"`
	// Healthy is false when the read model could not be queried at all.
	Healthy bool `json:"healthy"`
}

// Status handles GET /api/v1/listing/status.
func (h *StatusHandler) Status(w http.ResponseWriter, r *http.Request) {
	products, err := h.reporter.Products(r.Context())
	if err != nil {
		h.log.Error("reading projection status failed", "error", err)
		httpx.JSON(w, http.StatusServiceUnavailable, StatusResponse{Healthy: false})
		return
	}

	events, err := h.reporter.AppliedEvents(r.Context())
	if err != nil {
		h.log.Error("reading projection event count failed", "error", err)
		httpx.JSON(w, http.StatusServiceUnavailable, StatusResponse{Healthy: false})
		return
	}

	httpx.JSON(w, http.StatusOK, StatusResponse{
		Products:      products,
		AppliedEvents: events,
		Healthy:       true,
	})
}

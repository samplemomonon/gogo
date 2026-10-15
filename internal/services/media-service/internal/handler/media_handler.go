// Package handler exposes the media API over HTTP. Handlers translate between
// the wire format and the service layer and contain no business rules.
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"media-service/internal/httpx"
	"media-service/internal/model"
)

// MediaService is the behaviour the handler needs from the service layer.
// Declaring it as an interface lets tests supply a stub.
type MediaService interface {
	Register(ctx context.Context, req *model.CreateMediaRequest) (*model.Media, *model.PresignedUpload, error)
	GetByID(ctx context.Context, id int64) (*model.Media, error)
	List(ctx context.Context, q *model.ListMediaQuery) (*model.ListMediaResult, error)
	GetUploadURL(ctx context.Context, id int64) (*model.PresignedUpload, error)
	CompleteUpload(ctx context.Context, id int64) (*model.Media, error)
	GetDownloadURL(ctx context.Context, id int64, ttl time.Duration) (*model.DownloadURL, error)
	Delete(ctx context.Context, id int64) error
}

// ListMediaResponse is the body of GET /api/v1/media. The totals travel with the
// page so a client can render pagination without a second request.
type ListMediaResponse struct {
	Media  []*model.Media `json:"media"`
	Total  int64          `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// RegisterMediaResponse is the body of POST /api/v1/media: the stored metadata
// plus where to upload the bytes.
type RegisterMediaResponse struct {
	Media  *model.Media           `json:"media"`
	Upload *model.PresignedUpload `json:"upload"`
}

// MediaHandler serves the media endpoints.
type MediaHandler struct {
	service MediaService
	log     *slog.Logger
}

// NewMediaHandler returns a handler backed by svc.
func NewMediaHandler(svc MediaService, log *slog.Logger) *MediaHandler {
	return &MediaHandler{service: svc, log: log}
}

// RegisterRoutes mounts the media routes on mux.
func (h *MediaHandler) RegisterRoutes(mux *http.ServeMux) {
	const base = "/api/v1/media"
	mux.HandleFunc("POST "+base, h.Register)
	mux.HandleFunc("GET "+base, h.List)
	mux.HandleFunc("GET "+base+"/{id}", h.GetByID)
	mux.HandleFunc("DELETE "+base+"/{id}", h.Delete)
	mux.HandleFunc("POST "+base+"/{id}/complete", h.CompleteUpload)
	mux.HandleFunc("GET "+base+"/{id}/upload-url", h.GetUploadURL)
	mux.HandleFunc("GET "+base+"/{id}/download-url", h.GetDownloadURL)
}

// Register handles POST /api/v1/media.
func (h *MediaHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req model.CreateMediaRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, "invalid request body: "+err.Error())
		return
	}

	media, upload, err := h.service.Register(r.Context(), &req)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	w.Header().Set("Location", "/api/v1/media/"+strconv.FormatInt(media.ID, 10))
	httpx.JSON(w, http.StatusCreated, RegisterMediaResponse{Media: media, Upload: upload})
}

// List handles GET /api/v1/media.
func (h *MediaHandler) List(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	status, err := model.ParseStatus(query.Get("status"))
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	limit, err := parseNonNegativeInt(query.Get("limit"), "limit")
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}
	offset, err := parseNonNegativeInt(query.Get("offset"), "offset")
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}

	req := &model.ListMediaQuery{
		OwnerID: query.Get("owner_id"),
		Limit:   limit,
		Offset:  offset,
	}
	// A pointer so an absent filter stays nil and the repository does not filter
	// on it, rather than matching the empty string.
	if status != "" {
		req.Status = &status
	}

	result, err := h.service.List(r.Context(), req)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ListMediaResponse{
		Media:  result.Media,
		Total:  result.Total,
		Limit:  req.Limit,
		Offset: req.Offset,
	})
}

// GetByID handles GET /api/v1/media/{id}.
func (h *MediaHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	media, err := h.service.GetByID(r.Context(), id)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, media)
}

// GetUploadURL handles GET /api/v1/media/{id}/upload-url.
func (h *MediaHandler) GetUploadURL(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	upload, err := h.service.GetUploadURL(r.Context(), id)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, upload)
}

// CompleteUpload handles POST /api/v1/media/{id}/complete.
func (h *MediaHandler) CompleteUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	media, err := h.service.CompleteUpload(r.Context(), id)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, media)
}

// GetDownloadURL handles GET /api/v1/media/{id}/download-url.
func (h *MediaHandler) GetDownloadURL(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	ttl, err := parseTTL(r.URL.Query().Get("presign_ttl_seconds"))
	if err != nil {
		httpx.BadRequest(w, err.Error())
		return
	}

	download, err := h.service.GetDownloadURL(r.Context(), id, ttl)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, download)
}

// Delete handles DELETE /api/v1/media/{id}.
func (h *MediaHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	if err := h.service.Delete(r.Context(), id); err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	// 204 carries no body, so there is nothing to encode.
	w.WriteHeader(http.StatusNoContent)
}

// pathID reads the {id} path value, answering 400 and returning false when it
// is not a positive integer.
func (h *MediaHandler) pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		httpx.BadRequest(w, "invalid media id: "+raw)
		return 0, false
	}
	return id, true
}

// parseNonNegativeInt reads an optional query parameter that must be a
// non-negative integer. An absent or empty value yields zero, which the service
// turns into its default.
func parseNonNegativeInt(raw, name string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, &queryError{message: name + " must be a non-negative integer, got " + strconv.Quote(raw)}
	}
	return value, nil
}

// queryError carries a 400 message out of a helper that cannot write to w.
type queryError struct{ message string }

func (e *queryError) Error() string { return e.message }

// parseTTL reads the optional download lifetime in seconds. An absent value
// yields zero, which means "use the configured default".
func parseTTL(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds < 0 {
		return 0, &queryError{message: "presign_ttl_seconds must be a non-negative integer, got " + strconv.Quote(raw)}
	}
	return time.Duration(seconds) * time.Second, nil
}

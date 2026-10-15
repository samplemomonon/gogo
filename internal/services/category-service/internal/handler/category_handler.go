// Package handler exposes the category API over HTTP. Handlers translate
// between the wire format and the service layer and contain no business rules.
package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"category-service/internal/httpx"
	"category-service/internal/model"
)

// CategoryService is the behaviour the handler needs from the service layer.
// Declaring it as an interface lets tests supply a stub.
type CategoryService interface {
	Create(ctx context.Context, req *model.CreateCategoryRequest) (*model.Category, error)
	GetByID(ctx context.Context, id int64) (*model.Category, error)
	GetAll(ctx context.Context) ([]*model.Category, error)
	GetTree(ctx context.Context) ([]*model.CategoryNode, error)
	GetChildren(ctx context.Context, parentID int64) ([]*model.Category, error)
	GetAncestors(ctx context.Context, id int64) ([]*model.Category, error)
	Update(ctx context.Context, id int64, req *model.UpdateCategoryRequest) (*model.Category, error)
	Delete(ctx context.Context, id int64) error
	DeletionImpact(ctx context.Context, id int64) (int, error)
}

// CategoryHandler serves the category endpoints.
type CategoryHandler struct {
	service CategoryService
	log     *slog.Logger
}

// NewCategoryHandler returns a handler backed by svc.
func NewCategoryHandler(svc CategoryService, log *slog.Logger) *CategoryHandler {
	return &CategoryHandler{service: svc, log: log}
}

// RegisterRoutes mounts the category routes on mux.
func (h *CategoryHandler) RegisterRoutes(mux *http.ServeMux) {
	const base = "/api/v1/categories"
	mux.HandleFunc("POST "+base, h.Create)
	mux.HandleFunc("GET "+base, h.GetAll)
	mux.HandleFunc("GET "+base+"/tree", h.GetTree)
	mux.HandleFunc("GET "+base+"/{id}", h.GetByID)
	mux.HandleFunc("PUT "+base+"/{id}", h.Update)
	mux.HandleFunc("DELETE "+base+"/{id}", h.Delete)
	mux.HandleFunc("GET "+base+"/{id}/children", h.GetChildren)
	mux.HandleFunc("GET "+base+"/{id}/ancestors", h.GetAncestors)
	mux.HandleFunc("GET "+base+"/{id}/impact", h.DeletionImpact)
}

// Create handles POST /api/v1/categories.
func (h *CategoryHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req model.CreateCategoryRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, "invalid request body: "+err.Error())
		return
	}

	category, err := h.service.Create(r.Context(), &req)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	w.Header().Set("Location", "/api/v1/categories/"+strconv.FormatInt(category.ID, 10))
	httpx.JSON(w, http.StatusCreated, category)
}

// GetAll handles GET /api/v1/categories.
func (h *CategoryHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	categories, err := h.service.GetAll(r.Context())
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, categories)
}

// GetTree handles GET /api/v1/categories/tree.
func (h *CategoryHandler) GetTree(w http.ResponseWriter, r *http.Request) {
	tree, err := h.service.GetTree(r.Context())
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, tree)
}

// GetByID handles GET /api/v1/categories/{id}.
func (h *CategoryHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	category, err := h.service.GetByID(r.Context(), id)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, category)
}

// Update handles PUT /api/v1/categories/{id}.
func (h *CategoryHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	var req model.UpdateCategoryRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.BadRequest(w, "invalid request body: "+err.Error())
		return
	}

	category, err := h.service.Update(r.Context(), id, &req)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, category)
}

// Delete handles DELETE /api/v1/categories/{id}.
func (h *CategoryHandler) Delete(w http.ResponseWriter, r *http.Request) {
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

// DeletionImpact handles GET /api/v1/categories/{id}/impact.
func (h *CategoryHandler) DeletionImpact(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	count, err := h.service.DeletionImpact(r.Context(), id)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		ID          int64 `json:"id"`
		Descendants int   `json:"descendants"`
	}{ID: id, Descendants: count})
}

// GetChildren handles GET /api/v1/categories/{id}/children.
func (h *CategoryHandler) GetChildren(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	children, err := h.service.GetChildren(r.Context(), id)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, children)
}

// GetAncestors handles GET /api/v1/categories/{id}/ancestors.
func (h *CategoryHandler) GetAncestors(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}

	ancestors, err := h.service.GetAncestors(r.Context(), id)
	if err != nil {
		httpx.Fail(w, h.log, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ancestors)
}

// pathID reads the {id} path value, answering 400 and returning false when it
// is not a positive integer.
func (h *CategoryHandler) pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		httpx.BadRequest(w, "invalid category id: "+raw)
		return 0, false
	}
	return id, true
}

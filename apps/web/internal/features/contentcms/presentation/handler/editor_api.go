package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
	"gogo/internal/features/contentcms/presentation/pages"
	"gogo/internal/shared/tenant"
)

const maxJSONBodyBytes = domain.MaxDocumentBytes + 64_000

type problemResponse struct {
	Code            string                   `json:"code"`
	Message         string                   `json:"message"`
	Errors          []domain.ValidationError `json:"errors,omitempty"`
	CurrentRevision int64                    `json:"currentRevision,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, code, message string, validation []domain.ValidationError, currentRevision int64) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemResponse{
		Code: code, Message: message, Errors: validation, CurrentRevision: currentRevision,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func readJSON(r *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxJSONBodyBytes+1))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("body contains trailing data")
	}
	return nil
}

type saveDraftRequest struct {
	Title            string          `json:"title"`
	Document         json.RawMessage `json:"document"`
	ExpectedRevision int64           `json:"expectedRevision"`
}

type publishRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

type restoreRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

type draftState struct {
	Revision    int64      `json:"revision"`
	Title       string     `json:"title"`
	Status      string     `json:"status"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	ETag        string     `json:"etag"`
}

type draftPayload struct {
	Page        pages.EditorPage         `json:"page"`
	Document    domain.Document          `json:"document"`
	ETag        string                   `json:"etag"`
	Diagnostics []domain.ValidationError `json:"diagnostics,omitempty"`
}

func expectedRevision(r *http.Request, bodyRevision int64) (int64, bool) {
	headerRevision := int64(0)
	headerPresent := false
	if raw := strings.TrimSpace(r.Header.Get("If-Match")); raw != "" && raw != "*" {
		headerPresent = true
		parsed, ok := parseRevisionETag(raw)
		if !ok {
			return 0, false
		}
		headerRevision = parsed
	}
	if headerPresent && bodyRevision > 0 && headerRevision != bodyRevision {
		return 0, false
	}
	if headerPresent {
		return headerRevision, true
	}
	if bodyRevision > 0 {
		return bodyRevision, true
	}
	return 0, false
}

func (h *PageHandler) getDraftJSON(w http.ResponseWriter, r *http.Request) {
	page, err := h.store.ReadDraft(r.Context(), tenant.FromContextOrDefault(r.Context()).String(), chi.URLParam(r, "slug"))
	if err != nil {
		h.writeAPIStoreError(w, r, "read content page draft", err)
		return
	}
	etag := revisionETag(page.Revision)
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, draftPayload{
		Page:        editorPage(page),
		Document:    page.Document,
		ETag:        etag,
		Diagnostics: domain.ValidationErrors(page.Document.Validate()),
	})
}

func (h *PageHandler) saveDraftJSON(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		writeProblem(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.", nil, 0)
		return
	}
	var request saveDraftRequest
	if err := readJSON(r, &request); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The request body is not valid JSON.", nil, 0)
		return
	}
	expected, ok := expectedRevision(r, request.ExpectedRevision)
	if !ok {
		writeProblem(w, http.StatusPreconditionRequired, "revision_required", "If-Match or expectedRevision is required.", nil, 0)
		return
	}
	if len(request.Document) == 0 {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "document is required.", nil, 0)
		return
	}
	document, err := decodeDocumentBytes(request.Document)
	if err != nil {
		h.writeAPIStoreError(w, r, "validate content page draft", err)
		return
	}
	tenantID := tenant.FromContextOrDefault(r.Context()).String()
	page, err := h.store.SaveDraft(r.Context(), tenantID, chi.URLParam(r, "slug"), strings.TrimSpace(request.Title), document, expected, editorActor(r))
	if err != nil {
		h.writeAPIStoreError(w, r, "save content page draft", err)
		return
	}
	etag := revisionETag(page.Revision)
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, draftState{
		Revision: page.Revision, Title: page.Title, Status: page.Status,
		UpdatedAt: page.UpdatedAt, PublishedAt: page.PublishedAt, ETag: etag,
	})
}

// saveDraftForm keeps the non-JavaScript form path working. It has no ETag:
// it reloads the current revision inside the store before writing.
func (h *PageHandler) saveDraftForm(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		http.Error(w, "Invalid request origin", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	document, err := decodeDocument(r.PostFormValue("document"))
	if err != nil {
		http.Error(w, "The page document is invalid", http.StatusUnprocessableEntity)
		return
	}
	expected, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("revision")), 10, 64)
	tenantID, slug := tenant.FromContextOrDefault(r.Context()).String(), chi.URLParam(r, "slug")
	_, err = h.store.SaveDraft(r.Context(), tenantID, slug, strings.TrimSpace(r.PostFormValue("title")), document, expected, editorActor(r))
	if err != nil {
		h.handleDraftError(w, r, "save content page draft", err)
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/admin/content/pages/"+slug, http.StatusSeeOther)
		return
	}
	h.writeEditorStatus(w, r, "Draft saved", "")
}

func (h *PageHandler) publishPage(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		if wantsJSON(r) {
			writeProblem(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.", nil, 0)
			return
		}
		http.Error(w, "Invalid request origin", http.StatusForbidden)
		return
	}
	tenantID, slug := tenant.FromContextOrDefault(r.Context()).String(), chi.URLParam(r, "slug")
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var request publishRequest
		if err := readJSON(r, &request); err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid_request", "The request body is not valid JSON.", nil, 0)
			return
		}
		expected, ok := expectedRevision(r, request.ExpectedRevision)
		if !ok {
			writeProblem(w, http.StatusPreconditionRequired, "revision_required", "If-Match or expectedRevision is required.", nil, 0)
			return
		}
		page, err := h.store.Publish(r.Context(), tenantID, slug, expected, editorActor(r))
		if err != nil {
			h.writeAPIStoreError(w, r, "publish content page", err)
			return
		}
		etag := revisionETag(page.Revision)
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, struct {
			draftState
			PublicURL string `json:"publicUrl"`
		}{
			draftState: draftState{Revision: page.Revision, Title: page.Title, Status: page.Status, UpdatedAt: page.UpdatedAt, PublishedAt: page.PublishedAt, ETag: etag},
			PublicURL:  "/pages/" + page.Slug,
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}
	document, err := decodeDocument(r.PostFormValue("document"))
	if err != nil {
		http.Error(w, "The page document is invalid", http.StatusUnprocessableEntity)
		return
	}
	expected, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("revision")), 10, 64)
	saved, err := h.store.SaveDraft(r.Context(), tenantID, slug, strings.TrimSpace(r.PostFormValue("title")), document, expected, editorActor(r))
	if err != nil {
		h.handleDraftError(w, r, "save content page before publishing", err)
		return
	}
	if _, err := h.store.Publish(r.Context(), tenantID, slug, saved.Revision, editorActor(r)); err != nil {
		h.handleDraftError(w, r, "publish content page", err)
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/admin/content/pages/"+slug, http.StatusSeeOther)
		return
	}
	h.writeEditorStatus(w, r, "Page published", "/pages/"+slug)
}

type revisionSummary struct {
	Revision  int64     `json:"revision"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	ActorID   string    `json:"actorId"`
	CreatedAt time.Time `json:"createdAt"`
}

func (h *PageHandler) listRevisions(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	revisions, err := h.store.ListRevisions(r.Context(), tenant.FromContextOrDefault(r.Context()).String(), chi.URLParam(r, "slug"), limit)
	if err != nil {
		h.writeAPIStoreError(w, r, "list content page revisions", err)
		return
	}
	items := make([]revisionSummary, 0, len(revisions))
	for _, revision := range revisions {
		items = append(items, revisionSummary{
			Revision: revision.Revision, Title: revision.Title, Status: revision.Status,
			ActorID: revision.ActorID, CreatedAt: revision.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Revisions []revisionSummary `json:"revisions"`
	}{Revisions: items})
}

func (h *PageHandler) restoreRevision(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		writeProblem(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.", nil, 0)
		return
	}
	revisionNumber, err := strconv.ParseInt(chi.URLParam(r, "revision"), 10, 64)
	if err != nil || revisionNumber <= 0 {
		writeProblem(w, http.StatusBadRequest, "invalid_revision", "Revision must be a positive number.", nil, 0)
		return
	}
	var request restoreRequest
	if r.ContentLength != 0 {
		if err := readJSON(r, &request); err != nil {
			writeProblem(w, http.StatusBadRequest, "invalid_request", "The request body is not valid JSON.", nil, 0)
			return
		}
	}
	expected, ok := expectedRevision(r, request.ExpectedRevision)
	if !ok {
		writeProblem(w, http.StatusPreconditionRequired, "revision_required", "If-Match or expectedRevision is required.", nil, 0)
		return
	}
	tenantID, slug := tenant.FromContextOrDefault(r.Context()).String(), chi.URLParam(r, "slug")
	revision, err := h.store.ReadRevision(r.Context(), tenantID, slug, revisionNumber)
	if err != nil {
		h.writeAPIStoreError(w, r, "read content page revision", err)
		return
	}
	page, err := h.store.SaveDraft(r.Context(), tenantID, slug, revision.Title, revision.Document, expected, editorActor(r))
	if err != nil {
		h.writeAPIStoreError(w, r, "restore content page revision", err)
		return
	}
	etag := revisionETag(page.Revision)
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusOK, draftPayload{
		Page: editorPage(page), Document: page.Document, ETag: etag,
		Diagnostics: domain.ValidationErrors(page.Document.Validate()),
	})
}

func (h *PageHandler) listSections(w http.ResponseWriter, r *http.Request) {
	sections, err := h.store.ListSections(r.Context(), tenant.FromContextOrDefault(r.Context()).String())
	if err != nil {
		h.writeAPIStoreError(w, r, "list content sections", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Sections []ports.ReusableSection `json:"sections"`
	}{Sections: sections})
}

type createSectionRequest struct {
	Name      string          `json:"name"`
	Component json.RawMessage `json:"component"`
}

func (h *PageHandler) createSection(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		writeProblem(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.", nil, 0)
		return
	}
	var request createSectionRequest
	if err := readJSON(r, &request); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_request", "The request body is not valid JSON.", nil, 0)
		return
	}
	component, err := decodeComponent(request.Component)
	if err != nil {
		h.writeAPIStoreError(w, r, "validate content section", err)
		return
	}
	section, err := h.store.CreateSection(
		r.Context(), tenant.FromContextOrDefault(r.Context()).String(), uuid.NewString(),
		strings.TrimSpace(request.Name), component, editorActor(r),
	)
	if err != nil {
		h.writeAPIStoreError(w, r, "create content section", err)
		return
	}
	writeJSON(w, http.StatusCreated, section)
}

func (h *PageHandler) deleteSection(w http.ResponseWriter, r *http.Request) {
	if !sameOriginRequest(r) {
		writeProblem(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.", nil, 0)
		return
	}
	err := h.store.DeleteSection(r.Context(), tenant.FromContextOrDefault(r.Context()).String(), chi.URLParam(r, "id"))
	if err != nil {
		h.writeAPIStoreError(w, r, "delete content section", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeComponent(raw json.RawMessage) (domain.Component, error) {
	if len(raw) == 0 {
		return domain.Component{}, fmt.Errorf("%w: component is required", domain.ErrInvalidDocument)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var component domain.Component
	if err := decoder.Decode(&component); err != nil {
		return domain.Component{}, fmt.Errorf("%w: decode component: %v", domain.ErrInvalidDocument, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return domain.Component{}, fmt.Errorf("%w: component contains trailing data", domain.ErrInvalidDocument)
	}
	document := domain.Document{SchemaVersion: domain.SchemaVersion, Components: []domain.Component{component}}
	if err := document.Validate(); err != nil {
		return domain.Component{}, err
	}
	return component, nil
}

func editorPage(page ports.Page) pages.EditorPage {
	return pages.EditorPage{
		Slug: page.Slug, Title: page.Title, Status: page.Status, Revision: page.Revision,
		UpdatedAt: page.UpdatedAt, PublishedAt: page.PublishedAt,
	}
}

func (h *PageHandler) writeAPIStoreError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, ports.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", "Page not found.", nil, 0)
	case errors.Is(err, ports.ErrConflict):
		current := int64(0)
		var conflict *ports.ConflictError
		if errors.As(err, &conflict) {
			current = conflict.Current
		}
		writeProblem(w, http.StatusConflict, "revision_conflict", "This page changed in another session. Reload or overwrite deliberately.", nil, current)
	case errors.Is(err, domain.ErrInvalidData):
		validation := domain.ValidationErrors(err)
		message := "The page data is invalid."
		if len(validation) == 0 {
			message = err.Error()
		}
		writeProblem(w, http.StatusUnprocessableEntity, "validation_failed", message, validation, 0)
	default:
		h.logger.Error(operation, "error", err)
		writeProblem(w, http.StatusServiceUnavailable, "unavailable", "Content pages are temporarily unavailable.", nil, 0)
	}
}

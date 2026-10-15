// Package httpx holds helpers shared by the HTTP handlers: JSON encoding and
// the mapping from domain errors to status codes.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"media-service/internal/apperror"
)

// maxRequestBody caps the JSON body we are willing to read, so a malicious or
// accidental huge payload cannot exhaust memory.
const maxRequestBody = 1 << 20 // 1 MiB

// ErrorResponse is the body returned for any failed request.
type ErrorResponse struct {
	Error string `json:"error"`
}

// JSON writes payload as an HTTP response with the given status.
func JSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line is already on the wire, so there is nothing to do but
		// record it; the connection will be closed by the server.
		slog.Error("write response", "error", err)
	}
}

// DecodeJSON reads a single JSON object from the request body, rejecting unknown
// fields and oversized bodies.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)

	decoder := json.NewDecoder(r.Body)
	// Unknown fields are almost always a client mistake worth surfacing.
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return err
	}
	// More() reports another value waiting in the stream, meaning the client
	// sent more than the single document we asked for.
	if decoder.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

// Fail maps err to a status code and writes the response. Client mistakes are
// reported verbatim; anything unexpected is logged in full and answered with a
// generic message so internal details never reach the caller.
func Fail(w http.ResponseWriter, log *slog.Logger, err error) {
	switch {
	case errors.Is(err, apperror.ErrNotFound):
		JSON(w, http.StatusNotFound, ErrorResponse{Error: "media not found"})

	case errors.Is(err, apperror.ErrInvalidInput):
		JSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})

	case errors.Is(err, apperror.ErrConflict), errors.Is(err, apperror.ErrNotReady):
		JSON(w, http.StatusConflict, ErrorResponse{Error: err.Error()})

	default:
		log.Error("unhandled request error", "error", err)
		JSON(w, http.StatusInternalServerError, ErrorResponse{Error: "internal server error"})
	}
}

// BadRequest writes a 400 with a fixed message.
func BadRequest(w http.ResponseWriter, message string) {
	JSON(w, http.StatusBadRequest, ErrorResponse{Error: message})
}

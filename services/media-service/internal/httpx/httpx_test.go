// Package httpx_test pins the HTTP edge contract: the JSON encoding shape, the
// strict request decoder, and — the part that matters most — the error table
// that decides which status code a domain failure becomes, and which failures
// are answered opaquely so internals never reach the caller.
package httpx_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"media-service/internal/apperror"
	"media-service/internal/httpx"
)

// quietLogger discards output; the failure-path tests assert on responses, not
// on log records.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorResponse {
	t.Helper()

	var body httpx.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body %q is not the documented error shape: %v", rec.Body.String(), err)
	}
	return body
}

// TestJSONWritesStatusContentTypeAndBody pins the three things a client relies
// on: the status it was given, the JSON content type, and an encoded body.
func TestJSONWritesStatusContentTypeAndBody(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	httpx.JSON(rec, http.StatusCreated, map[string]string{"id": "7"})

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"id":"7"}` {
		t.Errorf("body = %q, want %q", got, `{"id":"7"}`)
	}
}

// TestDecodeJSONAcceptsASingleObject is the happy path.
func TestDecodeJSONAcceptsASingleObject(t *testing.T) {
	t.Parallel()

	var dst struct {
		Filename string `json:"filename"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"filename":"photo.jpg"}`))
	rec := httptest.NewRecorder()

	if err := httpx.DecodeJSON(rec, req, &dst); err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}
	if dst.Filename != "photo.jpg" {
		t.Errorf("decoded Filename = %q, want %q", dst.Filename, "photo.jpg")
	}
}

// TestDecodeJSONRejectsAForeignField pins DisallowUnknownFields: a typo'd key
// is answered instead of being silently dropped. The destination is a struct
// because the strictness only exists relative to a known field set.
func TestDecodeJSONRejectsAForeignField(t *testing.T) {
	t.Parallel()

	var dst struct {
		Filename string `json:"filename"`
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"file":"photo.jpg"}`))
	rec := httptest.NewRecorder()

	err := httpx.DecodeJSON(rec, req, &dst)
	if err == nil {
		t.Fatal("DecodeJSON accepted an unknown field")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("error %q should name the unknown field", err)
	}
}

// TestDecodeJSONRejectsTrailingContent pins the single-document rule: two
// concatenated JSON objects would otherwise let the first be validated while
// the second is ignored.
func TestDecodeJSONRejectsTrailingContent(t *testing.T) {
	t.Parallel()

	var dst map[string]any
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":1}{"b":2}`))
	rec := httptest.NewRecorder()

	err := httpx.DecodeJSON(rec, req, &dst)
	if err == nil {
		t.Fatal("DecodeJSON accepted two concatenated documents")
	}
	if !strings.Contains(err.Error(), "single JSON object") {
		t.Errorf("error %q should name the single-document rule", err)
	}
}

// TestDecodeJSONRejectsAnOversizedBody pins the MaxBytesReader cap. Without
// it, a request body is an unbounded memory commitment.
func TestDecodeJSONRejectsAnOversizedBody(t *testing.T) {
	t.Parallel()

	// One byte over the 1 MiB cap. The payload is a valid JSON object, so the
	// only reason to refuse it is its size.
	body := append([]byte(`{"filename":"`), bytes.Repeat([]byte("a"), 1<<20)...)
	body = append(body, []byte(`"}`)...)

	var dst map[string]any
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	if err := httpx.DecodeJSON(rec, req, &dst); err == nil {
		t.Fatal("DecodeJSON accepted a body over the size cap")
	}
}

// TestFailMapsTheErrorTable is the status contract. Each row names the sentinel,
// the status a caller gets, and the message in the body.
func TestFailMapsTheErrorTable(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			// A missing row is reported with a fixed message, not the internal
			// one that names the table and id.
			name:       "not found",
			err:        fmt.Errorf("get media %d: %w", 7, apperror.ErrNotFound),
			wantStatus: http.StatusNotFound,
			wantBody:   "media not found",
		},
		{
			// Invalid input is a client mistake, so its message is reported
			// verbatim — the caller needs to know which field was wrong.
			name:       "invalid input",
			err:        fmt.Errorf("unsupported content type: %w", apperror.ErrInvalidInput),
			wantStatus: http.StatusBadRequest,
			wantBody:   "unsupported content type: invalid input",
		},
		{
			name:       "conflict",
			err:        fmt.Errorf("object key taken: %w", apperror.ErrConflict),
			wantStatus: http.StatusConflict,
			wantBody:   "object key taken: conflict",
		},
		{
			// Not-ready shares the conflict status: the request collided with
			// the record's current state, and retrying later may succeed.
			name:       "not ready",
			err:        fmt.Errorf("object not uploaded: %w", apperror.ErrNotReady),
			wantStatus: http.StatusConflict,
			wantBody:   "object not uploaded: media is not ready",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			httpx.Fail(rec, quietLogger(), tt.err)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := decodeBody(t, rec).Error; got != tt.wantBody {
				t.Errorf("body error = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

// TestFailTreatsStorageFailuresAsServerFaults pins the deliberate omission:
// ErrStorage is not in the status table, so it must fall through to the
// opaque 500. A storage outage is the server's problem, not the caller's, and
// distinguishing "object missing" from "store down" must not leak outward.
func TestFailTreatsStorageFailuresAsServerFaults(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	httpx.Fail(rec, quietLogger(), fmt.Errorf("put object: %w", apperror.ErrStorage))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := decodeBody(t, rec).Error; got != "internal server error" {
		t.Errorf("body error = %q, want the generic message", got)
	}
}

// TestFailHidesInternalErrors pins the default branch: an unclassified failure
// is logged in full and answered with one generic message. The internal detail
// must not appear in the body.
func TestFailHidesInternalErrors(t *testing.T) {
	t.Parallel()

	internal := errors.New("pq: relation \"media\" does not exist")
	rec := httptest.NewRecorder()
	httpx.Fail(rec, quietLogger(), internal)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "internal server error") {
		t.Errorf("body %q should carry the generic message", body)
	}
	if strings.Contains(body, "pq:") || strings.Contains(body, "relation") {
		t.Errorf("body %q leaked the internal error", body)
	}
}

// TestBadRequestWritesTheGivenMessage pins the validation-failure helper.
func TestBadRequestWritesTheGivenMessage(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	httpx.BadRequest(rec, "invalid request body: unexpected EOF")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if got := decodeBody(t, rec).Error; got != "invalid request body: unexpected EOF" {
		t.Errorf("body error = %q", got)
	}
}

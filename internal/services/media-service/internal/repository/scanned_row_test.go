package repository

import (
	"database/sql"
	"testing"
	"time"
)

// TestScanDestWritesIntoTheCallersRow guards a trap that is invisible at compile
// time: scanDest returns addresses of the struct's fields, so it must use a
// pointer receiver. With a value receiver the driver writes into a temporary copy
// and every column silently stays at its zero value.
//
// The symptom would be an API returning HTTP 200 with a body full of empty fields
// even though the row was stored correctly, so this is worth pinning down with a
// test rather than leaving to review.
func TestScanDestWritesIntoTheCallersRow(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)

	var row scannedMedia
	values := row.scanDest()

	etag := sql.NullString{String: "abc-123", Valid: true}
	assign(values, int64(9), "user-1", "photo.png", "image/png", int64(4096),
		etag, "media", "media/2026/09/x.png", "image", "ready", createdAt, updatedAt)

	got := row.media()
	if got.ID != 9 {
		t.Errorf("id = %d, want 9", got.ID)
	}
	if got.OwnerID != "user-1" {
		t.Errorf("owner id = %q, want user-1", got.OwnerID)
	}
	if got.Filename != "photo.png" {
		t.Errorf("filename = %q, want photo.png", got.Filename)
	}
	if got.ContentType != "image/png" {
		t.Errorf("content type = %q, want image/png", got.ContentType)
	}
	if got.SizeBytes != 4096 {
		t.Errorf("size = %d, want 4096", got.SizeBytes)
	}
	if got.ObjectKey != "media/2026/09/x.png" {
		t.Errorf("object key = %q, want media/2026/09/x.png", got.ObjectKey)
	}
	if got.Kind != "image" {
		t.Errorf("kind = %q, want image", got.Kind)
	}
	if got.Status != "ready" {
		t.Errorf("status = %q, want ready", got.Status)
	}
	if got.ETag == nil || *got.ETag != "abc-123" {
		t.Errorf("etag = %v, want a pointer to abc-123", got.ETag)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Errorf("created at = %s, want %s", got.CreatedAt, createdAt)
	}
}

func TestNullETagBecomesNilPointer(t *testing.T) {
	t.Parallel()

	var row scannedMedia
	values := row.scanDest()

	createdAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	assign(values, int64(9), "user-1", "photo.png", "image/png", int64(0),
		sql.NullString{}, "media", "media/2026/09/x.png", "image", "pending",
		createdAt, createdAt)

	got := row.media()
	// A NULL etag means the upload has not completed, which is not the same as an
	// empty string.
	if got.ETag != nil {
		t.Errorf("ETag = %q, want nil for a NULL column", *got.ETag)
	}
}

// assign writes values into the pointers in dest in order, standing in for the
// row scanner. A nil entry writes a NULL.
func assign(dest []any, values ...any) {
	for i, value := range values {
		if value == nil {
			continue
		}
		switch target := dest[i].(type) {
		case *int64:
			*target = value.(int64)
		case *string:
			*target = value.(string)
		case *sql.NullString:
			*target = value.(sql.NullString)
		case *sql.NullInt64:
			*target = value.(sql.NullInt64)
		case *time.Time:
			*target = value.(time.Time)
		default:
			panic("unhandled scan target")
		}
	}
}

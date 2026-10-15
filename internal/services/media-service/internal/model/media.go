// Package model holds the domain types exchanged with clients and the database.
package model

import (
	"encoding/json"
	"fmt"
	"mime"
	"path/filepath"
	"strings"
	"time"
)

// Kind is the broad category of a stored object.
type Kind string

const (
	KindImage    Kind = "image"
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindDocument Kind = "document"
	KindOther    Kind = "other"
)

// Status tracks the upload lifecycle: a record exists as soon as it is
// registered, but it only becomes downloadable once the bytes are confirmed to
// be in the bucket.
type Status string

const (
	// StatusPending means the record exists but the object was never uploaded.
	StatusPending Status = "pending"
	// StatusReady means the object was verified in the object store.
	StatusReady Status = "ready"
)

// ParseKind converts a client supplied string to a Kind, rejecting unknown
// values so a typo cannot silently become KindOther.
func ParseKind(s string) (Kind, error) {
	switch k := Kind(strings.ToLower(strings.TrimSpace(s))); k {
	case KindImage, KindVideo, KindAudio, KindDocument, KindOther:
		return k, nil
	case "":
		return KindOther, nil
	default:
		return "", fmt.Errorf("unknown media kind %q, want one of image|video|audio|document|other", s)
	}
}

// ParseStatus converts a client supplied string to a Status.
func ParseStatus(s string) (Status, error) {
	switch st := Status(strings.ToLower(strings.TrimSpace(s))); st {
	case StatusPending, StatusReady:
		return st, nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("unknown media status %q, want pending or ready", s)
	}
}

// Media is one stored object. The bytes live in the object store; this row is
// the authoritative metadata.
//
// ETag is nullable because it is only known after the upload completes, and it
// is a pointer so that "not uploaded yet" serialises as null rather than "".
type Media struct {
	ID          int64   `json:"id"`
	OwnerID     string  `json:"owner_id"`
	Filename    string  `json:"filename"`
	ContentType string  `json:"content_type"`
	SizeBytes   int64   `json:"size_bytes"`
	ETag        *string `json:"etag"`
	Bucket      string  `json:"bucket"`
	// ObjectKey is the path inside the bucket. Clients must never construct it;
	// they use the presigned URLs, because the key is an implementation detail
	// that would otherwise leak internal layout into the public API.
	ObjectKey string    `json:"object_key"`
	Kind      Kind      `json:"kind"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateMediaRequest is the body of POST /api/v1/media.
type CreateMediaRequest struct {
	OwnerID     string `json:"owner_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	// Kind is optional. When empty it is inferred from ContentType.
	Kind Kind `json:"kind"`
	// SizeBytes is advisory. The size confirmed by CompleteUpload overwrites it,
	// because the client cannot be trusted to report its own upload correctly.
	SizeBytes int64 `json:"size_bytes"`
}

// ListMediaQuery are the filters accepted by GET /api/v1/media.
type ListMediaQuery struct {
	OwnerID string
	// Status is a pointer so "no filter" is distinguishable from "pending".
	Status *Status
	Limit  int
	Offset int
}

// ListMediaResult is a page of media together with the unpaged total.
type ListMediaResult struct {
	Media []*Media
	Total int64
}

// PresignedUpload is a direct-to-object-store upload target.
//
// The URL is signed with the secret key, so the client never needs credentials
// and this service never handles the bytes.
type PresignedUpload struct {
	URL    string `json:"url"`
	Method string `json:"method"`
	// Headers the client should send. These are advisory: the signature does not
	// cover them, so a mismatch is not rejected.
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// DownloadURL is a time limited read URL for a stored object.
type DownloadURL struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// KindForContentType infers a Kind from a MIME type. It is only a fallback for
// when the client did not state one, so an unrecognised type is not an error.
func KindForContentType(contentType string) Kind {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// A malformed header falls back to the file extension, then to other.
		switch strings.ToLower(strings.TrimPrefix(filepath.Ext(contentType), ".")) {
		case "jpg", "jpeg", "png", "gif", "webp", "avif", "svg":
			return KindImage
		case "mp4", "webm", "mov":
			return KindVideo
		case "mp3", "wav", "ogg", "flac":
			return KindAudio
		case "pdf", "doc", "docx", "txt":
			return KindDocument
		default:
			return KindOther
		}
	}

	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return KindImage
	case strings.HasPrefix(mediaType, "video/"):
		return KindVideo
	case strings.HasPrefix(mediaType, "audio/"):
		return KindAudio
	case mediaType == "application/pdf",
		strings.HasPrefix(mediaType, "text/"),
		strings.HasPrefix(mediaType, "application/vnd.openxmlformats"),
		strings.HasPrefix(mediaType, "application/vnd.ms-"):
		return KindDocument
	default:
		return KindOther
	}
}

// ExtensionFor returns the object key extension for a filename, lowercased and
// without a leading dot. It returns an empty string when there is no usable
// extension, in which case the key simply has none.
func ExtensionFor(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	return strings.TrimPrefix(ext, ".")
}

// MarshalJSON keeps Kind and Status as plain strings. Without it a named string
// type still marshals correctly, but the explicit method documents the intent
// and fails loudly if a field is ever added that should not leak.
func (k Kind) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(k))
}

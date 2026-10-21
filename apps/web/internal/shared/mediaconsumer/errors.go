package mediaconsumer

import (
	"errors"

	"gogo/internal/shared/grpcconsumer"
)

// ErrMediaNotFound reports that the media record — or its object — does not
// exist. It is the sentinel a surface branches on to render "no image" rather
// than treating a missing object as a broken reference.
var ErrMediaNotFound = errors.New("media not found")

// ErrMediaInvalid reports a rejection of the request itself: a bad owner id, an
// unsupported filename, or a content type media-service will not store.
var ErrMediaInvalid = errors.New("invalid media")

// ErrMediaConflict reports a rejection because the write would collide with an
// existing row.
var ErrMediaConflict = errors.New("media conflict")

// ErrMediaUnavailable is the outage sentinel: the media-service upstream (and
// therefore MinIO) cannot be reached. It is the one sentinel the convention
// guarantees, and it is what lets a storefront degrade (render no image) instead
// of failing as an unknown error.
var ErrMediaUnavailable = errors.New("media service unavailable")

// vocabulary is media-service's error vocabulary: the sentinels a surface can
// branch on without importing media-service to learn its semantics.
var vocabulary = grpcconsumer.Vocabulary{
	NotFound:     ErrMediaNotFound,
	Invalid:      ErrMediaInvalid,
	Conflict:     ErrMediaConflict,
	Precondition: ErrMediaInvalid,
	Unavailable:  ErrMediaUnavailable,
}

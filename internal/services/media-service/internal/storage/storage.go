// Package storage wraps the S3-compatible object store. Everything above this
// package works with the Store interface, so the MinIO SDK is a detail and the
// service can be tested against a stub.
package storage

import (
	"context"
	"time"

	"media-service/internal/model"
)

// ObjectInfo is what the store knows about an object that has been uploaded.
type ObjectInfo struct {
	Size int64
	// ETag is the store's version marker. Multipart uploads produce a composite
	// value with a dash in it, so it is a cache-busting token, not a checksum.
	ETag string
	// ContentType is what the client actually sent, which may differ from what
	// was declared at registration time.
	ContentType string
}

// Store is the object storage behaviour the service depends on.
type Store interface {
	// EnsureBucket creates the configured bucket when it does not exist yet, so
	// a fresh volume needs no manual setup step.
	EnsureBucket(ctx context.Context) error

	// PresignUpload returns a direct-to-store URL the client can PUT to.
	PresignUpload(ctx context.Context, key, contentType string, ttl time.Duration) (*model.PresignedUpload, error)

	// PresignDownload returns a time limited GET URL. filename, when not empty,
	// is offered to the client as the download name.
	PresignDownload(ctx context.Context, key, filename string, ttl time.Duration) (*model.DownloadURL, error)

	// Stat reports the stored object, or ErrObjectNotFound if it is absent.
	Stat(ctx context.Context, key string) (*ObjectInfo, error)

	// Delete removes the object. Deleting an object that is already gone is not
	// an error, so the operation is safe to retry.
	Delete(ctx context.Context, key string) error

	// Ping verifies the store is reachable, for the readiness probe.
	Ping(ctx context.Context) error

	// Bucket returns the bucket every object lives in.
	Bucket() string
}

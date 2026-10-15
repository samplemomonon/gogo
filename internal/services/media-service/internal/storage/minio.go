package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"media-service/internal/model"
)

// ErrObjectNotFound reports that the key does not exist in the bucket. It is
// separate from other store failures so the caller can tell "the client never
// finished uploading" from "the object store is broken".
var ErrObjectNotFound = errors.New("object not found in bucket")

// S3Store is the MinIO implementation of Store. MinIO speaks the S3 API, and so
// do most other object stores, so this works unchanged against any of them.
type S3Store struct {
	client *minio.Client
	bucket string
	region string
}

// Options configures NewS3Store.
type Options struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// NewS3Store builds a client. It does not contact the store, so a temporarily
// unreachable endpoint surfaces at EnsureBucket or Ping instead of here.
func NewS3Store(opts Options) (*S3Store, error) {
	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
		Secure: opts.UseSSL,
		Region: opts.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("new object store client: %w", err)
	}
	return &S3Store{client: client, bucket: opts.Bucket, region: opts.Region}, nil
}

// Bucket returns the bucket every object lives in.
func (s *S3Store) Bucket() string { return s.bucket }

// EnsureBucket creates the bucket if it is missing.
func (s *S3Store) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %q: %w", s.bucket, err)
	}
	if exists {
		return nil
	}

	// Region is set explicitly so the bucket lands in the same place on stores
	// other than MinIO, where it is not ignored.
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
		// Another replica can win the race and create the bucket first, which is
		// a success from this service's point of view.
		if isAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create bucket %q: %w", s.bucket, err)
	}
	return nil
}

// PresignUpload returns a PUT URL the client can upload to directly. The bytes
// never pass through this service.
func (s *S3Store) PresignUpload(
	ctx context.Context,
	key, contentType string,
	ttl time.Duration,
) (*model.PresignedUpload, error) {
	raw, err := s.client.PresignedPutObject(ctx, s.bucket, key, ttl)
	if err != nil {
		return nil, fmt.Errorf("presign upload for %q: %w", key, err)
	}

	// The signature does not cover request headers, so these are hints for the
	// client rather than values the store will verify.
	headers := map[string]string{}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}

	return &model.PresignedUpload{
		URL:       raw.String(),
		Method:    http.MethodPut,
		Headers:   headers,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

// PresignDownload returns a GET URL. When filename is set the store is asked to
// offer it as the download name.
func (s *S3Store) PresignDownload(
	ctx context.Context,
	key, filename string,
	ttl time.Duration,
) (*model.DownloadURL, error) {
	var params url.Values
	if filename != "" {
		params = url.Values{}
		// Quoted so spaces in the name cannot break the header, and escaped so
		// a quote in the filename cannot inject a second directive.
		params.Set(
			"response-content-disposition",
			fmt.Sprintf("attachment; filename=%q", filename),
		)
	}

	raw, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, params)
	if err != nil {
		return nil, fmt.Errorf("presign download for %q: %w", key, err)
	}

	return &model.DownloadURL{
		URL:       raw.String(),
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

// Stat reports the stored object.
func (s *S3Store) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s", ErrObjectNotFound, s.bucket, key)
		}
		return nil, fmt.Errorf("stat %q: %w", key, err)
	}
	return &ObjectInfo{
		Size:        info.Size,
		ETag:        info.ETag,
		ContentType: info.ContentType,
	}, nil
}

// Delete removes the object, tolerating one that is already gone.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete %q: %w", key, err)
	}
	return nil
}

// Ping verifies the store answers. BucketExists is used rather than the live
// endpoint because it works over plain http, which is what local development and
// the compose stack run.
func (s *S3Store) Ping(ctx context.Context) error {
	if _, err := s.client.BucketExists(ctx, s.bucket); err != nil {
		return fmt.Errorf("ping object store: %w", err)
	}
	return nil
}

// isNotFound reports whether the store answered that the key does not exist.
func isNotFound(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.StatusCode == http.StatusNotFound ||
			resp.Code == "NoSuchKey" ||
			resp.Code == "NoSuchBucket"
	}
	return false
}

// isAlreadyExists reports whether a create failed because the name is taken.
func isAlreadyExists(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "BucketAlreadyOwnedByYou" ||
			resp.Code == "BucketAlreadyExists"
	}
	return false
}

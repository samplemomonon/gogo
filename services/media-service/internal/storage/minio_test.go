// Package storage_test pins the S3 adapter's translation layer: how store
// failures become ErrObjectNotFound, what a successful Stat reports, and the
// shapes of the presigned URLs clients are handed. The tests run against a
// stub HTTP server speaking just enough S3 XML, so no MinIO process is
// required.
package storage_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"media-service/internal/storage"
)

// stubS3 serves the handful of S3 responses these tests exercise and records
// the requests it saw, so a test can assert on method and path.
func stubS3(t *testing.T, handle http.HandlerFunc) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(handle)
	t.Cleanup(server.Close)
	return server
}

// newStore builds a store pointed at the stub server. The endpoint is
// host:port without a scheme, which is the shape minio.New requires.
func newStore(t *testing.T, server *httptest.Server, bucket string) *storage.S3Store {
	t.Helper()

	store, err := storage.NewS3Store(storage.Options{
		Endpoint:  strings.TrimPrefix(server.URL, "http://"),
		AccessKey: "test-access",
		SecretKey: "test-secret",
		Bucket:    bucket,
		Region:    "us-east-1",
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	return store
}

// TestNewS3StoreRejectsAFullyQualifiedEndpoint pins the endpoint contract: it
// is host:port, not a URL. Accepting a scheme here would produce a client that
// fails on every call with a confusing parse error at request time.
func TestNewS3StoreRejectsAFullyQualifiedEndpoint(t *testing.T) {
	t.Parallel()

	_, err := storage.NewS3Store(storage.Options{
		Endpoint:  "https://minio:9000",
		AccessKey: "k",
		SecretKey: "s",
		Bucket:    "media",
		Region:    "us-east-1",
	})
	if err == nil {
		t.Fatal("NewS3Store accepted a fully qualified URL")
	}
	if !strings.Contains(err.Error(), "new object store client") {
		t.Errorf("error %q should name the client construction step", err)
	}
}

// TestNewS3StoreDoesNotContactTheStore pins the documented laziness: building
// the client must succeed even when the store is unreachable, because the
// unreachable case belongs to EnsureBucket or Ping, where the message is
// actionable.
func TestNewS3StoreDoesNotContactTheStore(t *testing.T) {
	t.Parallel()

	store, err := storage.NewS3Store(storage.Options{
		Endpoint:  "127.0.0.1:9",
		AccessKey: "k",
		SecretKey: "s",
		Bucket:    "media",
		Region:    "us-east-1",
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	if got := store.Bucket(); got != "media" {
		t.Errorf("Bucket() = %q, want media", got)
	}
}

// TestPresignUpload pins the upload target: a PUT URL into the configured
// bucket, the content type as an advisory header, and an expiry consistent
// with the requested TTL.
func TestPresignUpload(t *testing.T) {
	t.Parallel()

	server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("PresignUpload contacted the store: %s %s", r.Method, r.URL)
	})
	store := newStore(t, server, "media")

	before := time.Now()
	upload, err := store.PresignUpload(context.Background(), "owner-1/photo.png", "image/png", 15*time.Minute)
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}

	if upload.Method != http.MethodPut {
		t.Errorf("Method = %q, want PUT", upload.Method)
	}
	parsed, err := url.Parse(upload.URL)
	if err != nil {
		t.Fatalf("upload URL %q does not parse: %v", upload.URL, err)
	}
	if !strings.HasPrefix(parsed.Path, "/media/owner-1/photo.png") {
		t.Errorf("URL path = %q, want it under /media/owner-1/photo.png", parsed.Path)
	}
	if got := parsed.Query().Get("X-Amz-Expires"); got != "900" {
		t.Errorf("X-Amz-Expires = %q, want 900", got)
	}
	if got := upload.Headers["Content-Type"]; got != "image/png" {
		t.Errorf("Content-Type header = %q, want image/png", got)
	}
	if !upload.ExpiresAt.After(before) {
		t.Errorf("ExpiresAt %v should be after the request time", upload.ExpiresAt)
	}
}

// TestPresignUploadOmitsAnEmptyContentType pins that an unknown content type
// produces no header rather than an empty one, which a client might send
// literally.
func TestPresignUploadOmitsAnEmptyContentType(t *testing.T) {
	t.Parallel()

	server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {})
	store := newStore(t, server, "media")

	upload, err := store.PresignUpload(context.Background(), "key", "", time.Minute)
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	if _, ok := upload.Headers["Content-Type"]; ok {
		t.Errorf("Headers = %v, want no Content-Type entry", upload.Headers)
	}
}

// TestPresignDownloadNamesTheFile pins the download-name mechanism: the
// filename travels as a signed response-content-disposition parameter, quoted
// so spaces survive and escaped so a quote cannot inject a second directive.
func TestPresignDownloadNamesTheFile(t *testing.T) {
	t.Parallel()

	server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {})
	store := newStore(t, server, "media")

	download, err := store.PresignDownload(context.Background(), "key", `laporan "Q1".pdf`, time.Hour)
	if err != nil {
		t.Fatalf("PresignDownload: %v", err)
	}

	parsed, err := url.Parse(download.URL)
	if err != nil {
		t.Fatalf("download URL %q does not parse: %v", download.URL, err)
	}
	want := `attachment; filename="laporan \"Q1\".pdf"`
	if got := parsed.Query().Get("response-content-disposition"); got != want {
		t.Errorf("response-content-disposition = %q, want %q", got, want)
	}
}

// TestPresignDownloadWithoutAFilename pins the plain URL case: no disposition
// parameter at all rather than an empty one.
func TestPresignDownloadWithoutAFilename(t *testing.T) {
	t.Parallel()

	server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {})
	store := newStore(t, server, "media")

	download, err := store.PresignDownload(context.Background(), "key", "", time.Hour)
	if err != nil {
		t.Fatalf("PresignDownload: %v", err)
	}

	parsed, err := url.Parse(download.URL)
	if err != nil {
		t.Fatalf("download URL %q does not parse: %v", download.URL, err)
	}
	if _, ok := parsed.Query()["response-content-disposition"]; ok {
		t.Errorf("URL %q should carry no disposition parameter", download.URL)
	}
}

// TestStatReportsTheStoredObject pins the happy path mapping from S3 response
// headers to ObjectInfo.
func TestStatReportsTheStoredObject(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath string
	server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("ETag", `"e-tag-value"`)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "1234")
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	})
	store := newStore(t, server, "media")

	info, err := store.Stat(context.Background(), "owner-1/photo.png")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if gotMethod != http.MethodHead {
		t.Errorf("Stat used %s, want HEAD", gotMethod)
	}
	if gotPath != "/media/owner-1/photo.png" {
		t.Errorf("Stat path = %q, want /media/owner-1/photo.png", gotPath)
	}
	if info.Size != 1234 {
		t.Errorf("Size = %d, want 1234", info.Size)
	}
	if info.ETag != "e-tag-value" {
		t.Errorf("ETag = %q, want e-tag-value", info.ETag)
	}
	if info.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", info.ContentType)
	}
}

// TestStatTranslatesNotFoundToErrObjectNotFound is the classification this
// adapter exists for: a missing key must be distinguishable from a broken
// store, because the service answers the first with "upload never finished"
// and the second with a server fault.
func TestStatTranslatesNotFoundToErrObjectNotFound(t *testing.T) {
	t.Parallel()

	server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`)
	})
	store := newStore(t, server, "media")

	_, err := store.Stat(context.Background(), "missing")
	if err == nil {
		t.Fatal("Stat succeeded for a missing object")
	}
	if !errors.Is(err, storage.ErrObjectNotFound) {
		t.Errorf("error %q should wrap ErrObjectNotFound", err)
	}
	if !strings.Contains(err.Error(), "media/missing") {
		t.Errorf("error %q should name the bucket and key", err)
	}
}

// TestDeleteToleratesAMissingObject pins the idempotency claim in the Store
// interface: deleting what is already gone is success, so a retried cleanup
// can proceed.
func TestDeleteToleratesAMissingObject(t *testing.T) {
	t.Parallel()

	server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`)
	})
	store := newStore(t, server, "media")

	if err := store.Delete(context.Background(), "missing"); err != nil {
		t.Errorf("Delete of a missing object returned %v, want nil", err)
	}
}

// TestEnsureBucketCreatesOnlyWhenMissing pins both branches: an existing
// bucket is left alone, and a missing one is created in the configured region.
func TestEnsureBucketCreatesOnlyWhenMissing(t *testing.T) {
	t.Parallel()

	t.Run("exists", func(t *testing.T) {
		t.Parallel()

		var puts int
		server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodHead:
				w.WriteHeader(http.StatusOK)
			case http.MethodPut:
				puts++
				w.WriteHeader(http.StatusOK)
			}
		})
		store := newStore(t, server, "media")

		if err := store.EnsureBucket(context.Background()); err != nil {
			t.Fatalf("EnsureBucket: %v", err)
		}
		if puts != 0 {
			t.Errorf("EnsureBucket issued %d PUTs for an existing bucket, want 0", puts)
		}
	})

	t.Run("missing", func(t *testing.T) {
		t.Parallel()

		var putPath string
		server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodHead:
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `<Error><Code>NoSuchBucket</Code><Message>no bucket</Message></Error>`)
			case http.MethodPut:
				putPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
			}
		})
		store := newStore(t, server, "media")

		if err := store.EnsureBucket(context.Background()); err != nil {
			t.Fatalf("EnsureBucket: %v", err)
		}
		// The SDK appends a trailing slash to the bucket path.
		if strings.TrimSuffix(putPath, "/") != "/media" {
			t.Errorf("MakeBucket path = %q, want /media", putPath)
		}
	})

	t.Run("lost the creation race", func(t *testing.T) {
		t.Parallel()

		// Another replica creates the bucket between this replica's HEAD and
		// PUT. From this service's point of view that is a success.
		server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			switch r.Method {
			case http.MethodHead:
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `<Error><Code>NoSuchBucket</Code><Message>no bucket</Message></Error>`)
			case http.MethodPut:
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, `<Error><Code>BucketAlreadyOwnedByYou</Code><Message>taken</Message></Error>`)
			}
		})
		store := newStore(t, server, "media")

		if err := store.EnsureBucket(context.Background()); err != nil {
			t.Errorf("EnsureBucket returned %v for a bucket another replica created, want nil", err)
		}
	})
}

// TestPingReportsAnUnreachableStore pins the readiness-probe contract: Ping
// errors when the store cannot answer, and names the step for the log.
func TestPingReportsAnUnreachableStore(t *testing.T) {
	t.Parallel()

	server := stubS3(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `<Error><Code>InternalError</Code><Message>broken</Message></Error>`)
	})
	store := newStore(t, server, "media")

	err := store.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping succeeded against a broken store")
	}
	if !strings.Contains(err.Error(), "ping object store") {
		t.Errorf("error %q should name the ping step", err)
	}
}

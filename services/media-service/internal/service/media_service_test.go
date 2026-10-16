package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"media-service/internal/apperror"
	"media-service/internal/model"
	"media-service/internal/storage"
)

// stubRepo lets service tests exercise the rules without a database.
type stubRepo struct {
	rows      map[int64]*model.Media
	nextID    int64
	created   *model.Media
	list      *model.ListMediaResult
	ready     *model.Media
	updated   *model.Media
	err       error
	createErr error

	gotCreate   *model.Media
	gotQuery    *model.ListMediaQuery
	gotMarkID   int64
	gotMarkSize int64
	gotMarkETag string
	gotDeleteID int64
	deleted     bool
}

// rows is the store the stubRepo answers reads from. Tests populate it directly
// when they need specific rows, and Create adds to it, which is what makes the
// key-uniqueness test meaningful.
func (s *stubRepo) Create(_ context.Context, rec *model.Media) (*model.Media, error) {
	s.gotCreate = rec
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.nextID++
	stored := *rec
	stored.ID = s.nextID
	// The row is copied, so a later test mutation cannot reach back into it.
	if s.rows == nil {
		s.rows = make(map[int64]*model.Media)
	}
	s.rows[stored.ID] = &stored
	s.created = &stored
	return &stored, nil
}

func (s *stubRepo) GetByID(_ context.Context, id int64) (*model.Media, error) {
	if s.err != nil {
		return nil, s.err
	}
	if row, ok := s.rows[id]; ok {
		return row, nil
	}
	return nil, fmt.Errorf("%w: media %d", apperror.ErrNotFound, id)
}

func (s *stubRepo) List(_ context.Context, q *model.ListMediaQuery) (*model.ListMediaResult, error) {
	s.gotQuery = q
	if s.err != nil {
		return nil, s.err
	}
	if s.list == nil {
		return &model.ListMediaResult{Media: []*model.Media{}}, nil
	}
	return s.list, nil
}

func (s *stubRepo) MarkReady(_ context.Context, id, size int64, etag string) (*model.Media, error) {
	s.gotMarkID, s.gotMarkSize, s.gotMarkETag = id, size, etag
	if s.ready != nil {
		return s.ready, nil
	}
	return &model.Media{ID: id, SizeBytes: size, ETag: &etag, Status: model.StatusReady}, nil
}

func (s *stubRepo) Delete(_ context.Context, id int64) error {
	s.gotDeleteID, s.deleted = id, true
	return s.err
}

// stubStore lets service tests drive presigning and stat results.
type stubStore struct {
	bucket     string
	info       *storage.ObjectInfo
	statErr    error
	presignErr error

	uploadTTL   time.Duration
	downloadTTL time.Duration
	deleteErr   error
	deletedKey  string
}

func (s *stubStore) EnsureBucket(context.Context) error { return nil }
func (s *stubStore) Bucket() string                     { return s.bucket }

func (s *stubStore) PresignUpload(
	_ context.Context,
	key, _ string,
	ttl time.Duration,
) (*model.PresignedUpload, error) {
	if s.presignErr != nil {
		return nil, s.presignErr
	}
	s.uploadTTL = ttl
	return &model.PresignedUpload{
		URL: "https://store.example/" + key, Method: "PUT",
		Headers:   map[string]string{"Content-Type": "image/png"},
		ExpiresAt: time.Now().Add(ttl),
	}, nil
}

func (s *stubStore) PresignDownload(
	_ context.Context,
	key, _ string,
	ttl time.Duration,
) (*model.DownloadURL, error) {
	if s.presignErr != nil {
		return nil, s.presignErr
	}
	s.downloadTTL = ttl
	return &model.DownloadURL{URL: "https://store.example/" + key, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (s *stubStore) Stat(_ context.Context, key string) (*storage.ObjectInfo, error) {
	if s.statErr != nil {
		return nil, s.statErr
	}
	if s.info == nil {
		return nil, errors.Join(storage.ErrObjectNotFound, errors.New("no object"))
	}
	return s.info, nil
}

func (s *stubStore) Delete(_ context.Context, key string) error {
	s.deletedKey = key
	return s.deleteErr
}

func (s *stubStore) Ping(context.Context) error { return nil }

// repoWith returns a stub whose reads answer the given rows. Rows are copied so
// the service cannot mutate the caller's fixture.
func repoWith(rows ...model.Media) *stubRepo {
	repo := &stubRepo{rows: make(map[int64]*model.Media, len(rows))}
	for _, row := range rows {
		stored := row
		repo.rows[row.ID] = &stored
		repo.nextID = max(repo.nextID, row.ID)
	}
	repo.created = nil
	return repo
}

func newTestService(repo MediaRepository, store storage.Store) *MediaService {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewMediaService(repo, store, 15*time.Minute, time.Hour, log)
	// A fixed clock keeps generated object keys predictable in assertions.
	svc.now = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
	return svc
}

func TestRegister(t *testing.T) {
	t.Parallel()

	t.Run("derives kind, key and content type", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepo{rows: make(map[int64]*model.Media)}
		store := &stubStore{bucket: "media"}
		svc := newTestService(repo, store)

		media, upload, err := svc.Register(context.Background(), &model.CreateMediaRequest{
			OwnerID:  "user-1",
			Filename: "photo.PNG",
		})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}

		if media.Kind != model.KindImage {
			t.Errorf("kind = %q, want image", media.Kind)
		}
		if media.ContentType != "image/png" {
			t.Errorf("content type = %q, want image/png", media.ContentType)
		}
		if media.Status != model.StatusPending {
			t.Errorf("status = %q, want pending", media.Status)
		}
		if media.Bucket != "media" {
			t.Errorf("bucket = %q, want media", media.Bucket)
		}
		// Year/month partition plus the timestamped random name.
		if want := "media/2026/09/"; !strings.HasPrefix(media.ObjectKey, want) {
			t.Errorf("object key = %q, want prefix %q", media.ObjectKey, want)
		}
		if !strings.HasSuffix(media.ObjectKey, ".png") {
			t.Errorf("object key = %q, want a .png suffix", media.ObjectKey)
		}
		if upload.Method != "PUT" {
			t.Errorf("upload method = %q, want PUT", upload.Method)
		}
		if !strings.Contains(upload.URL, media.ObjectKey) {
			t.Errorf("upload url %q does not reference the object key", upload.URL)
		}
	})

	t.Run("keeps an explicitly declared kind", func(t *testing.T) {
		t.Parallel()

		svc := newTestService(&stubRepo{}, &stubStore{bucket: "media"})
		media, _, err := svc.Register(context.Background(), &model.CreateMediaRequest{
			OwnerID: "user-1", Filename: "clip.mp4",
			ContentType: "video/mp4", Kind: model.KindOther,
		})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		if media.Kind != model.KindOther {
			t.Errorf("kind = %q, want the declared other", media.Kind)
		}
	})

	t.Run("generates a distinct key for every upload", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepo{rows: make(map[int64]*model.Media)}
		// The same instant for both calls, which is exactly the collision case the
		// random suffix exists to survive.
		svc := newTestService(repo, &stubStore{bucket: "media"})

		first, _, err := svc.Register(context.Background(), &model.CreateMediaRequest{
			OwnerID: "user-1", Filename: "a.png",
		})
		if err != nil {
			t.Fatalf("first Register: %v", err)
		}
		second, _, err := svc.Register(context.Background(), &model.CreateMediaRequest{
			OwnerID: "user-1", Filename: "a.png",
		})
		if err != nil {
			t.Fatalf("second Register: %v", err)
		}
		if first.ObjectKey == second.ObjectKey {
			t.Errorf("both uploads got object key %q", first.ObjectKey)
		}
	})

	t.Run("removes the record when presigning fails", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepo{rows: make(map[int64]*model.Media)}
		store := &stubStore{bucket: "media", presignErr: errors.New("store unreachable")}
		svc := newTestService(repo, store)

		if _, _, err := svc.Register(context.Background(), &model.CreateMediaRequest{
			OwnerID: "user-1", Filename: "a.png",
		}); !errors.Is(err, store.presignErr) {
			t.Fatalf("err = %v, want the presign failure", err)
		}
		// Without the cleanup the row would be a pending record no client can ever
		// complete.
		if !repo.deleted {
			t.Error("record was not cleaned up after the presign failure")
		}
	})

	t.Run("rejects invalid input", func(t *testing.T) {
		t.Parallel()

		svc := newTestService(&stubRepo{}, &stubStore{bucket: "media"})
		cases := map[string]model.CreateMediaRequest{
			"missing owner":    {Filename: "a.png"},
			"missing filename": {OwnerID: "user-1"},
			"negative size":    {OwnerID: "user-1", Filename: "a.png", SizeBytes: -1},
			"malformed type":   {OwnerID: "user-1", Filename: "a.png", ContentType: "not a/type"},
			"newline in name":  {OwnerID: "user-1", Filename: "a\nb.png"},
			"unknown kind":     {OwnerID: "user-1", Filename: "a.png", Kind: "spreadsheet"},
		}
		for name, req := range cases {
			_, _, err := svc.Register(context.Background(), &req)
			if !errors.Is(err, apperror.ErrInvalidInput) {
				t.Errorf("%s: err = %v, want ErrInvalidInput", name, err)
			}
		}
	})

	t.Run("falls back to octet-stream for an unknown extension", func(t *testing.T) {
		t.Parallel()

		svc := newTestService(&stubRepo{}, &stubStore{bucket: "media"})
		media, _, err := svc.Register(context.Background(), &model.CreateMediaRequest{
			OwnerID: "user-1", Filename: "archive.unknown-ext",
		})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		if media.ContentType != "application/octet-stream" {
			t.Errorf("content type = %q, want application/octet-stream", media.ContentType)
		}
		if media.Kind != model.KindOther {
			t.Errorf("kind = %q, want other", media.Kind)
		}
	})
}

func TestCompleteUpload(t *testing.T) {
	t.Parallel()

	t.Run("records the size the store reports", func(t *testing.T) {
		t.Parallel()

		// The client claimed 10 bytes at registration; the store knows better.
		repo := repoWith(model.Media{
			ID: 7, ObjectKey: "media/2026/09/x.png", Status: model.StatusPending,
		})
		store := &stubStore{bucket: "media", info: &storage.ObjectInfo{
			Size: 4096, ETag: "abc-1", ContentType: "image/png",
		}}
		svc := newTestService(repo, store)

		media, err := svc.CompleteUpload(context.Background(), 7)
		if err != nil {
			t.Fatalf("CompleteUpload: %v", err)
		}
		if media.Status != model.StatusReady {
			t.Errorf("status = %q, want ready", media.Status)
		}
		if repo.gotMarkSize != 4096 {
			t.Errorf("stored size = %d, want 4096", repo.gotMarkSize)
		}
		if repo.gotMarkETag != "abc-1" {
			t.Errorf("stored etag = %q, want abc-1", repo.gotMarkETag)
		}
	})

	t.Run("reports not ready when nothing was uploaded", func(t *testing.T) {
		t.Parallel()

		repo := repoWith(model.Media{ID: 7, Status: model.StatusPending})
		svc := newTestService(repo, &stubStore{bucket: "media"})

		_, err := svc.CompleteUpload(context.Background(), 7)
		if !errors.Is(err, apperror.ErrNotReady) {
			t.Fatalf("err = %v, want ErrNotReady", err)
		}
	})

	t.Run("rejects an empty object", func(t *testing.T) {
		t.Parallel()

		repo := repoWith(model.Media{ID: 7, Status: model.StatusPending})
		store := &stubStore{bucket: "media", info: &storage.ObjectInfo{Size: 0}}
		svc := newTestService(repo, store)

		if _, err := svc.CompleteUpload(context.Background(), 7); !errors.Is(err, apperror.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("is idempotent once ready", func(t *testing.T) {
		t.Parallel()

		ready := &model.Media{ID: 7, Status: model.StatusReady, SizeBytes: 10}
		repo := repoWith(*ready)
		svc := newTestService(repo, &stubStore{bucket: "media"})

		if _, err := svc.CompleteUpload(context.Background(), 7); err != nil {
			t.Fatalf("re-completing a ready media failed: %v", err)
		}
		if repo.gotMarkSize != 0 {
			t.Error("a second CompleteUpload rewrote the stored size")
		}
	})
}

func TestGetDownloadURL(t *testing.T) {
	t.Parallel()

	ready := &model.Media{ID: 3, ObjectKey: "media/2026/09/a.png", Status: model.StatusReady}

	t.Run("uses the configured default ttl", func(t *testing.T) {
		t.Parallel()

		store := &stubStore{bucket: "media"}
		svc := newTestService(repoWith(*ready), store)

		if _, err := svc.GetDownloadURL(context.Background(), 3, 0); err != nil {
			t.Fatalf("GetDownloadURL: %v", err)
		}
		if store.downloadTTL != time.Hour {
			t.Errorf("ttl = %s, want the 1h default", store.downloadTTL)
		}
	})

	t.Run("honours a caller supplied ttl", func(t *testing.T) {
		t.Parallel()

		store := &stubStore{bucket: "media"}
		svc := newTestService(repoWith(*ready), store)

		if _, err := svc.GetDownloadURL(context.Background(), 3, 5*time.Minute); err != nil {
			t.Fatalf("GetDownloadURL: %v", err)
		}
		if store.downloadTTL != 5*time.Minute {
			t.Errorf("ttl = %s, want 5m", store.downloadTTL)
		}
	})

	t.Run("rejects a ttl the store cannot sign", func(t *testing.T) {
		t.Parallel()

		svc := newTestService(repoWith(*ready), &stubStore{bucket: "media"})
		if _, err := svc.GetDownloadURL(context.Background(), 3, 30*24*time.Hour); !errors.Is(err, apperror.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
	})

	t.Run("refuses a pending media", func(t *testing.T) {
		t.Parallel()

		repo := repoWith(model.Media{ID: 3, Status: model.StatusPending})
		svc := newTestService(repo, &stubStore{bucket: "media"})

		if _, err := svc.GetDownloadURL(context.Background(), 3, 0); !errors.Is(err, apperror.ErrNotReady) {
			t.Fatalf("err = %v, want ErrNotReady", err)
		}
	})
}

func TestGetUploadURL(t *testing.T) {
	t.Parallel()

	t.Run("reissues for a pending media", func(t *testing.T) {
		t.Parallel()

		repo := repoWith(model.Media{ID: 3, Status: model.StatusPending})
		svc := newTestService(repo, &stubStore{bucket: "media"})

		if _, err := svc.GetUploadURL(context.Background(), 3); err != nil {
			t.Fatalf("GetUploadURL: %v", err)
		}
	})

	t.Run("refuses to reissue for a ready media", func(t *testing.T) {
		t.Parallel()

		// Re-uploading would overwrite the object behind an already published URL.
		repo := repoWith(model.Media{ID: 3, Status: model.StatusReady})
		svc := newTestService(repo, &stubStore{bucket: "media"})

		if _, err := svc.GetUploadURL(context.Background(), 3); !errors.Is(err, apperror.ErrConflict) {
			t.Fatalf("err = %v, want ErrConflict", err)
		}
	})
}

func TestBatchGetDownloadURLs(t *testing.T) {
	t.Parallel()

	t.Run("skips ids that are missing or not ready", func(t *testing.T) {
		t.Parallel()

		repo := repoWith(model.Media{ID: 1, Status: model.StatusReady})
		store := &stubStore{bucket: "media"}
		svc := newTestService(repo, store)

		urls, err := svc.BatchGetDownloadURLs(context.Background(), []int64{1, 2, 1}, 0)
		if err != nil {
			t.Fatalf("BatchGetDownloadURLs: %v", err)
		}
		if len(urls) != 1 {
			t.Fatalf("got %d urls, want 1", len(urls))
		}
		if _, ok := urls[1]; !ok {
			t.Error("the ready media is missing from the result")
		}
		// The repeated id must not cause a second presign.
		if store.downloadTTL == 0 {
			t.Error("no url was presigned")
		}
	})

	t.Run("returns an empty map for no ids", func(t *testing.T) {
		t.Parallel()

		svc := newTestService(&stubRepo{}, &stubStore{bucket: "media"})
		urls, err := svc.BatchGetDownloadURLs(context.Background(), nil, 0)
		if err != nil {
			t.Fatalf("BatchGetDownloadURLs: %v", err)
		}
		if urls == nil {
			t.Error("map is nil, want an empty map")
		}
		if len(urls) != 0 {
			t.Errorf("got %d urls, want 0", len(urls))
		}
	})
}

func TestDelete(t *testing.T) {
	t.Parallel()

	t.Run("removes the object before the row", func(t *testing.T) {
		t.Parallel()

		repo := repoWith(model.Media{ID: 5, ObjectKey: "media/2026/09/a.png"})
		store := &stubStore{bucket: "media"}
		svc := newTestService(repo, store)

		if err := svc.Delete(context.Background(), 5); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if store.deletedKey != "media/2026/09/a.png" {
			t.Errorf("deleted object key = %q", store.deletedKey)
		}
		if repo.gotDeleteID != 5 {
			t.Errorf("deleted row = %d, want 5", repo.gotDeleteID)
		}
	})

	t.Run("keeps the row when the object delete fails", func(t *testing.T) {
		t.Parallel()

		repo := repoWith(model.Media{ID: 5})
		store := &stubStore{bucket: "media", deleteErr: errors.New("store down")}
		svc := newTestService(repo, store)

		if err := svc.Delete(context.Background(), 5); err == nil {
			t.Fatal("Delete succeeded, want the storage failure")
		}
		// Deleting the row first would orphan bytes nothing can find again.
		if repo.deleted {
			t.Error("the row was deleted even though the object removal failed")
		}
	})
}

func TestList(t *testing.T) {
	t.Parallel()

	t.Run("applies defaults and caps the page size", func(t *testing.T) {
		t.Parallel()

		repo := &stubRepo{rows: make(map[int64]*model.Media)}
		svc := newTestService(repo, &stubStore{bucket: "media"})

		if _, err := svc.List(context.Background(), nil); err != nil {
			t.Fatalf("List: %v", err)
		}
		if repo.gotQuery.Limit != defaultListLimit {
			t.Errorf("limit = %d, want the default %d", repo.gotQuery.Limit, defaultListLimit)
		}

		if _, err := svc.List(context.Background(), &model.ListMediaQuery{Limit: maxListLimit + 1}); !errors.Is(err, apperror.ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput for an oversized page", err)
		}
	})
}

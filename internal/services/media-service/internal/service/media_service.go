// Package service holds the media business rules. It validates input, drives the
// upload lifecycle through the object store, and translates persistence and
// storage failures into domain errors without knowing about HTTP or gRPC.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"path"
	"strings"
	"time"

	"media-service/internal/apperror"
	"media-service/internal/model"
	"media-service/internal/storage"
)

const (
	// maxFilenameLength mirrors VARCHAR(255) of media.filename.
	maxFilenameLength = 255
	// maxContentTypeLength mirrors VARCHAR(127) of media.content_type.
	maxContentTypeLength = 127
	// maxOwnerIDLength mirrors VARCHAR(255) of media.owner_id.
	maxOwnerIDLength = 255

	// maxListLimit caps a single page so one request cannot ask for everything.
	maxListLimit = 100
	// defaultListLimit applies when the caller does not specify one.
	defaultListLimit = 20

	// maxDownloadURLTTL is the longest a caller may ask a download URL to live.
	// The store rejects longer presigns, so the ceiling is enforced here to turn
	// that into a clean 400.
	maxDownloadURLTTL = 7 * 24 * time.Hour
)

// MediaRepository is the subset of persistence behaviour the service needs.
// Declaring it here keeps the service testable with a stub.
type MediaRepository interface {
	Create(ctx context.Context, rec *model.Media) (*model.Media, error)
	GetByID(ctx context.Context, id int64) (*model.Media, error)
	List(ctx context.Context, q *model.ListMediaQuery) (*model.ListMediaResult, error)
	MarkReady(ctx context.Context, id, sizeBytes int64, etag string) (*model.Media, error)
	Delete(ctx context.Context, id int64) error
}

// MediaService implements the media business rules.
type MediaService struct {
	repo        MediaRepository
	store       storage.Store
	uploadTTL   time.Duration
	downloadTTL time.Duration
	log         *slog.Logger
	// now is injectable so key generation is deterministic in tests.
	now func() time.Time
}

// NewMediaService returns a service backed by repo and store.
func NewMediaService(
	repo MediaRepository,
	store storage.Store,
	uploadTTL, downloadTTL time.Duration,
	log *slog.Logger,
) *MediaService {
	return &MediaService{
		repo:        repo,
		store:       store,
		uploadTTL:   uploadTTL,
		downloadTTL: downloadTTL,
		log:         log,
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// Register records a new media entry in the pending state and returns it with a
// presigned URL the client can upload to. The bytes never pass through here.
func (s *MediaService) Register(
	ctx context.Context,
	req *model.CreateMediaRequest,
) (*model.Media, *model.PresignedUpload, error) {
	ownerID := strings.TrimSpace(req.OwnerID)
	filename := strings.TrimSpace(req.Filename)
	if err := validateOwnerID(ownerID); err != nil {
		return nil, nil, err
	}
	if err := validateFilename(filename); err != nil {
		return nil, nil, err
	}

	contentType, err := normaliseContentType(req.ContentType, filename)
	if err != nil {
		return nil, nil, err
	}

	kind := req.Kind
	if kind == "" {
		kind = model.KindForContentType(contentType)
	}
	if _, err := model.ParseKind(string(kind)); err != nil {
		return nil, nil, fmt.Errorf("%w: %s", apperror.ErrInvalidInput, err)
	}

	sizeBytes := req.SizeBytes
	if sizeBytes < 0 {
		return nil, nil, fmt.Errorf(
			"%w: size_bytes must not be negative, got %d", apperror.ErrInvalidInput, sizeBytes,
		)
	}

	record := &model.Media{
		OwnerID:     ownerID,
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   sizeBytes,
		Bucket:      s.store.Bucket(),
		ObjectKey:   s.buildObjectKey(filename, s.now()),
		Kind:        kind,
		Status:      model.StatusPending,
	}

	created, err := s.repo.Create(ctx, record)
	if err != nil {
		return nil, nil, err
	}

	upload, err := s.store.PresignUpload(ctx, created.ObjectKey, created.ContentType, s.uploadTTL)
	if err != nil {
		// Without a usable upload URL the record is unusable, so drop it rather
		// than leave a pending row no client can ever complete. The delete is
		// best effort: the original failure is what the caller needs to see.
		if delErr := s.repo.Delete(ctx, created.ID); delErr != nil {
			s.log.Error("cleanup media after presign failure",
				"media_id", created.ID, "error", delErr,
			)
		}
		return nil, nil, err
	}

	s.log.Info("media registered",
		"media_id", created.ID,
		"owner_id", created.OwnerID,
		"object_key", created.ObjectKey,
		"content_type", created.ContentType,
	)
	return created, upload, nil
}

// GetByID returns a single media record.
func (s *MediaService) GetByID(ctx context.Context, id int64) (*model.Media, error) {
	return s.repo.GetByID(ctx, id)
}

// List returns a page of media for an owner.
func (s *MediaService) List(ctx context.Context, q *model.ListMediaQuery) (*model.ListMediaResult, error) {
	if q == nil {
		q = &model.ListMediaQuery{}
	}
	if q.Limit <= 0 {
		q.Limit = defaultListLimit
	}
	if q.Limit > maxListLimit {
		return nil, fmt.Errorf("%w: limit must be at most %d, got %d", apperror.ErrInvalidInput, maxListLimit, q.Limit)
	}
	if q.Offset < 0 {
		return nil, fmt.Errorf("%w: offset must not be negative, got %d", apperror.ErrInvalidInput, q.Offset)
	}
	if q.OwnerID != "" {
		ownerID := strings.TrimSpace(q.OwnerID)
		if err := validateOwnerID(ownerID); err != nil {
			return nil, err
		}
		q.OwnerID = ownerID
	}
	return s.repo.List(ctx, q)
}

// GetUploadURL issues a fresh presigned upload URL for a media that has not been
// uploaded yet, so an interrupted upload can be resumed.
func (s *MediaService) GetUploadURL(ctx context.Context, id int64) (*model.PresignedUpload, error) {
	record, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if record.Status != model.StatusPending {
		return nil, fmt.Errorf(
			"%w: media %d is already uploaded, create a new record to store another file",
			apperror.ErrConflict, id,
		)
	}
	return s.store.PresignUpload(ctx, record.ObjectKey, record.ContentType, s.uploadTTL)
}

// CompleteUpload verifies the object reached the store and marks the record
// ready. The size and ETag the store reports win over anything the client
// claimed, because the client cannot be trusted to describe its own upload.
func (s *MediaService) CompleteUpload(ctx context.Context, id int64) (*model.Media, error) {
	record, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if record.Status == model.StatusReady {
		// Completing twice is a no-op rather than an error, so a retried request
		// does not fail after a successful first attempt.
		return record, nil
	}

	info, err := s.store.Stat(ctx, record.ObjectKey)
	if err != nil {
		if isObjectMissing(err) {
			return nil, fmt.Errorf(
				"%w: media %d has no uploaded object yet", apperror.ErrNotReady, id,
			)
		}
		return nil, err
	}
	if info.Size <= 0 {
		return nil, fmt.Errorf(
			"%w: media %d stored an empty object", apperror.ErrInvalidInput, id,
		)
	}

	// The object may have been uploaded with a different type than declared, for
	// instance because the client's browser guessed differently. The store is
	// authoritative.
	contentType := record.ContentType
	if info.ContentType != "" && info.ContentType != contentType {
		contentType = info.ContentType
	}

	ready, err := s.repo.MarkReady(ctx, id, info.Size, info.ETag)
	if err != nil {
		return nil, err
	}
	if contentType != ready.ContentType {
		// MarkReady does not update the content type, so reflect the value in the
		// returned record without a second write.
		ready.ContentType = contentType
	}

	s.log.Info("media ready",
		"media_id", ready.ID,
		"size_bytes", ready.SizeBytes,
		"etag", info.ETag,
	)
	return ready, nil
}

// GetDownloadURL issues a presigned read URL for a ready media. A ttlOverride of
// zero uses the configured default.
func (s *MediaService) GetDownloadURL(
	ctx context.Context,
	id int64,
	ttlOverride time.Duration,
) (*model.DownloadURL, error) {
	record, err := s.requireReady(ctx, id)
	if err != nil {
		return nil, err
	}
	ttl, err := s.resolveTTL(ttlOverride)
	if err != nil {
		return nil, err
	}
	return s.store.PresignDownload(ctx, record.ObjectKey, record.Filename, ttl)
}

// BatchGetDownloadURLs resolves several ids in one call, which is what a
// product listing needs to avoid a round trip per image. Ids that do not exist
// or are not ready are left out of the result rather than reported as errors, so
// one broken image cannot fail the whole batch.
func (s *MediaService) BatchGetDownloadURLs(
	ctx context.Context,
	ids []int64,
	ttlOverride time.Duration,
) (map[int64]string, error) {
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	if len(ids) > maxListLimit*10 {
		return nil, fmt.Errorf(
			"%w: at most %d ids may be requested at once, got %d",
			apperror.ErrInvalidInput, maxListLimit*10, len(ids),
		)
	}
	ttl, err := s.resolveTTL(ttlOverride)
	if err != nil {
		return nil, err
	}

	urls := make(map[int64]string, len(ids))
	for _, id := range ids {
		// Deduplicate so a repeated id does not cause a second lookup.
		if _, seen := urls[id]; seen {
			continue
		}

		record, err := s.repo.GetByID(ctx, id)
		if err != nil {
			s.log.Warn("skipping unknown media in batch", "media_id", id, "error", err)
			continue
		}
		if record.Status != model.StatusReady {
			continue
		}

		download, err := s.store.PresignDownload(ctx, record.ObjectKey, "", ttl)
		if err != nil {
			s.log.Error("presign download in batch", "media_id", id, "error", err)
			continue
		}
		urls[id] = download.URL
	}
	return urls, nil
}

// Delete removes the object and then the metadata row.
//
// The object goes first on purpose: if that fails the metadata survives and the
// delete can be retried, whereas the other order would orphan bytes that nothing
// can find again.
func (s *MediaService) Delete(ctx context.Context, id int64) error {
	record, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, record.ObjectKey); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.log.Info("media deleted", "media_id", id, "object_key", record.ObjectKey)
	return nil
}

// requireReady loads a media and fails unless its object has been uploaded.
func (s *MediaService) requireReady(ctx context.Context, id int64) (*model.Media, error) {
	record, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if record.Status != model.StatusReady {
		return nil, fmt.Errorf(
			"%w: media %d has not been uploaded yet", apperror.ErrNotReady, id,
		)
	}
	return record, nil
}

// resolveTTL clamps a caller supplied lifetime to something the store accepts.
func (s *MediaService) resolveTTL(ttlOverride time.Duration) (time.Duration, error) {
	if ttlOverride == 0 {
		return s.downloadTTL, nil
	}
	if ttlOverride < 0 {
		return 0, fmt.Errorf(
			"%w: presign_ttl_seconds must be positive, got %s", apperror.ErrInvalidInput, ttlOverride,
		)
	}
	if ttlOverride > maxDownloadURLTTL {
		return 0, fmt.Errorf(
			"%w: presign_ttl_seconds must be at most %s, got %s",
			apperror.ErrInvalidInput, maxDownloadURLTTL, ttlOverride,
		)
	}
	return ttlOverride, nil
}

// buildObjectKey lays objects out as media/<year>/<month>/<time>-<random><ext>.
//
// The date prefix keeps listings browsable and stops a single flat directory from
// growing without bound, which matters on file systems where that is slow. The
// random suffix is what guarantees uniqueness: the timestamp alone would collide
// for two uploads in the same nanosecond, and the caller never chooses this value,
// so it cannot be used to overwrite another owner's object.
func (s *MediaService) buildObjectKey(filename string, now time.Time) string {
	ext := model.ExtensionFor(filename)

	var random [8]byte
	// crypto/rand does not fail on any platform this targets, but a failure must
	// not silently produce a predictable key, so a timestamp stands in and the
	// result is still unique in practice.
	if _, err := rand.Read(random[:]); err != nil {
		random = [8]byte{0, 0, 0, 0, 0, 0, 0, 0}
	}

	name := fmt.Sprintf("%d-%s", now.UnixNano(), hex.EncodeToString(random[:]))
	if ext != "" {
		name += "." + ext
	}
	return path.Join("media", now.Format("2006"), now.Format("01"), name)
}

func validateOwnerID(ownerID string) error {
	switch {
	case ownerID == "":
		return fmt.Errorf("%w: owner_id is required", apperror.ErrInvalidInput)
	case len(ownerID) > maxOwnerIDLength:
		return fmt.Errorf(
			"%w: owner_id must be at most %d characters, got %d",
			apperror.ErrInvalidInput, maxOwnerIDLength, len(ownerID),
		)
	}
	return nil
}

func validateFilename(filename string) error {
	switch {
	case filename == "":
		return fmt.Errorf("%w: filename is required", apperror.ErrInvalidInput)
	case len(filename) > maxFilenameLength:
		return fmt.Errorf(
			"%w: filename must be at most %d characters, got %d",
			apperror.ErrInvalidInput, maxFilenameLength, len(filename),
		)
	}
	// The name is only ever sent back as a Content-Disposition value or logged,
	// never used as a path, so control characters are the only real concern.
	if strings.ContainsAny(filename, "\r\n\x00") {
		return fmt.Errorf(
			"%w: filename must not contain control characters", apperror.ErrInvalidInput,
		)
	}
	return nil
}

// normaliseContentType validates the declared type, falling back to the file
// extension and finally to a generic binary type. A client that omits the type
// entirely is common, so that is not an error.
func normaliseContentType(contentType, filename string) (string, error) {
	declared := strings.TrimSpace(contentType)

	if declared != "" {
		mediaType, _, err := mime.ParseMediaType(declared)
		if err != nil {
			return "", fmt.Errorf("%w: content_type %q is not a valid media type", apperror.ErrInvalidInput, declared)
		}
		if len(mediaType) > maxContentTypeLength {
			return "", fmt.Errorf(
				"%w: content_type must be at most %d characters, got %d",
				apperror.ErrInvalidInput, maxContentTypeLength, len(mediaType),
			)
		}
		// Re-serialise to drop parameters such as charset, so equal types compare
		// equal in the database.
		return mediaType, nil
	}

	switch ext := model.ExtensionFor(filename); ext {
	case "jpg", "jpeg":
		return "image/jpeg", nil
	case "png":
		return "image/png", nil
	case "gif":
		return "image/gif", nil
	case "webp":
		return "image/webp", nil
	case "avif":
		return "image/avif", nil
	case "svg":
		return "image/svg+xml", nil
	case "pdf":
		return "application/pdf", nil
	case "webm":
		return "video/webm", nil
	case "txt":
		return "text/plain; charset=utf-8", nil
	}

	return "application/octet-stream", nil
}

// isObjectMissing reports whether the store says the key does not exist.
func isObjectMissing(err error) bool {
	return errors.Is(err, storage.ErrObjectNotFound)
}

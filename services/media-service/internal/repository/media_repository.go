// Package repository implements PostgreSQL persistence for media metadata. All
// SQL lives here; higher layers work with domain types and sentinel errors.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"media-service/internal/apperror"
	"media-service/internal/model"
)

// MediaRepository stores media metadata in PostgreSQL.
type MediaRepository struct {
	db *sql.DB
}

// NewMediaRepository returns a repository backed by db.
func NewMediaRepository(db *sql.DB) *MediaRepository {
	return &MediaRepository{db: db}
}

const mediaColumns = `id, owner_id, filename, content_type, size_bytes, etag,
	bucket, object_key, kind, status, created_at, updated_at`

// scannedMedia holds a row using the sql null types, which the driver needs for
// scanning but which the model does not expose.
type scannedMedia struct {
	id          int64
	ownerID     string
	filename    string
	contentType string
	sizeBytes   int64
	etag        sql.NullString
	bucket      string
	objectKey   string
	kind        string
	status      string
	createdAt   time.Time
	updatedAt   time.Time
}

func (s scannedMedia) media() *model.Media {
	m := &model.Media{
		ID:          s.id,
		OwnerID:     s.ownerID,
		Filename:    s.filename,
		ContentType: s.contentType,
		SizeBytes:   s.sizeBytes,
		Bucket:      s.bucket,
		ObjectKey:   s.objectKey,
		Kind:        model.Kind(s.kind),
		Status:      model.Status(s.status),
		CreatedAt:   s.createdAt,
		UpdatedAt:   s.updatedAt,
	}
	if s.etag.Valid {
		etag := s.etag.String
		m.ETag = &etag
	}
	return m
}

// scanDest returns the scan targets for a row.
//
// The receiver must be a pointer: the returned slice holds addresses of the
// struct's fields, and with a value receiver those addresses would point into
// this method's copy of the row rather than the caller's, so the values the
// driver writes would be discarded.
func (s *scannedMedia) scanDest() []any {
	return []any{
		&s.id,
		&s.ownerID,
		&s.filename,
		&s.contentType,
		&s.sizeBytes,
		&s.etag,
		&s.bucket,
		&s.objectKey,
		&s.kind,
		&s.status,
		&s.createdAt,
		&s.updatedAt,
	}
}

// Create inserts a new media record and returns the stored row. The caller
// supplies a fully populated record with a zero ID.
func (r *MediaRepository) Create(ctx context.Context, rec *model.Media) (*model.Media, error) {
	const query = `
		INSERT INTO media (
			owner_id, filename, content_type, size_bytes, bucket, object_key, kind, status
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + mediaColumns

	row := scannedMedia{}
	err := r.db.QueryRowContext(ctx, query,
		rec.OwnerID,
		rec.Filename,
		rec.ContentType,
		rec.SizeBytes,
		rec.Bucket,
		rec.ObjectKey,
		string(rec.Kind),
		string(rec.Status),
	).Scan(row.scanDest()...)
	switch {
	case isUniqueViolation(err):
		return nil, fmt.Errorf("%w: object key %q is already in use", apperror.ErrConflict, rec.ObjectKey)
	case err != nil:
		return nil, fmt.Errorf("create media: %w", err)
	}
	return row.media(), nil
}

// GetByID returns a single media record.
func (r *MediaRepository) GetByID(ctx context.Context, id int64) (*model.Media, error) {
	const query = `SELECT ` + mediaColumns + ` FROM media WHERE id = $1`

	row := scannedMedia{}
	if err := r.db.QueryRowContext(ctx, query, id).Scan(row.scanDest()...); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("%w: media %d", apperror.ErrNotFound, id)
		default:
			return nil, fmt.Errorf("get media %d: %w", id, err)
		}
	}
	return row.media(), nil
}

// List returns a page of media plus the total number of matches.
func (r *MediaRepository) List(ctx context.Context, q *model.ListMediaQuery) (*model.ListMediaResult, error) {
	// Filters are assembled with placeholders so values always travel as bind
	// parameters and the query shape stays constant.
	var (
		where []string
		args  []any
	)
	if q.OwnerID != "" {
		args = append(args, q.OwnerID)
		where = append(where, fmt.Sprintf("owner_id = $%d", len(args)))
	}
	if q.Status != nil {
		args = append(args, string(*q.Status))
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}

	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}

	// Counted separately rather than with a window function: with an empty page
	// a window function yields no rows at all, which would report a total of 0
	// for a query that does have matches.
	var total int64
	countQuery := `SELECT COUNT(*) FROM media` + clause
	if err := r.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("count media: %w", err)
	}

	args = append(args, q.Limit, q.Offset)
	listQuery := fmt.Sprintf(
		`SELECT %s FROM media%s ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d`,
		mediaColumns, clause, len(args)-1, len(args),
	)

	rows, err := r.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("list media: %w", err)
	}
	defer func() { _ = rows.Close() }()

	// An empty slice, not nil, so the API renders [] rather than null.
	items := make([]*model.Media, 0, q.Limit)
	for rows.Next() {
		row := scannedMedia{}
		if err := rows.Scan(row.scanDest()...); err != nil {
			return nil, fmt.Errorf("list media: scan row: %w", err)
		}
		items = append(items, row.media())
	}
	// Without this check a mid-iteration network failure looks like a short page.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list media: %w", err)
	}
	return &model.ListMediaResult{Media: items, Total: total}, nil
}

// MarkReady records the confirmed size and ETag and flips the status to ready.
func (r *MediaRepository) MarkReady(ctx context.Context, id, sizeBytes int64, etag string) (*model.Media, error) {
	const query = `
		UPDATE media
		SET size_bytes = $2, etag = $3, status = 'ready'
		WHERE id = $1
		RETURNING ` + mediaColumns

	row := scannedMedia{}
	err := r.db.QueryRowContext(ctx, query, id, sizeBytes, etag).Scan(row.scanDest()...)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("%w: media %d", apperror.ErrNotFound, id)
	case err != nil:
		return nil, fmt.Errorf("mark media %d ready: %w", id, err)
	}
	return row.media(), nil
}

// Delete removes the media row. The object itself is deleted by the service
// before this is called, so a failure here can only leave an unreferenced row.
func (r *MediaRepository) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM media WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete media %d: %w", id, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete media %d: rows affected: %w", id, err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("%w: media %d", apperror.ErrNotFound, id)
	}
	return nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique constraint
// violation. lib/pq surfaces this as *pq.Error, and the SQLSTATE is checked
// rather than the message text so it survives wording changes.
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

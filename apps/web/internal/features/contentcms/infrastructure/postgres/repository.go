package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
)

const (
	pageListLimit     = 100
	revisionListLimit = 100
)

var (
	ErrNotFound                 = ports.ErrNotFound
	_           ports.PageStore = (*Store)(nil)
)

// Store is the PostgreSQL-backed native page store.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) List(ctx context.Context, tenantID string) ([]ports.Page, error) {
	if err := domain.ValidatePageIdentity(tenantID, "x", "x"); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id, slug, title, draft_document, status, revision,
		        created_at, updated_at, published_at
		 FROM content_pages
		 WHERE tenant_id = $1
		 ORDER BY updated_at DESC, slug
		 LIMIT $2`,
		tenantID, pageListLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list content pages: %w", err)
	}
	defer rows.Close()

	pages := make([]ports.Page, 0, pageListLimit)
	for rows.Next() {
		page, err := scanPage(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan content page: %w", err)
		}
		pages = append(pages, page)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list content pages: %w", err)
	}
	return pages, nil
}

func (s *Store) ReadDraft(ctx context.Context, tenantID, slug string) (ports.Page, error) {
	if err := validatePageLookup(tenantID, slug); err != nil {
		return ports.Page{}, err
	}
	page, err := scanPage(s.pool.QueryRow(ctx,
		`SELECT tenant_id, slug, title, draft_document, status, revision,
		        created_at, updated_at, published_at
		 FROM content_pages
		 WHERE tenant_id = $1 AND slug = $2`,
		tenantID, slug,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.Page{}, ErrNotFound
	}
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: read content page draft: %w", err)
	}
	return page, nil
}

func (s *Store) ReadPublished(ctx context.Context, tenantID, slug string) (ports.Page, error) {
	if err := validatePageLookup(tenantID, slug); err != nil {
		return ports.Page{}, err
	}
	page, err := scanPage(s.pool.QueryRow(ctx,
		`SELECT tenant_id, slug, published_title, published_document, 'published',
		        published_revision, created_at, updated_at, published_at
		 FROM content_pages
		 WHERE tenant_id = $1 AND slug = $2 AND published_document IS NOT NULL`,
		tenantID, slug,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.Page{}, ErrNotFound
	}
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: read published content page: %w", err)
	}
	return page, nil
}

func (s *Store) CreateDraft(ctx context.Context, tenantID, slug, title string, document domain.Document, actorID string) (ports.Page, error) {
	document, err := normalizeAndValidateDraft(tenantID, slug, title, document, actorID)
	if err != nil {
		return ports.Page{}, err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: encode content page document: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: begin content page creation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	page, err := scanPage(tx.QueryRow(ctx,
		`INSERT INTO content_pages (tenant_id, slug, title, draft_document, status, revision)
		 VALUES ($1, $2, $3, $4::jsonb, 'draft', 1)
		 RETURNING tenant_id, slug, title, draft_document, status, revision,
		           created_at, updated_at, published_at`,
		tenantID, slug, title, encoded,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ports.Page{}, ports.ErrAlreadyExists
		}
		return ports.Page{}, fmt.Errorf("postgres: create content page draft: %w", err)
	}
	if err := appendRevision(ctx, tx, page, actorID); err != nil {
		return ports.Page{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ports.Page{}, fmt.Errorf("postgres: commit content page creation: %w", err)
	}
	return page, nil
}

func (s *Store) SaveDraft(ctx context.Context, tenantID, slug, title string, document domain.Document, expectedRevision int64, actorID string) (ports.Page, error) {
	document, err := normalizeAndValidateDraft(tenantID, slug, title, document, actorID)
	if err != nil {
		return ports.Page{}, err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: encode content page document: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: begin content page draft save: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	currentRevision := expectedRevision
	if currentRevision <= 0 {
		err := tx.QueryRow(ctx,
			`SELECT revision FROM content_pages WHERE tenant_id = $1 AND slug = $2 FOR UPDATE`,
			tenantID, slug,
		).Scan(&currentRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.Page{}, ErrNotFound
		}
		if err != nil {
			return ports.Page{}, fmt.Errorf("postgres: read content page revision: %w", err)
		}
	}

	page, err := scanPage(tx.QueryRow(ctx,
		`UPDATE content_pages
		 SET title = $3, draft_document = $4::jsonb, status = 'draft',
		     revision = revision + 1, updated_at = now()
		 WHERE tenant_id = $1 AND slug = $2 AND revision = $5
		 RETURNING tenant_id, slug, title, draft_document, status, revision,
		           created_at, updated_at, published_at`,
		tenantID, slug, title, encoded, currentRevision,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		var actual int64
		selectErr := tx.QueryRow(ctx,
			`SELECT revision FROM content_pages WHERE tenant_id = $1 AND slug = $2`,
			tenantID, slug,
		).Scan(&actual)
		if errors.Is(selectErr, pgx.ErrNoRows) {
			return ports.Page{}, ErrNotFound
		}
		if selectErr != nil {
			return ports.Page{}, fmt.Errorf("postgres: read content page conflict revision: %w", selectErr)
		}
		return ports.Page{}, &ports.ConflictError{Expected: currentRevision, Current: actual}
	}
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: save content page draft: %w", err)
	}
	if err := appendRevision(ctx, tx, page, actorID); err != nil {
		return ports.Page{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ports.Page{}, fmt.Errorf("postgres: commit content page draft save: %w", err)
	}
	return page, nil
}

func (s *Store) Publish(ctx context.Context, tenantID, slug string, expectedRevision int64, actorID string) (ports.Page, error) {
	if err := validatePageLookup(tenantID, slug); err != nil {
		return ports.Page{}, err
	}
	if err := validateActor(actorID); err != nil {
		return ports.Page{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: begin content page publish: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var title string
	var encoded []byte
	var currentRevision int64
	if err := tx.QueryRow(ctx,
		`SELECT title, draft_document, revision
		 FROM content_pages
		 WHERE tenant_id = $1 AND slug = $2
		 FOR UPDATE`, tenantID, slug,
	).Scan(&title, &encoded, &currentRevision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.Page{}, ErrNotFound
		}
		return ports.Page{}, fmt.Errorf("postgres: load content page for publish: %w", err)
	}
	if expectedRevision > 0 && expectedRevision != currentRevision {
		return ports.Page{}, &ports.ConflictError{Expected: expectedRevision, Current: currentRevision}
	}
	document, err := decodeDocument(encoded)
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: decode content page draft for publish: %w", err)
	}
	if err := domain.ValidatePageIdentity(tenantID, slug, title); err != nil {
		return ports.Page{}, err
	}
	if err := document.ValidateForPublish(); err != nil {
		return ports.Page{}, err
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: encode content page for publish: %w", err)
	}

	page, err := scanPage(tx.QueryRow(ctx,
		`UPDATE content_pages
		 SET published_title = title,
		     published_document = $3::jsonb,
		     status = 'published',
		     revision = revision + 1,
		     published_revision = revision + 1,
		     published_at = now(),
		     updated_at = now()
		 WHERE tenant_id = $1 AND slug = $2
		 RETURNING tenant_id, slug, published_title, published_document, status,
		           published_revision, created_at, updated_at, published_at`,
		tenantID, slug, canonical,
	))
	if err != nil {
		return ports.Page{}, fmt.Errorf("postgres: publish content page: %w", err)
	}
	if err := appendRevision(ctx, tx, page, actorID); err != nil {
		return ports.Page{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ports.Page{}, fmt.Errorf("postgres: commit content page publish: %w", err)
	}
	return page, nil
}

func (s *Store) ListRevisions(ctx context.Context, tenantID, slug string, limit int) ([]ports.Revision, error) {
	if err := validatePageLookup(tenantID, slug); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > revisionListLimit {
		limit = revisionListLimit
	}
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id, slug, revision, title, document, status, actor_id, created_at
		 FROM content_page_revisions
		 WHERE tenant_id = $1 AND slug = $2
		 ORDER BY revision DESC
		 LIMIT $3`,
		tenantID, slug, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list content revisions: %w", err)
	}
	defer rows.Close()

	revisions := make([]ports.Revision, 0, limit)
	for rows.Next() {
		revision, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan content revision: %w", err)
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list content revisions: %w", err)
	}
	return revisions, nil
}

func (s *Store) ReadRevision(ctx context.Context, tenantID, slug string, revision int64) (ports.Revision, error) {
	if err := validatePageLookup(tenantID, slug); err != nil {
		return ports.Revision{}, err
	}
	if revision <= 0 {
		return ports.Revision{}, fmt.Errorf("%w: revision must be positive", domain.ErrInvalidData)
	}
	result, err := scanRevision(s.pool.QueryRow(ctx,
		`SELECT tenant_id, slug, revision, title, document, status, actor_id, created_at
		 FROM content_page_revisions
		 WHERE tenant_id = $1 AND slug = $2 AND revision = $3`,
		tenantID, slug, revision,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.Revision{}, ErrNotFound
	}
	if err != nil {
		return ports.Revision{}, fmt.Errorf("postgres: read content revision: %w", err)
	}
	return result, nil
}

func (s *Store) ListSections(ctx context.Context, tenantID string) ([]ports.ReusableSection, error) {
	if err := domain.ValidatePageIdentity(tenantID, "x", "x"); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id, id, name, component, created_at
		 FROM content_sections
		 WHERE tenant_id = $1
		 ORDER BY created_at DESC, id
		 LIMIT 200`,
		tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list content sections: %w", err)
	}
	defer rows.Close()

	sections := make([]ports.ReusableSection, 0, 32)
	for rows.Next() {
		section, err := scanSection(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan content section: %w", err)
		}
		sections = append(sections, section)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list content sections: %w", err)
	}
	return sections, nil
}

func (s *Store) CreateSection(ctx context.Context, tenantID, id, name string, component domain.Component, actorID string) (ports.ReusableSection, error) {
	if err := domain.ValidatePageIdentity(tenantID, "x", "x"); err != nil {
		return ports.ReusableSection{}, err
	}
	if !validSectionID(id) {
		return ports.ReusableSection{}, fmt.Errorf("%w: section id must be a UUID", domain.ErrInvalidData)
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return ports.ReusableSection{}, fmt.Errorf("%w: section name is required and must be at most 120 bytes", domain.ErrInvalidData)
	}
	if err := validateActor(actorID); err != nil {
		return ports.ReusableSection{}, err
	}
	if err := (domain.Document{SchemaVersion: domain.SchemaVersion, Components: []domain.Component{component}}).Validate(); err != nil {
		return ports.ReusableSection{}, err
	}
	encoded, err := json.Marshal(component)
	if err != nil {
		return ports.ReusableSection{}, fmt.Errorf("postgres: encode content section: %w", err)
	}
	section, err := scanSection(s.pool.QueryRow(ctx,
		`INSERT INTO content_sections (tenant_id, id, name, component, actor_id)
		 VALUES ($1, $2, $3, $4::jsonb, $5)
		 RETURNING tenant_id, id, name, component, created_at`,
		tenantID, id, name, encoded, actorID,
	))
	if err != nil {
		return ports.ReusableSection{}, fmt.Errorf("postgres: create content section: %w", err)
	}
	return section, nil
}

func (s *Store) DeleteSection(ctx context.Context, tenantID, id string) error {
	if err := domain.ValidatePageIdentity(tenantID, "x", "x"); err != nil {
		return err
	}
	if !validSectionID(id) {
		return fmt.Errorf("%w: section id must be a UUID", domain.ErrInvalidData)
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM content_sections WHERE tenant_id = $1 AND id = $2`,
		tenantID, id,
	)
	if err != nil {
		return fmt.Errorf("postgres: delete content section: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SubmitForm(ctx context.Context, tenantID, slug string, pageRevision int64, componentID string, values map[string]string) error {
	if err := validatePageLookup(tenantID, slug); err != nil {
		return err
	}
	if pageRevision <= 0 || !validComponentID(componentID) {
		return fmt.Errorf("%w: invalid form identity", domain.ErrInvalidData)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("postgres: encode form submission: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO content_form_submissions
		 (tenant_id, slug, page_revision, component_id, values)
		 VALUES ($1, $2, $3, $4, $5::jsonb)`,
		tenantID, slug, pageRevision, componentID, encoded,
	); err != nil {
		return fmt.Errorf("postgres: store form submission: %w", err)
	}
	return nil
}

func appendRevision(ctx context.Context, tx pgx.Tx, page ports.Page, actorID string) error {
	encoded, err := json.Marshal(page.Document)
	if err != nil {
		return fmt.Errorf("postgres: encode content page revision: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO content_page_revisions
		 (tenant_id, slug, revision, title, document, status, actor_id)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)`,
		page.TenantID, page.Slug, page.Revision, page.Title, encoded, page.Status, actorID,
	); err != nil {
		return fmt.Errorf("postgres: append content page revision: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPage(row rowScanner) (ports.Page, error) {
	var page ports.Page
	var encoded []byte
	var publishedAt pgtype.Timestamptz
	if err := row.Scan(&page.TenantID, &page.Slug, &page.Title, &encoded, &page.Status,
		&page.Revision, &page.CreatedAt, &page.UpdatedAt, &publishedAt); err != nil {
		return ports.Page{}, err
	}
	document, err := decodeDocument(encoded)
	if err != nil {
		return ports.Page{}, fmt.Errorf("decode document: %w", err)
	}
	page.Document = document
	if publishedAt.Valid {
		published := publishedAt.Time
		page.PublishedAt = &published
	}
	return page, nil
}

func scanRevision(row rowScanner) (ports.Revision, error) {
	var revision ports.Revision
	var encoded []byte
	if err := row.Scan(&revision.TenantID, &revision.Slug, &revision.Revision, &revision.Title,
		&encoded, &revision.Status, &revision.ActorID, &revision.CreatedAt); err != nil {
		return ports.Revision{}, err
	}
	document, err := decodeDocument(encoded)
	if err != nil {
		return ports.Revision{}, fmt.Errorf("decode revision document: %w", err)
	}
	revision.Document = document
	return revision, nil
}

func scanSection(row rowScanner) (ports.ReusableSection, error) {
	var section ports.ReusableSection
	var encoded []byte
	if err := row.Scan(&section.TenantID, &section.ID, &section.Name, &encoded, &section.CreatedAt); err != nil {
		return ports.ReusableSection{}, err
	}
	if err := json.Unmarshal(encoded, &section.Component); err != nil {
		return ports.ReusableSection{}, fmt.Errorf("decode section component: %w", err)
	}
	return section, nil
}

func decodeDocument(encoded []byte) (domain.Document, error) {
	var document domain.Document
	if err := json.Unmarshal(encoded, &document); err != nil {
		return domain.Document{}, err
	}
	return document.Normalized()
}

func normalizeAndValidateDraft(tenantID, slug, title string, document domain.Document, actorID string) (domain.Document, error) {
	if err := domain.ValidatePageIdentity(tenantID, slug, title); err != nil {
		return domain.Document{}, err
	}
	if err := validateActor(actorID); err != nil {
		return domain.Document{}, err
	}
	normalized, err := document.Normalized()
	if err != nil {
		return domain.Document{}, err
	}
	if err := normalized.Validate(); err != nil {
		return domain.Document{}, err
	}
	return normalized, nil
}

func validateActor(actorID string) error {
	if len(actorID) == 0 || len(actorID) > 128 {
		return fmt.Errorf("%w: actor id is required and must be at most 128 bytes", domain.ErrInvalidData)
	}
	return nil
}

func validatePageLookup(tenantID, slug string) error {
	return domain.ValidatePageIdentity(tenantID, slug, "x")
}

func validSectionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func validComponentID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

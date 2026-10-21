package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"gogo/internal/features/contentcms/application/ports"
	"gogo/internal/features/contentcms/domain"
)

var _ ports.SiteStore = (*Store)(nil)

// ReadSiteDraft returns the tenant's current draft shell. A tenant without a
// row gets the built-in default at revision 0, which lets the editor create
// the first row with the same optimistic-concurrency contract as a page save.
func (s *Store) ReadSiteDraft(ctx context.Context, tenantID string) (ports.SiteSnapshot, error) {
	if err := validateTenant(tenantID); err != nil {
		return ports.SiteSnapshot{}, err
	}
	snapshot, err := scanSite(s.pool.QueryRow(ctx,
		`SELECT tenant_id, draft_settings, status, revision, updated_at, published_at
		 FROM content_site_settings
		 WHERE tenant_id = $1`,
		tenantID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultSiteSnapshot(tenantID), nil
	}
	if err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: read site draft: %w", err)
	}
	return snapshot, nil
}

// ReadSitePublished returns the shell published for public traffic. A tenant
// that has not published yet gets the built-in default rather than an error;
// the public website must keep serving while the CMS is being configured.
func (s *Store) ReadSitePublished(ctx context.Context, tenantID string) (ports.SiteSnapshot, error) {
	if err := validateTenant(tenantID); err != nil {
		return ports.SiteSnapshot{}, err
	}
	snapshot, err := scanSite(s.pool.QueryRow(ctx,
		`SELECT tenant_id, published_settings, 'published', published_revision, updated_at, published_at
		 FROM content_site_settings
		 WHERE tenant_id = $1 AND published_settings IS NOT NULL`,
		tenantID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return defaultSiteSnapshot(tenantID), nil
	}
	if err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: read published site: %w", err)
	}
	return snapshot, nil
}

// SaveSiteDraft updates only the draft shell. The published pointer is left
// untouched, so autosaving settings never changes what visitors see.
func (s *Store) SaveSiteDraft(ctx context.Context, tenantID string, settings domain.SiteSettings, expectedRevision int64, actorID string) (ports.SiteSnapshot, error) {
	settings = settings.Normalized()
	if err := validateTenant(tenantID); err != nil {
		return ports.SiteSnapshot{}, err
	}
	if err := validateActor(actorID); err != nil {
		return ports.SiteSnapshot{}, err
	}
	if err := settings.Validate(); err != nil {
		return ports.SiteSnapshot{}, err
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: encode site draft: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: begin site draft save: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var snapshot ports.SiteSnapshot
	if expectedRevision == 0 {
		snapshot, err = scanSite(tx.QueryRow(ctx,
			`INSERT INTO content_site_settings (tenant_id, draft_settings, status, revision)
			 VALUES ($1, $2::jsonb, 'draft', 1)
			 ON CONFLICT (tenant_id) DO NOTHING
			 RETURNING tenant_id, draft_settings, status, revision, updated_at, published_at`,
			tenantID, encoded,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			current, selectErr := siteRevision(ctx, tx, tenantID)
			if errors.Is(selectErr, pgx.ErrNoRows) {
				return ports.SiteSnapshot{}, ports.ErrNotFound
			}
			if selectErr != nil {
				return ports.SiteSnapshot{}, selectErr
			}
			return ports.SiteSnapshot{}, &ports.ConflictError{Expected: 0, Current: current}
		}
	} else {
		snapshot, err = scanSite(tx.QueryRow(ctx,
			`UPDATE content_site_settings
			 SET draft_settings = $2::jsonb, status = 'draft', revision = revision + 1, updated_at = now()
			 WHERE tenant_id = $1 AND revision = $3
			 RETURNING tenant_id, draft_settings, status, revision, updated_at, published_at`,
			tenantID, encoded, expectedRevision,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			current, selectErr := siteRevision(ctx, tx, tenantID)
			if errors.Is(selectErr, pgx.ErrNoRows) {
				return ports.SiteSnapshot{}, ports.ErrNotFound
			}
			if selectErr != nil {
				return ports.SiteSnapshot{}, selectErr
			}
			return ports.SiteSnapshot{}, &ports.ConflictError{Expected: expectedRevision, Current: current}
		}
	}
	if err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: save site draft: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: commit site draft save: %w", err)
	}
	return snapshot, nil
}

// PublishSite copies the validated draft into the public pointer. It increments
// the revision, matching the page publication contract, so the editor's next
// save must use the new revision.
func (s *Store) PublishSite(ctx context.Context, tenantID string, expectedRevision int64, actorID string) (ports.SiteSnapshot, error) {
	if err := validateTenant(tenantID); err != nil {
		return ports.SiteSnapshot{}, err
	}
	if err := validateActor(actorID); err != nil {
		return ports.SiteSnapshot{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: begin site publish: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var encoded []byte
	var currentRevision int64
	if err := tx.QueryRow(ctx,
		`SELECT draft_settings, revision FROM content_site_settings
		 WHERE tenant_id = $1 FOR UPDATE`,
		tenantID,
	).Scan(&encoded, &currentRevision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ports.SiteSnapshot{}, ports.ErrNotFound
		}
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: load site draft for publish: %w", err)
	}
	if expectedRevision > 0 && expectedRevision != currentRevision {
		return ports.SiteSnapshot{}, &ports.ConflictError{Expected: expectedRevision, Current: currentRevision}
	}
	var settings domain.SiteSettings
	if err := json.Unmarshal(encoded, &settings); err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: decode site draft for publish: %w", err)
	}
	settings = settings.Normalized()
	if err := settings.Validate(); err != nil {
		return ports.SiteSnapshot{}, err
	}
	canonical, err := json.Marshal(settings)
	if err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: encode site for publish: %w", err)
	}

	snapshot, err := scanSite(tx.QueryRow(ctx,
		`UPDATE content_site_settings
		 SET published_settings = $2::jsonb,
		     status = 'published',
		     revision = revision + 1,
		     published_revision = revision + 1,
		     published_at = now(),
		     updated_at = now()
		 WHERE tenant_id = $1
		 RETURNING tenant_id, published_settings, status, published_revision, updated_at, published_at`,
		tenantID, canonical,
	))
	if err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: publish site: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("postgres: commit site publish: %w", err)
	}
	return snapshot, nil
}

func siteRevision(ctx context.Context, tx pgx.Tx, tenantID string) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx,
		`SELECT revision FROM content_site_settings WHERE tenant_id = $1`,
		tenantID,
	).Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("postgres: read site revision: %w", err)
	}
	return revision, nil
}

func validateTenant(tenantID string) error {
	return domain.ValidatePageIdentity(tenantID, "x", "x")
}

func defaultSiteSnapshot(tenantID string) ports.SiteSnapshot {
	return ports.SiteSnapshot{
		TenantID: tenantID,
		Settings: domain.DefaultSiteSettings(),
		Status:   ports.StatusDraft,
		Revision: 0,
	}
}

func scanSite(row rowScanner) (ports.SiteSnapshot, error) {
	var snapshot ports.SiteSnapshot
	var encoded []byte
	var publishedAt pgtype.Timestamptz
	if err := row.Scan(
		&snapshot.TenantID, &encoded, &snapshot.Status, &snapshot.Revision,
		&snapshot.UpdatedAt, &publishedAt,
	); err != nil {
		return ports.SiteSnapshot{}, err
	}
	if err := json.Unmarshal(encoded, &snapshot.Settings); err != nil {
		return ports.SiteSnapshot{}, fmt.Errorf("decode site settings: %w", err)
	}
	snapshot.Settings = snapshot.Settings.Normalized()
	if publishedAt.Valid {
		published := publishedAt.Time
		snapshot.PublishedAt = &published
	}
	if snapshot.Status == ports.StatusPublished {
		snapshot.PublishedRevision = snapshot.Revision
	}
	return snapshot, nil
}

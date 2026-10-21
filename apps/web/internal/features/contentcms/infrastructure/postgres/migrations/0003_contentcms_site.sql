CREATE TABLE IF NOT EXISTS content_site_settings (
    tenant_id          TEXT PRIMARY KEY,
    draft_settings     JSONB NOT NULL,
    published_settings JSONB,
    status             TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
    revision           BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    published_revision BIGINT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at       TIMESTAMPTZ,
    CHECK ((published_settings IS NULL) = (published_revision IS NULL))
);

CREATE INDEX IF NOT EXISTS content_site_settings_updated_idx
    ON content_site_settings (updated_at DESC);

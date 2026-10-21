CREATE TABLE IF NOT EXISTS content_pages (
    tenant_id        TEXT NOT NULL,
    slug             TEXT NOT NULL,
    title            TEXT NOT NULL,
    draft_document   JSONB NOT NULL,
    published_title  TEXT,
    published_document JSONB,
    status           TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
    revision         BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    published_revision BIGINT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at     TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, slug),
    CHECK ((published_document IS NULL) = (published_title IS NULL))
);

CREATE INDEX IF NOT EXISTS content_pages_tenant_updated_idx
    ON content_pages (tenant_id, updated_at DESC, slug);

CREATE TABLE IF NOT EXISTS content_page_revisions (
    tenant_id    TEXT NOT NULL,
    slug         TEXT NOT NULL,
    revision     BIGINT NOT NULL CHECK (revision > 0),
    title        TEXT NOT NULL,
    document     JSONB NOT NULL,
    status       TEXT NOT NULL CHECK (status IN ('draft', 'published')),
    actor_id     TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, slug, revision),
    FOREIGN KEY (tenant_id, slug) REFERENCES content_pages (tenant_id, slug)
);

CREATE INDEX IF NOT EXISTS content_page_revisions_tenant_page_idx
    ON content_page_revisions (tenant_id, slug, revision DESC);

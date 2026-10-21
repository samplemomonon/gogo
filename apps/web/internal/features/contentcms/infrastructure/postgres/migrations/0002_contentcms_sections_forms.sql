CREATE TABLE IF NOT EXISTS content_sections (
    tenant_id   TEXT NOT NULL,
    id          TEXT NOT NULL,
    name        TEXT NOT NULL,
    component   JSONB NOT NULL,
    actor_id    TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS content_sections_tenant_created_idx
    ON content_sections (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS content_form_submissions (
    id             BIGSERIAL PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    slug           TEXT NOT NULL,
    page_revision  BIGINT NOT NULL,
    component_id   TEXT NOT NULL,
    values         JSONB NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS content_form_submissions_page_idx
    ON content_form_submissions (tenant_id, slug, created_at DESC);

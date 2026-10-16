-- 0001_init: media metadata table, indexes and updated_at trigger.
--
-- This table holds metadata only. The bytes live in the object store under
-- (bucket, object_key); the unique constraint on that pair keeps two records
-- from ever claiming the same object.
CREATE TABLE IF NOT EXISTS media (
    id           BIGSERIAL PRIMARY KEY,
    owner_id     VARCHAR(255) NOT NULL,
    filename     VARCHAR(255) NOT NULL,
    content_type VARCHAR(127) NOT NULL,
    -- Advisory until CompleteUpload confirms what actually landed.
    size_bytes   BIGINT NOT NULL DEFAULT 0,
    -- Null until the upload completes; the store has no ETag before that.
    etag         VARCHAR(255),
    bucket       VARCHAR(63)  NOT NULL,
    object_key   VARCHAR(512) NOT NULL,
    kind         VARCHAR(16)  NOT NULL DEFAULT 'other',
    -- pending until the object is verified in the store, then ready.
    status       VARCHAR(16)  NOT NULL DEFAULT 'pending',
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT media_bucket_object_key_unique UNIQUE (bucket, object_key),
    CONSTRAINT media_size_non_negative CHECK (size_bytes >= 0),
    CONSTRAINT media_status_valid CHECK (status IN ('pending', 'ready')),
    CONSTRAINT media_kind_valid CHECK (kind IN ('image', 'video', 'audio', 'document', 'other'))
);

-- The listing endpoint pages by owner newest first.
CREATE INDEX IF NOT EXISTS idx_media_owner_created_at ON media(owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_media_status ON media(status);
CREATE INDEX IF NOT EXISTS idx_media_owner_status ON media(owner_id, status);

-- Keep updated_at current without relying on the application to remember.
CREATE OR REPLACE FUNCTION media_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS media_set_updated_at ON media;
CREATE TRIGGER media_set_updated_at
    BEFORE UPDATE ON media
    FOR EACH ROW
    EXECUTE FUNCTION media_set_updated_at();

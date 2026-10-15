-- 0001_init: the product read model.
--
-- This is a projection, not a copy. Every row here is derived from the events
-- product-service publishes, and every row can be rebuilt by replaying the topic
-- from the beginning. That is what lets this service own its own schema: it can
-- carry denormalised columns and indexes shaped for browsing that the write side
-- has no reason to maintain.
--
-- The primary key is the product id assigned by product-service. Reusing it means
-- the projection never has to map one id space onto another, and it makes an
-- event's aggregate id directly usable as the row's key.
CREATE TABLE IF NOT EXISTS product_listings (
    product_id      BIGINT       PRIMARY KEY,
    category_id     BIGINT       NOT NULL,
    name            VARCHAR(255) NOT NULL,
    slug            VARCHAR(255) NOT NULL,
    description     TEXT,
    brand           VARCHAR(255),
    -- Minor currency units, matching product-service. The read model does no
    -- arithmetic on money, so this is a faithful copy rather than a
    -- re-interpretation.
    price_cents     BIGINT       NOT NULL,
    currency        CHAR(3)      NOT NULL,
    stock           INTEGER      NOT NULL DEFAULT 0,
    status          VARCHAR(16)  NOT NULL,
    primary_media_id BIGINT,

    -- The product's own timestamps, carried by the event, so the read model
    -- reports when the product was actually created and changed.
    created_at      TIMESTAMPTZ  NOT NULL,
    updated_at      TIMESTAMPTZ  NOT NULL,

    -- When this projection last wrote the row, and which event wrote it. Kept
    -- distinct from updated_at above: a product can be republished without
    -- having changed, and an operator debugging lag needs to tell those apart.
    projected_at    TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_event_id   UUID         NOT NULL,

    CONSTRAINT product_listings_slug_unique UNIQUE (slug),
    CONSTRAINT product_listings_price_non_negative CHECK (price_cents >= 0),
    CONSTRAINT product_listings_stock_non_negative CHECK (stock >= 0),
    CONSTRAINT product_listings_status_valid CHECK (status IN ('draft', 'active', 'archived')),
    CONSTRAINT product_listings_currency_format CHECK (currency ~ '^[A-Z]{3}$')
);

-- The indexes below mirror the filters the read API exposes. This is the whole
-- justification for a separate read service: these are indexes the write path
-- would otherwise have to maintain on every insert and update for the benefit of
-- queries it never serves.
CREATE INDEX IF NOT EXISTS idx_product_listings_category_id
    ON product_listings(category_id);
CREATE INDEX IF NOT EXISTS idx_product_listings_status
    ON product_listings(status);
CREATE INDEX IF NOT EXISTS idx_product_listings_created_at
    ON product_listings(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_product_listings_price_cents
    ON product_listings(price_cents);
-- Supports the free-text filter over name, slug and brand.
CREATE INDEX IF NOT EXISTS idx_product_listings_name_lower
    ON product_listings(LOWER(name));
-- A browse listing that hides drafts reads this constantly, so the partial index
-- is smaller and more cache-resident than a full one on status.
CREATE INDEX IF NOT EXISTS idx_product_listings_active
    ON product_listings(created_at DESC)
    WHERE status = 'active';

-- Variants are projected separately rather than embedded in the product row. A
-- row with a JSONB array of variants would have to be rewritten on every variant
-- change, and the common case is a page of products with no variants at all.
CREATE TABLE IF NOT EXISTS product_listing_variants (
    variant_id  BIGINT       PRIMARY KEY,
    product_id  BIGINT       NOT NULL,
    sku         VARCHAR(64)  NOT NULL,
    name        VARCHAR(255) NOT NULL,
    price_cents BIGINT       NOT NULL,
    currency    CHAR(3)      NOT NULL,
    stock       INTEGER      NOT NULL DEFAULT 0,
    media_id    BIGINT,

    created_at  TIMESTAMPTZ  NOT NULL,
    updated_at  TIMESTAMPTZ  NOT NULL,
    projected_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_event_id UUID        NOT NULL,

    CONSTRAINT product_listing_variants_sku_unique UNIQUE (sku),
    CONSTRAINT product_listing_variants_price_non_negative CHECK (price_cents >= 0),
    CONSTRAINT product_listing_variants_stock_non_negative CHECK (stock >= 0),
    CONSTRAINT product_listing_variants_currency_format CHECK (currency ~ '^[A-Z]{3}$')
);

-- Fetching the variants of one product is the only access pattern.
CREATE INDEX IF NOT EXISTS idx_product_listing_variants_product_id
    ON product_listing_variants(product_id);

-- Processed events record which event ids have been applied, which is what makes
-- the consumer idempotent.
--
-- At-least-once delivery means a redelivered event is normal rather than
-- exceptional: the outbox relay may publish an event and crash before recording
-- that it did. Re-applying it would be harmless because every projection is an
-- upsert, but recording the id lets the consumer skip the write outright, and it
-- turns "is this event applied?" from a guess into a query.
--
-- The primary key is the event id, so the check and the insert are one
-- constraint. Rows are pruned by the maintenance job rather than here: this table
-- grows with the event count, and a lookup only ever consults one id.
CREATE TABLE IF NOT EXISTS processed_events (
    event_id      UUID        PRIMARY KEY,
    event_type    TEXT        NOT NULL,
    aggregate_id  BIGINT      NOT NULL,
    -- The partition and offset the event was read from, which is what makes a
    -- replay auditable: it says exactly which point in the log a row came from.
    topic         TEXT        NOT NULL,
    partition     INTEGER     NOT NULL,
    kafka_offset  BIGINT      NOT NULL,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_processed_events_processed_at
    ON processed_events(processed_at);
CREATE INDEX IF NOT EXISTS idx_processed_events_aggregate
    ON processed_events(event_type, aggregate_id);

-- A consumer applies events in batches, and one that dies mid-batch must not
-- leave a partial write behind. Every projection therefore runs in one
-- transaction together with its processed_events insert, so an event is either
-- fully applied and recorded, or neither.
--
-- The committed offset is bookkeeping for operators, not a source of truth: the
-- consumer group holds the authoritative offset in Kafka. This table answers
-- "how far behind is the read model", which is a question the broker cannot.
CREATE TABLE IF NOT EXISTS consumer_checkpoints (
    consumer_group TEXT        NOT NULL,
    partition      INTEGER     NOT NULL,
    last_offset    BIGINT      NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,

    PRIMARY KEY (consumer_group, partition)
);

-- 0001_init: the product search read model.
--
-- This is a projection, not a copy, and it is shaped for one job: finding
-- products by text. Every row is derived from the events product-service
-- publishes and can be rebuilt by replaying the topic.
--
-- It is a separate database from product_listings on purpose. Browsing and
-- searching are different workloads with different costs. A browse table wants
-- cheap indexes on structured columns; a search table wants a trigram index,
-- which is expensive to maintain on every write. Sharing one table would mean
-- both services pay for both, and neither could evolve on its own schedule.
--
-- The primary key is the product id assigned by product-service, so the
-- projection never has to map one id space onto another.
--
-- pg_trgm is installed here rather than by an init script because it is a
-- trusted extension: as the owner of this database, CREATE EXTENSION is enough
-- and no superuser is involved. Verified against PostgreSQL 17 with pg_trgm 1.6.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS product_search (
    product_id       BIGINT       PRIMARY KEY,
    category_id      BIGINT       NOT NULL,
    name             VARCHAR(255) NOT NULL,
    slug             VARCHAR(255) NOT NULL,
    -- Kept in the document, deliberately kept out of the index. See search_text.
    description      TEXT,
    brand            VARCHAR(255),
    price_cents      BIGINT       NOT NULL,
    currency         CHAR(3)      NOT NULL,
    stock            INTEGER      NOT NULL DEFAULT 0,
    status           VARCHAR(16)  NOT NULL,
    primary_media_id BIGINT,

    -- The product's own timestamps, carried by the event, so the read model
    -- reports when the product was actually created and changed.
    created_at       TIMESTAMPTZ  NOT NULL,
    updated_at       TIMESTAMPTZ  NOT NULL,

    -- When this projection last wrote the row. Kept distinct from updated_at: a
    -- product can be republished without having changed, and an operator
    -- debugging lag needs to tell those apart.
    projected_at     TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_event_id    UUID         NOT NULL,

    -- The searchable text, as one lowercased column.
    --
    -- Three columns or one? Indexing name, brand and slug separately means
    -- three GIN indexes to maintain on every product event, and a query whose
    -- OR branches are combined by the planner have to be indexable in every
    -- branch or the whole plan collapses to a sequential scan. One combined
    -- column is one index, one bitmap, and no such cliff.
    --
    -- A generated column rather than an expression index, because the query then
    -- reads `search_text LIKE ...` with no obligation to reproduce the expression
    -- exactly; a mismatch there fails silently as a sequential scan.
    --
    -- description is excluded on purpose. A trigram index costs roughly one entry
    -- per trigram of indexed text, so long-form marketing copy would dominate the
    -- index while adding little to what a shopper types into a search box. This
    -- matches what the listing service searched before the split. Widening it is
    -- a one-line change here, and the right way to do it for long text is a
    -- tsvector GIN index rather than a trigram one.
    search_text      TEXT         GENERATED ALWAYS AS (
        lower(coalesce(name, '') || ' ' || coalesce(brand, '') || ' ' || coalesce(slug, ''))
    ) STORED,

    CONSTRAINT product_search_price_non_negative CHECK (price_cents >= 0),
    CONSTRAINT product_search_stock_non_negative  CHECK (stock >= 0),
    CONSTRAINT product_search_status_valid
        CHECK (status IN ('draft', 'active', 'archived')),
    CONSTRAINT product_search_currency_format CHECK (currency ~ '^[A-Z]{3}$')
);

-- The trigram index, and the reason this service exists.
--
-- GIN rather than GiST because the indexed text is short, where GIN is both
-- faster to search and smaller.
--
-- gin_pending_list_limit is deliberately left at the 4MB default. Raising it looks
-- appealing - fewer foreground flushes means less latency variance - and it was
-- tried here at 64MB. It made search dramatically worse, and measurably so.
--
-- GIN writes go to a pending list that has to be walked linearly on every search
-- until autovacuum flushes it. With a 64MB limit the list stayed unflushed: after
-- loading 60k products the index was 31MB, a Bitmap Index Scan was costed at
-- 17026 against 2465 for reading the whole 13MB table, and the planner correctly
-- preferred a sequential scan - so the index was never used at all. After one
-- VACUUM flushed the list, the same query cost 38.89 and the index won.
--
-- A larger pending list therefore does not only delay the flush, it raises the
-- cost of every search in the meantime. The default bounds that cost, which is the
-- quantity that matters on the request path.
CREATE INDEX IF NOT EXISTS idx_product_search_text
    ON product_search USING GIN (search_text gin_trgm_ops);

-- Filters that survive the text match are applied to the candidate set, so these
-- are secondary to the trigram index. They still earn their place: a browse-shaped
-- query with no text term, and the count behind a filtered page, both read them.
CREATE INDEX IF NOT EXISTS idx_product_search_category_id
    ON product_search(category_id);
CREATE INDEX IF NOT EXISTS idx_product_search_status
    ON product_search(status);
CREATE INDEX IF NOT EXISTS idx_product_search_price_cents
    ON product_search(price_cents);

-- A storefront hides drafts, and that is the common query, so the partial index
-- is smaller and more cache-resident than a full one on status.
CREATE INDEX IF NOT EXISTS idx_product_search_active
    ON product_search(created_at DESC)
    WHERE status = 'active';

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

-- 0001_init: products, variants, indexes and updated_at triggers.
--
-- category_id and primary_media_id deliberately have no foreign key: they are
-- owned by category-service and media-service, and a cross-service constraint
-- would couple the deploy order of three services. The service validates them
-- over gRPC instead.
CREATE TABLE IF NOT EXISTS products (
    id               BIGSERIAL PRIMARY KEY,
    category_id      BIGINT       NOT NULL,
    name             VARCHAR(255) NOT NULL,
    -- URL-safe, unique so a product has one canonical address.
    slug             VARCHAR(255) NOT NULL,
    description      TEXT,
    brand            VARCHAR(255),
    -- Minor currency units. Never a float: binary floating point cannot hold
    -- decimal currency exactly.
    price_cents      BIGINT       NOT NULL,
    currency         CHAR(3)      NOT NULL DEFAULT 'IDR',
    stock            INTEGER      NOT NULL DEFAULT 0,
    status           VARCHAR(16)  NOT NULL DEFAULT 'draft',
    primary_media_id BIGINT,
    created_at       TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT products_slug_unique UNIQUE (slug),
    CONSTRAINT products_price_non_negative CHECK (price_cents >= 0),
    CONSTRAINT products_stock_non_negative CHECK (stock >= 0),
    CONSTRAINT products_status_valid CHECK (status IN ('draft', 'active', 'archived')),
    CONSTRAINT products_currency_format CHECK (currency ~ '^[A-Z]{3}$')
);

CREATE INDEX IF NOT EXISTS idx_products_category_id ON products(category_id);
CREATE INDEX IF NOT EXISTS idx_products_status ON products(status);
CREATE INDEX IF NOT EXISTS idx_products_created_at ON products(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_products_price_cents ON products(price_cents);
-- Supports the free-text filter over name, slug and brand.
CREATE INDEX IF NOT EXISTS idx_products_name_lower ON products(LOWER(name));

-- Variants are the purchasable options of a product. Deleting the product
-- removes them, since a variant without a product is meaningless.
CREATE TABLE IF NOT EXISTS product_variants (
    id          BIGSERIAL PRIMARY KEY,
    product_id  BIGINT       NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    sku         VARCHAR(64)  NOT NULL,
    name        VARCHAR(255) NOT NULL,
    price_cents BIGINT       NOT NULL,
    currency    CHAR(3)      NOT NULL DEFAULT 'IDR',
    stock       INTEGER      NOT NULL DEFAULT 0,
    media_id    BIGINT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT product_variants_sku_unique UNIQUE (sku),
    CONSTRAINT product_variants_price_non_negative CHECK (price_cents >= 0),
    CONSTRAINT product_variants_stock_non_negative CHECK (stock >= 0),
    CONSTRAINT product_variants_currency_format CHECK (currency ~ '^[A-Z]{3}$')
);

CREATE INDEX IF NOT EXISTS idx_product_variants_product_id ON product_variants(product_id);

-- Keep updated_at current without relying on the application to remember.
CREATE OR REPLACE FUNCTION products_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS products_set_updated_at ON products;
CREATE TRIGGER products_set_updated_at
    BEFORE UPDATE ON products
    FOR EACH ROW
    EXECUTE FUNCTION products_set_updated_at();

CREATE OR REPLACE FUNCTION product_variants_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS product_variants_set_updated_at ON product_variants;
CREATE TRIGGER product_variants_set_updated_at
    BEFORE UPDATE ON product_variants
    FOR EACH ROW
    EXECUTE FUNCTION product_variants_set_updated_at();

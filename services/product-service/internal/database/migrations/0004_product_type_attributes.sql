ALTER TABLE products
    ADD COLUMN IF NOT EXISTS product_type_id TEXT NOT NULL DEFAULT 'general',
    ADD COLUMN IF NOT EXISTS schema_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS attributes JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE products
    ADD CONSTRAINT products_attributes_object CHECK (jsonb_typeof(attributes) = 'object');

CREATE INDEX IF NOT EXISTS idx_products_type ON products(product_type_id, schema_version);

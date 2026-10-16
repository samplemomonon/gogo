-- Categories without tenant_id remain administrator-managed global categories.
-- A non-null tenant_id marks a merchant-owned category.
ALTER TABLE categories ADD COLUMN IF NOT EXISTS tenant_id TEXT;
CREATE INDEX IF NOT EXISTS idx_categories_tenant_parent ON categories(tenant_id, parent_id);

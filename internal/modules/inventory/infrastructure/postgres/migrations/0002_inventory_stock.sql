-- The inventory module's own stock table.
--
-- Stock used to be a column on the catalog's products table, which the storefront
-- read directly. Now that the catalog is gone, the inventory module owns the
-- quantity outright: it keeps its own row per (tenant, product) instead of
-- mutating a column in a table another module created. That removes the last
-- cross-module table write in the monolith — inventory no longer needs the
-- catalog's schema to exist, or its migrations to have run first.
--
-- The product id is TEXT because it is whatever product-service calls the
-- product: gogo's gateway translates the service's int64 id at the adapter
-- boundary, so no column here assumes a numeric key.
--
-- The quantity is the authority for selling decisions (the conditional UPDATE in
-- AdjustStock refuses to drive it below zero). product-service keeps its own
-- stock column for its storefront projection; the two are reconciled by the
-- event feed, not by sharing a row.
CREATE TABLE IF NOT EXISTS inventory_stock (
    tenant_id   TEXT NOT NULL DEFAULT 'default',
    product_id  TEXT NOT NULL,
    quantity    INTEGER NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, product_id)
);

-- Row-Level Security as defense in depth for the tenant boundary: a missed
-- `WHERE tenant_id = $1` is a leak, and RLS blocks it at the database. The
-- application connects as the table owner, so the policies below are bypassed for
-- it; they protect any other role.
ALTER TABLE inventory_stock ENABLE ROW LEVEL SECURITY;

CREATE POLICY inventory_stock_tenant_isolation ON inventory_stock
    USING (tenant_id = current_setting('app.tenant_id', TRUE));

-- The inventory module's append-only stock ledger. The current quantity lives in
-- inventory_stock (migration 0002); this table records how it changed. The unique
-- (tenant_id, reference) constraint makes an adjustment idempotent on a non-empty
-- reference, so a retried request cannot double-apply.
CREATE TABLE IF NOT EXISTS inventory_movements (
    id              TEXT PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    product_id      TEXT NOT NULL,
    kind            TEXT NOT NULL,
    quantity_delta  INTEGER NOT NULL,
    quantity_before INTEGER NOT NULL,
    quantity_after  INTEGER NOT NULL,
    note            TEXT NOT NULL DEFAULT '',
    actor           TEXT NOT NULL DEFAULT '',
    reference       TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS inventory_movements_product_idx
    ON inventory_movements (tenant_id, product_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS inventory_movements_reference_idx
    ON inventory_movements (tenant_id, reference) WHERE reference != '';

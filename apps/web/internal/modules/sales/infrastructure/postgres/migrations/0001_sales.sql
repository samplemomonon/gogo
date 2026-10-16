-- The sales module's transactions and line items. Money is in minor units.
CREATE TABLE IF NOT EXISTS sales_transactions (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL DEFAULT 'default',
    number         TEXT NOT NULL,
    total          BIGINT NOT NULL DEFAULT 0,
    discount       BIGINT NOT NULL DEFAULT 0,
    paid           BIGINT NOT NULL DEFAULT 0,
    change         BIGINT NOT NULL DEFAULT 0,
    payment_method TEXT NOT NULL DEFAULT '',
    cashier        TEXT NOT NULL DEFAULT '',
    employee_id    TEXT NOT NULL DEFAULT '',
    voided_at      TIMESTAMPTZ,
    voided_by      TEXT NOT NULL DEFAULT '',
    void_reason    TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS sales_transactions_tenant_idx
    ON sales_transactions (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS sales_transaction_items (
    id             TEXT PRIMARY KEY,
    transaction_id TEXT NOT NULL,
    tenant_id      TEXT NOT NULL DEFAULT 'default',
    product_id     TEXT NOT NULL DEFAULT '',
    name           TEXT NOT NULL DEFAULT '',
    quantity       INTEGER NOT NULL DEFAULT 0,
    price          BIGINT NOT NULL DEFAULT 0,
    discount       BIGINT NOT NULL DEFAULT 0,
    subtotal       BIGINT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS sales_transaction_items_txn_idx
    ON sales_transaction_items (transaction_id);

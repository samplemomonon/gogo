-- Merchant relationship model: the auth module's authoritative store-ownership
-- and membership data. Products in the product platform carry a merchant_id that
-- references these rows (no foreign key across services — the platform validates
-- the merchant over gRPC). This is the ReBAC provider the identity ADR reserves
-- for a domain with a real owner and lifecycle.

CREATE TABLE IF NOT EXISTS merchants (
    tenant_id        TEXT NOT NULL,
    id               TEXT NOT NULL,
    owner_employee_id TEXT NOT NULL,
    name             TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_merchants_owner ON merchants (tenant_id, owner_employee_id);

-- A membership is one employee's role inside one merchant. The owner (penjual)
-- is a membership with role 'owner'; an invited member (karyawan) has role
-- 'karyawan' and no product authority until the owner grants one.
CREATE TABLE IF NOT EXISTS merchant_memberships (
    tenant_id   TEXT NOT NULL,
    merchant_id TEXT NOT NULL,
    employee_id TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('owner', 'karyawan')),
    granted_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, merchant_id, employee_id),
    FOREIGN KEY (tenant_id, merchant_id) REFERENCES merchants (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_merchant_memberships_employee ON merchant_memberships (tenant_id, employee_id);

-- How an owner adds a karyawan. Carries a one-time token and an expiry.
CREATE TABLE IF NOT EXISTS merchant_invitations (
    tenant_id   TEXT NOT NULL,
    id          TEXT NOT NULL,
    merchant_id TEXT NOT NULL,
    email       TEXT NOT NULL,
    role        TEXT NOT NULL CHECK (role IN ('owner', 'karyawan')),
    status      TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    token       TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    invited_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    accepted_at TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, merchant_id) REFERENCES merchants (tenant_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_merchant_invitations_token ON merchant_invitations (token);

-- Merchant-scoped capability grants: a karyawan's delegated product authority.
CREATE TABLE IF NOT EXISTS merchant_permissions (
    tenant_id      TEXT NOT NULL,
    merchant_id    TEXT NOT NULL,
    employee_id    TEXT NOT NULL,
    permission_key TEXT NOT NULL,
    granted_by     TEXT NOT NULL,
    granted_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at     TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, merchant_id, employee_id, permission_key),
    FOREIGN KEY (tenant_id, merchant_id, employee_id)
        REFERENCES merchant_memberships (tenant_id, merchant_id, employee_id)
);

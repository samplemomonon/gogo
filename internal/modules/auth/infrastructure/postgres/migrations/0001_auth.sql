-- Employees table
CREATE TABLE IF NOT EXISTS employees (
    tenant_id TEXT NOT NULL,
    id TEXT NOT NULL,
    name TEXT NOT NULL,
    email TEXT NOT NULL,
    role TEXT NOT NULL,
    pin_hash TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS idx_employees_tenant_email ON employees (tenant_id, email);

-- Sessions table
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT NOT NULL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    employee_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sessions_tenant_employee ON sessions (tenant_id, employee_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions (expires_at);

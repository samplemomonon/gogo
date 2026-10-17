-- Dynamic, tenant-scoped authorization metadata. Permission keys are a
-- code-owned capability catalog; role membership and grants are mutable data.
CREATE TABLE IF NOT EXISTS authorization_roles (
    tenant_id   TEXT NOT NULL,
    id          TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by  TEXT,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name)
);

-- Supported capabilities are registered by code. No permissions are seeded
-- here, and a role name alone therefore grants nothing.
CREATE TABLE IF NOT EXISTS authorization_permissions (
    permission_key TEXT PRIMARY KEY,
    resource       TEXT NOT NULL,
    action         TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    UNIQUE (resource, action)
);

-- Policy-administration capabilities are part of the auth module's supported
-- permission vocabulary. They are intentionally not assigned to any role here.
INSERT INTO authorization_permissions (permission_key, resource, action, description)
VALUES
    ('authz.role.manage', 'authz.role', 'manage', 'Create, update, and retire tenant roles'),
    ('authz.permission.manage', 'authz.permission', 'manage', 'Grant and revoke supported permissions on tenant roles'),
    ('authz.assignment.manage', 'authz.assignment', 'manage', 'Assign and revoke tenant roles for employees')
ON CONFLICT (permission_key) DO NOTHING;

CREATE TABLE IF NOT EXISTS authorization_role_permissions (
    tenant_id      TEXT NOT NULL,
    grant_id       TEXT NOT NULL,
    role_id        TEXT NOT NULL,
    permission_key TEXT NOT NULL,
    granted_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ,
    revoked_at     TIMESTAMPTZ,
    granted_by     TEXT,
    revoked_by     TEXT,
    grant_reason   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, grant_id),
    FOREIGN KEY (tenant_id, role_id)
        REFERENCES authorization_roles (tenant_id, id),
    FOREIGN KEY (permission_key)
        REFERENCES authorization_permissions (permission_key),
    CHECK (expires_at IS NULL OR expires_at > granted_at),
    CHECK (revoked_at IS NULL OR revoked_at >= granted_at)
);

CREATE TABLE IF NOT EXISTS authorization_employee_roles (
    tenant_id    TEXT NOT NULL,
    assignment_id TEXT NOT NULL,
    employee_id  TEXT NOT NULL,
    role_id      TEXT NOT NULL,
    assigned_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_from   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    assigned_by  TEXT,
    source       TEXT NOT NULL DEFAULT 'admin',
    reason       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_id, assignment_id),
    FOREIGN KEY (tenant_id, employee_id)
        REFERENCES employees (tenant_id, id),
    FOREIGN KEY (tenant_id, role_id)
        REFERENCES authorization_roles (tenant_id, id),
    CHECK (expires_at IS NULL OR expires_at > valid_from),
    CHECK (revoked_at IS NULL OR revoked_at >= assigned_at)
);

-- At most one unretracted assignment per employee/role. A timed-out assignment
-- must be revoked before it can be issued again, preserving a clear history.
CREATE UNIQUE INDEX IF NOT EXISTS authorization_employee_roles_active_idx
    ON authorization_employee_roles (tenant_id, employee_id, role_id)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS authorization_employee_roles_lookup_idx
    ON authorization_employee_roles (tenant_id, employee_id, valid_from, expires_at)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS authorization_role_permissions_lookup_idx
    ON authorization_role_permissions (tenant_id, permission_key, role_id, expires_at)
    WHERE revoked_at IS NULL;

-- Records the one-time bootstrap operation and later PAP changes. The actor is
-- the authenticated PostgreSQL session principal, not an application-supplied
-- username. Business mutation audit remains a separate transactional concern.
CREATE TABLE IF NOT EXISTS authorization_admin_events (
    tenant_id       TEXT NOT NULL,
    id              TEXT NOT NULL,
    action          TEXT NOT NULL,
    subject_id      TEXT NOT NULL DEFAULT '',
    role_id         TEXT NOT NULL DEFAULT '',
    actor_db_user   TEXT NOT NULL,
    change_reference TEXT NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);

CREATE INDEX IF NOT EXISTS authorization_admin_events_tenant_time_idx
    ON authorization_admin_events (tenant_id, occurred_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS authorization_role_permissions_active_idx
    ON authorization_role_permissions (tenant_id, role_id, permission_key)
    WHERE revoked_at IS NULL;

-- Preserve the legacy one-role state as tenant-local role assignments. These
-- names are opaque data, not privileged built-ins. No role_permission rows are
-- created, so they grant no authority until an explicit policy administrator
-- maps supported permission keys to them.
INSERT INTO authorization_roles (tenant_id, id, name, description)
SELECT DISTINCT tenant_id, 'legacy:' || role, role, 'Migrated from employees.role'
FROM employees
WHERE btrim(role) <> ''
ON CONFLICT (tenant_id, id) DO NOTHING;

INSERT INTO authorization_employee_roles (
    tenant_id, assignment_id, employee_id, role_id, assigned_at, valid_from,
    source, reason
)
SELECT tenant_id,
       'legacy:' || length(id)::TEXT || ':' || id || length(role)::TEXT || ':' || role,
       id, 'legacy:' || role,
       created_at, created_at, 'legacy', 'Migrated from employees.role'
FROM employees
WHERE btrim(role) <> ''
ON CONFLICT (tenant_id, assignment_id) DO NOTHING;

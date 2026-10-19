INSERT INTO authorization_permissions (permission_key, resource, action, description)
VALUES ('authz.assignment.review', 'authz.assignment', 'review', 'Read tenant role assignment history for access review')
ON CONFLICT (permission_key) DO NOTHING;

CREATE INDEX IF NOT EXISTS authorization_employee_roles_tenant_review_idx
    ON authorization_employee_roles (tenant_id, assigned_at DESC, assignment_id DESC);

INSERT INTO authorization_permissions (permission_key, resource, action, description)
VALUES ('authz.audit.read', 'authz.audit', 'read', 'Read tenant policy-administration audit history')
ON CONFLICT (permission_key) DO NOTHING;

CREATE INDEX IF NOT EXISTS authorization_admin_events_tenant_review_idx
    ON authorization_admin_events (tenant_id, occurred_at DESC, id DESC);

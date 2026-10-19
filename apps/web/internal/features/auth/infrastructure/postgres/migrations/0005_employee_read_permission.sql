INSERT INTO authorization_permissions (permission_key, resource, action, description)
VALUES ('authz.employee.read', 'authz.employee', 'read', 'Read tenant employee identities for access administration and review')
ON CONFLICT (permission_key) DO NOTHING;

-- Seller-console capabilities are code-owned; tenant policy administrators may
-- compose these identifiers into dynamic roles without inventing new authority.
INSERT INTO authorization_permissions (permission_key, resource, action, description)
VALUES
    ('seller.dashboard.read', 'seller.dashboard', 'read', 'View seller dashboard'),
    ('seller.catalog.read', 'seller.catalog', 'read', 'View product catalog and product types'),
    ('seller.catalog.manage', 'seller.catalog', 'manage', 'Create, update, and archive products'),
    ('seller.category.read', 'seller.category', 'read', 'View categories'),
    ('seller.category.manage', 'seller.category', 'manage', 'Create merchant-local categories'),
    ('seller.inventory.read', 'seller.inventory', 'read', 'View stock and inventory movements'),
    ('seller.inventory.manage', 'seller.inventory', 'manage', 'Adjust inventory'),
    ('seller.sales.read', 'seller.sales', 'read', 'View point-of-sale and transactions'),
    ('seller.sales.checkout', 'seller.sales', 'checkout', 'Create sales transactions'),
    ('seller.sales.void', 'seller.sales', 'void', 'Void sales transactions')
ON CONFLICT (permission_key) DO NOTHING;

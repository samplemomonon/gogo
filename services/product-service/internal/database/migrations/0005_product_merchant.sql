-- 0005_product_merchant: products gain an owning merchant.
--
-- merchant_id references the merchant aggregate in gogo's identity module, so
-- it carries no foreign key here for the same reason category_id does not: a
-- constraint would couple the deploy order of two services and two databases.
-- The service that writes products validates the merchant over gRPC instead.
--
-- The empty default keeps products stored before merchants existed, and
-- products created without a merchant context, representable as unowned.
-- Ownership is fixed at creation and copied into every event, which is what
-- lets gogo's seller console enforce that only the owning merchant's members
-- may change the product.
ALTER TABLE products
    ADD COLUMN IF NOT EXISTS merchant_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_products_merchant_id ON products(merchant_id);
-- The seller console lists one merchant's products newest first; the composite
-- index answers that page without a sort.
CREATE INDEX IF NOT EXISTS idx_products_merchant_created ON products(merchant_id, created_at DESC);

-- 0004_product_merchant: the read model carries product ownership.
--
-- merchant_id is projected from the product.events.v1 snapshot, which
-- product-service stamps onto every product event. The read side stores it so
-- gogo's seller console can list one merchant's products and verify ownership
-- before issuing a write, without a synchronous call on every read.
--
-- The empty default keeps products projected from events that predate the
-- field, and unowned products, representable.
ALTER TABLE product_listings
    ADD COLUMN IF NOT EXISTS merchant_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_product_listings_merchant_id ON product_listings(merchant_id);
-- The seller console lists one merchant's products newest first; the composite
-- index answers that page without a sort.
CREATE INDEX IF NOT EXISTS idx_product_listings_merchant_created ON product_listings(merchant_id, created_at DESC);

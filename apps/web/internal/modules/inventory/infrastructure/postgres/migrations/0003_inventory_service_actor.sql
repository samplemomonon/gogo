-- Keep the human subject and the service performing delegated work distinct.
ALTER TABLE inventory_movements
    ADD COLUMN IF NOT EXISTS service_actor TEXT NOT NULL DEFAULT '';

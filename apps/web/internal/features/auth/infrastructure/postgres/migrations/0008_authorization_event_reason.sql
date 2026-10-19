ALTER TABLE authorization_admin_events
    ADD COLUMN IF NOT EXISTS reason TEXT NOT NULL DEFAULT '';

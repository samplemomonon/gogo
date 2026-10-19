ALTER TABLE authorization_admin_events
    ADD COLUMN IF NOT EXISTS actor_employee_id TEXT NOT NULL DEFAULT '';

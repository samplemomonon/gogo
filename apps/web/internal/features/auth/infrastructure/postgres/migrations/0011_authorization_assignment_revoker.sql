-- authorization_employee_roles was missing the revoker its sibling
-- authorization_role_permissions already carries.
--
-- The repository writes revoked_by when it closes out an expired assignment
-- (to regrant the same role) and when a policy administrator revokes one:
--
--   UPDATE authorization_employee_roles SET revoked_at=now(), revoked_by=$n ...
--
-- The column was never created, so both paths failed with
-- "column revoked_by of relation authorization_employee_roles does not exist"
-- (SQLSTATE 42703) the first time a tenant exercised AssignRole or
-- RevokeRoleAssignment against a real database. The unit tests never reached a
-- database, so the PAP RPCs shipped broken.
--
-- This adds the column, nullable and with no backfill: an assignment revoked
-- before this migration has no recorded revoker, which is the honest value.
ALTER TABLE authorization_employee_roles
    ADD COLUMN IF NOT EXISTS revoked_by TEXT;

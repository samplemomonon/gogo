-- merchant_memberships was created in 0010 with a foreign key to merchants but
-- none to employees, unlike its sibling authorization_employee_roles, which
-- carries FOREIGN KEY (tenant_id, employee_id) REFERENCES employees.
--
-- The consequence was a dangling relationship: a membership could outlive the
-- employee it names, because nothing in the database prevented removing that
-- employee. Every merchant PDP query joins the membership to the employee, so a
-- dangling row is an unreachable grant rather than a loud failure.
--
-- Existing rows already reference live employees (the only writers are
-- OpenMerchant, which resolves the owner from a live session, and
-- AcceptInvitation, which resolves the accepting employee the same way), so
-- this validates cleanly on an already-migrated database.
ALTER TABLE merchant_memberships
    ADD CONSTRAINT merchant_memberships_employee_fkey
    FOREIGN KEY (tenant_id, employee_id)
    REFERENCES employees (tenant_id, id);

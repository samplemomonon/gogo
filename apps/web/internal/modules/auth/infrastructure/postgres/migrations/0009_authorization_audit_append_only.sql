CREATE OR REPLACE FUNCTION deny_authorization_admin_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'authorization_admin_events is append-only'
        USING ERRCODE = '55000';
    RETURN NULL;
END;
$$;

CREATE TRIGGER authorization_admin_events_no_update
    BEFORE UPDATE ON authorization_admin_events
    FOR EACH ROW EXECUTE FUNCTION deny_authorization_admin_event_mutation();

CREATE TRIGGER authorization_admin_events_no_delete
    BEFORE DELETE ON authorization_admin_events
    FOR EACH ROW EXECUTE FUNCTION deny_authorization_admin_event_mutation();

CREATE TRIGGER authorization_admin_events_no_truncate
    BEFORE TRUNCATE ON authorization_admin_events
    FOR EACH STATEMENT EXECUTE FUNCTION deny_authorization_admin_event_mutation();

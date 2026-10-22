-- +goose Up
ALTER TABLE runner_grants ADD COLUMN encrypted_token bytea;
ALTER TABLE provider_installations
    ADD COLUMN workload_state text NOT NULL DEFAULT 'pending' CHECK (workload_state IN ('pending','starting','ready','erasing','erased','failed')),
    ADD COLUMN workload_error text NOT NULL DEFAULT '' CHECK (workload_error IN ('','capacity','manual_grant','cluster_unavailable','resource_conflict','workload_pending','workload_failed'));

-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_controller') THEN
        GRANT SELECT ON provider_accounts TO camp_controller;
        GRANT UPDATE(revision) ON provider_accounts TO camp_controller;
        GRANT SELECT(id,account_id,owner_user_id,location,state,epoch,credential_expires_at,workload_state,workload_error)
            ON provider_installations TO camp_controller;
        GRANT UPDATE(state,workload_state,workload_error,updated_at) ON provider_installations TO camp_controller;
        GRANT SELECT,INSERT,UPDATE ON runner_grants TO camp_controller;
        GRANT SELECT(installation_id,state,expires_at) ON provider_login_attempts TO camp_controller;
        GRANT SELECT ON goose_db_version TO camp_controller;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping runner provisioning state'; END $$;
-- +goose StatementEnd

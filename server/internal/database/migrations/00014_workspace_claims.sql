-- +goose Up
ALTER TABLE ticket_workspaces DROP CONSTRAINT ticket_workspaces_state_check;
ALTER TABLE ticket_workspaces ADD CONSTRAINT ticket_workspaces_state_check CHECK (state IN ('queued','preparing','ready','failed','cancelled'));
ALTER TABLE ticket_workspaces
    ADD COLUMN installation_page integer NOT NULL DEFAULT 1 CHECK (installation_page BETWEEN 1 AND 10000),
    ADD COLUMN repository_page integer NOT NULL DEFAULT 1 CHECK (repository_page BETWEEN 1 AND 10000),
    ADD COLUMN claim_id uuid,
    ADD COLUMN claim_grant_id uuid REFERENCES runner_grants(id),
    ADD COLUMN lease_until timestamptz,
    ADD COLUMN prepare_deadline timestamptz,
    ADD COLUMN token_state text NOT NULL DEFAULT '' CHECK (token_state IN ('','issuing','ready','closed','failed')),
    ADD COLUMN encrypted_read_token bytea,
    ADD COLUMN read_token_expires_at timestamptz,
    ADD COLUMN read_revocation_id uuid,
    ADD COLUMN result_hash bytea CHECK (result_hash IS NULL OR octet_length(result_hash)=32),
    ADD COLUMN failure_code text NOT NULL DEFAULT '' CHECK (failure_code IN ('','lease_expired','authority_changed','git_grant_failed','checkout_failed','checkout_unsafe','checkout_too_large','checkout_cancelled')),
    ADD CONSTRAINT workspace_claim_complete CHECK ((claim_id IS NULL AND claim_grant_id IS NULL AND lease_until IS NULL AND prepare_deadline IS NULL) OR (claim_id IS NOT NULL AND claim_grant_id IS NOT NULL AND lease_until IS NOT NULL AND prepare_deadline IS NOT NULL)),
    ADD CONSTRAINT workspace_token_complete CHECK ((token_state='ready' AND encrypted_read_token IS NOT NULL AND read_token_expires_at IS NOT NULL AND read_revocation_id IS NOT NULL) OR (token_state<>'ready' AND encrypted_read_token IS NULL));
DROP INDEX ticket_workspace_pending;
CREATE UNIQUE INDEX ticket_workspace_pending ON ticket_workspaces(session_id) WHERE state IN ('queued','preparing','ready');
CREATE UNIQUE INDEX ticket_workspace_active_installation ON ticket_workspaces(provider_installation_id) WHERE state IN ('preparing','ready');
CREATE INDEX ticket_workspace_lease ON ticket_workspaces(lease_until) WHERE state='preparing';
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_controller') THEN
        GRANT SELECT(provider_installation_id,state,lease_until) ON ticket_workspaces TO camp_controller;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping workspace claims'; END $$;
-- +goose StatementEnd

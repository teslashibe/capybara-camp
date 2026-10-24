-- +goose Up
ALTER TABLE sessions
    ADD COLUMN activity_revision bigint NOT NULL DEFAULT 1 CHECK (activity_revision > 0),
    ADD COLUMN execution_location text NOT NULL DEFAULT 'local' CHECK (execution_location IN ('local','cloud')),
    ADD COLUMN execution_epoch bigint NOT NULL DEFAULT 1 CHECK (execution_epoch > 0),
    ADD COLUMN execution_workspace_id uuid REFERENCES ticket_workspaces(id),
    ADD COLUMN execution_permissions jsonb NOT NULL DEFAULT '{}',
    ADD CONSTRAINT session_execution_target CHECK ((execution_location='local' AND execution_workspace_id IS NULL AND execution_permissions='{}'::jsonb) OR (execution_location='cloud' AND execution_workspace_id IS NOT NULL));

ALTER TABLE prompt_requests
    ADD COLUMN execution_location text NOT NULL DEFAULT 'local' CHECK (execution_location IN ('local','cloud')),
    ADD COLUMN execution_epoch bigint NOT NULL DEFAULT 1 CHECK (execution_epoch > 0),
    ADD COLUMN execution_workspace_id uuid REFERENCES ticket_workspaces(id),
    ADD COLUMN execution_permissions jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN execution_mode text NOT NULL DEFAULT 'chat' CHECK (execution_mode IN ('chat','planning','implementation')),
    ADD COLUMN runner_grant_id uuid REFERENCES runner_grants(id),
    ADD CONSTRAINT request_execution_target CHECK ((execution_location='local' AND execution_workspace_id IS NULL AND runner_grant_id IS NULL) OR (execution_location='cloud' AND execution_workspace_id IS NOT NULL));
CREATE INDEX cloud_request_queue ON prompt_requests(execution_workspace_id,created_at,id) WHERE execution_location='cloud' AND state='queued';
CREATE INDEX request_session_state ON prompt_requests(session_id,state);
CREATE UNIQUE INDEX cloud_request_claim ON prompt_requests(runner_grant_id,lease_token) WHERE runner_grant_id IS NOT NULL AND lease_token IS NOT NULL;
UPDATE prompt_requests SET execution_mode='planning' WHERE kind='planning';

CREATE VIEW runner_execution_leases WITH (security_barrier=true) AS
SELECT w.provider_installation_id,r.lease_until FROM prompt_requests r
JOIN ticket_workspaces w ON w.id=r.execution_workspace_id
WHERE r.execution_location='cloud' AND r.state IN ('claimed','started','cancel_requested');
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_controller') THEN
        GRANT SELECT ON runner_execution_leases TO camp_controller;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping execution assignments'; END $$;
-- +goose StatementEnd

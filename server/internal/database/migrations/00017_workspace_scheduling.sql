-- +goose Up
DROP INDEX ticket_workspace_active_installation;
CREATE UNIQUE INDEX ticket_workspace_active_installation ON ticket_workspaces(provider_installation_id) WHERE state='preparing';
CREATE INDEX ticket_workspace_schedule ON ticket_workspaces(provider_installation_id,provider_epoch,state,created_at,id);
CREATE INDEX request_workspace_schedule ON prompt_requests(execution_workspace_id,state,created_at,id) WHERE execution_workspace_id IS NOT NULL;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping multi-ticket workspace scheduling'; END $$;
-- +goose StatementEnd

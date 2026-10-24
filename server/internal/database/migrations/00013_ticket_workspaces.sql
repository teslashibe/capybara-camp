-- +goose Up
CREATE TABLE ticket_workspaces (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL,
    session_id uuid NOT NULL,
    project_id uuid NOT NULL,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    provider_account_id uuid NOT NULL,
    provider_installation_id uuid NOT NULL,
    provider_epoch bigint NOT NULL CHECK (provider_epoch > 0),
    git_connection_id uuid NOT NULL,
    git_epoch bigint NOT NULL CHECK (git_epoch > 0),
    github_user_id bigint NOT NULL CHECK (github_user_id > 0),
    github_login text NOT NULL,
    github_installation_id bigint NOT NULL CHECK (github_installation_id > 0),
    repository_id bigint NOT NULL CHECK (repository_id > 0),
    repository_name text NOT NULL CHECK (length(repository_name) BETWEEN 3 AND 140),
    requested_ref text NOT NULL CHECK (length(requested_ref) BETWEEN 1 AND 512),
    base_commit text NOT NULL CHECK (base_commit ~ '^[a-f0-9]{40}$'),
    branch text NOT NULL UNIQUE,
    purpose text NOT NULL CHECK (purpose IN ('planning','implementation')),
    source_revision bigint NOT NULL CHECK (source_revision > 0),
    project_revision bigint NOT NULL CHECK (project_revision > 0),
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','cancelled')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id),
    FOREIGN KEY(camp_id,project_id) REFERENCES projects(camp_id,id),
    FOREIGN KEY(provider_installation_id,provider_account_id,owner_user_id)
        REFERENCES provider_installations(id,account_id,owner_user_id),
    FOREIGN KEY(git_connection_id,owner_user_id) REFERENCES git_connections(id,owner_user_id)
);
CREATE UNIQUE INDEX ticket_workspace_pending ON ticket_workspaces(session_id) WHERE state='queued';
CREATE INDEX ticket_workspace_owner ON ticket_workspaces(owner_user_id,created_at,id);
CREATE INDEX ticket_workspace_installation ON ticket_workspaces(provider_installation_id) WHERE state='queued';
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON ticket_workspaces TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping ticket workspace history'; END $$;
-- +goose StatementEnd

-- +goose Up
CREATE TABLE workspace_export_git_grants (
    export_id uuid NOT NULL REFERENCES workspace_exports(id) ON DELETE CASCADE,
    device_id uuid NOT NULL REFERENCES devices(id),
    state text NOT NULL DEFAULT 'issuing' CHECK(state IN ('issuing','ready','closed')),
    encrypted_token bytea,
    expires_at timestamptz,
    revocation_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    next_check_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(export_id,device_id),
    CHECK((state='ready') = (encrypted_token IS NOT NULL)),
    CHECK(state<>'ready' OR (expires_at IS NOT NULL AND revocation_id IS NOT NULL))
);
CREATE INDEX workspace_export_git_grants_reap ON workspace_export_git_grants(next_check_at) WHERE state<>'closed';
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON workspace_export_git_grants TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    LOCK TABLE workspace_export_git_grants IN ACCESS EXCLUSIVE MODE;
    IF EXISTS(SELECT 1 FROM workspace_export_git_grants) THEN
        RAISE EXCEPTION 'Restore a compatible backup instead of discarding retained export Git grants';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE workspace_export_git_grants;

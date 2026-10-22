-- +goose Up
CREATE TABLE workspace_exports (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL,
    session_id uuid NOT NULL,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    device_id uuid NOT NULL REFERENCES devices(id),
    workspace_id uuid NOT NULL REFERENCES ticket_workspaces(id),
    workspace_revision bigint NOT NULL CHECK(workspace_revision > 0),
    source_epoch bigint NOT NULL CHECK(source_epoch > 0),
    source_revision bigint NOT NULL CHECK(source_revision > 0),
    transcript_revision bigint NOT NULL CHECK(transcript_revision >= 0),
    permission_revision bigint NOT NULL CHECK(permission_revision > 0),
    kind text NOT NULL CHECK(kind IN ('inspect','capture')),
    paths jsonb NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','claimed','ready','failed','cancelled','expired')),
    claim_id uuid,
    grant_id uuid REFERENCES runner_grants(id),
    result_sha256 text NOT NULL DEFAULT '',
    result_bytes integer NOT NULL DEFAULT 0 CHECK(result_bytes BETWEEN 0 AND 48234496),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now()+interval '10 minutes',
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id),
    CHECK((claim_id IS NULL) = (grant_id IS NULL)),
    CHECK(state NOT IN ('claimed','ready') OR claim_id IS NOT NULL),
    CHECK((result_bytes=0 AND result_sha256='') OR (result_bytes>0 AND result_sha256 ~ '^[a-f0-9]{64}$'))
);
CREATE INDEX workspace_export_owner ON workspace_exports(owner_user_id,created_at DESC,id);
CREATE INDEX workspace_export_pending ON workspace_exports(workspace_id,created_at,id) WHERE state IN ('pending','claimed');
CREATE TABLE workspace_export_chunks (
    export_id uuid NOT NULL REFERENCES workspace_exports(id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK(ordinal BETWEEN 0 AND 183),
    sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),
    encrypted_data bytea NOT NULL,
    PRIMARY KEY(export_id,ordinal)
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON workspace_exports,workspace_export_chunks TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    LOCK TABLE workspace_exports,workspace_export_chunks IN ACCESS EXCLUSIVE MODE;
    IF EXISTS(SELECT 1 FROM workspace_exports) THEN
        RAISE EXCEPTION 'Restore a compatible backup instead of dropping retained workspace exports';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE workspace_export_chunks;
DROP TABLE workspace_exports;

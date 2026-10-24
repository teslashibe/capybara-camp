-- +goose Up
CREATE TABLE session_transfers (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL,
    session_id uuid NOT NULL,
    owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_location text NOT NULL CHECK (source_location IN ('local','cloud')),
    source_workspace_id uuid,
    source_epoch bigint NOT NULL CHECK (source_epoch > 0),
    source_revision bigint NOT NULL CHECK (source_revision > 0),
    source_transcript_revision bigint NOT NULL CHECK (source_transcript_revision >= 0),
    source_permission_revision bigint NOT NULL CHECK (source_permission_revision > 0),
    target_location text NOT NULL CHECK (target_location IN ('local','cloud')),
    target_account_id uuid REFERENCES provider_accounts(id) ON DELETE CASCADE,
    target_account_epoch bigint,
    target_device_id uuid REFERENCES devices(id) ON DELETE CASCADE,
    payload_sha256 text NOT NULL CHECK (payload_sha256 ~ '^[a-f0-9]{64}$'),
    review_sha256 text NOT NULL CHECK (review_sha256 ~ '^[a-f0-9]{64}$'),
    payload_bytes integer NOT NULL CHECK (payload_bytes BETWEEN 2 AND 48234496),
    uploaded_bytes integer NOT NULL DEFAULT 0 CHECK (uploaded_bytes >= 0 AND uploaded_bytes <= payload_bytes),
    state text NOT NULL DEFAULT 'uploading' CHECK (state IN ('uploading','ready','cancelled','expired')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id) ON DELETE CASCADE,
    CHECK ((target_location='local' AND target_device_id IS NOT NULL AND target_account_id IS NULL AND target_account_epoch IS NULL)
        OR (target_location='cloud' AND target_device_id IS NULL AND target_account_id IS NOT NULL AND target_account_epoch>0)),
    CHECK ((source_location='local' AND source_workspace_id IS NULL) OR (source_location='cloud' AND source_workspace_id IS NOT NULL))
);
CREATE INDEX session_transfer_owner ON session_transfers(owner_user_id,state,expires_at);
CREATE INDEX session_transfer_session ON session_transfers(camp_id,session_id,created_at DESC,id);
CREATE TABLE transfer_chunks (
    transfer_id uuid NOT NULL REFERENCES session_transfers(id) ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 183),
    sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    size integer NOT NULL CHECK (size BETWEEN 1 AND 262144),
    encrypted_data bytea NOT NULL CHECK (octet_length(encrypted_data) BETWEEN 29 AND 262208),
    PRIMARY KEY(transfer_id,ordinal)
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON session_transfers,transfer_chunks TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping transfer data'; END $$;
-- +goose StatementEnd

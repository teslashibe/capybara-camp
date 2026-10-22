-- +goose Up
CREATE TABLE transfer_handoffs (
    id uuid PRIMARY KEY,
    transfer_id uuid NOT NULL REFERENCES session_transfers(id),
    camp_id uuid NOT NULL,
    session_id uuid NOT NULL,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    source_device_id uuid NOT NULL REFERENCES devices(id),
    workspace_id uuid NOT NULL REFERENCES ticket_workspaces(id),
    workspace_revision bigint NOT NULL CHECK (workspace_revision > 0),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','claimed','acknowledged','released','invalidated','expired','committed')),
    claim_id uuid,
    claim_device_id uuid REFERENCES devices(id),
    claim_grant_id uuid REFERENCES runner_grants(id),
    ack_hash bytea CHECK (ack_hash IS NULL OR octet_length(ack_hash)=32),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    expires_at timestamptz NOT NULL DEFAULT now()+interval '5 minutes',
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id),
    CHECK ((claim_id IS NULL AND claim_device_id IS NULL AND claim_grant_id IS NULL)
        OR (claim_id IS NOT NULL AND ((claim_device_id IS NOT NULL)::int+(claim_grant_id IS NOT NULL)::int)=1)),
    CHECK (state NOT IN ('claimed','acknowledged','committed') OR claim_id IS NOT NULL),
    CHECK (state NOT IN ('acknowledged','committed') OR ack_hash IS NOT NULL)
);
CREATE UNIQUE INDEX transfer_handoff_active_session ON transfer_handoffs(session_id)
    WHERE state IN ('pending','claimed','acknowledged');
CREATE INDEX transfer_handoff_owner ON transfer_handoffs(owner_user_id,created_at DESC,id);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON transfer_handoffs TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping transfer handoff records'; END $$;
-- +goose StatementEnd

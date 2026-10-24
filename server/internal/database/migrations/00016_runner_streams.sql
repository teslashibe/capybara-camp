-- +goose Up
ALTER TABLE prompt_requests
    ADD COLUMN context_revision bigint NOT NULL DEFAULT 0,
    ADD COLUMN stream_seq bigint NOT NULL DEFAULT 0,
    ADD COLUMN stream_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN stream_text_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN stream_published boolean NOT NULL DEFAULT false;
UPDATE prompt_requests r SET context_revision=s.transcript_revision FROM sessions s WHERE s.id=r.session_id AND r.state IN ('queued','pending_approval');
CREATE TABLE request_stream_events (
    request_id uuid NOT NULL REFERENCES prompt_requests(id),
    seq bigint NOT NULL CHECK (seq>0),
    payload jsonb NOT NULL,
    payload_hash bytea NOT NULL CHECK (octet_length(payload_hash)=32),
    PRIMARY KEY(request_id,seq)
);
CREATE TABLE workspace_runtime (
    workspace_id uuid PRIMARY KEY REFERENCES ticket_workspaces(id),
    provider_session_id text NOT NULL CHECK (length(provider_session_id) BETWEEN 1 AND 200)
);
CREATE TABLE request_controls (
    id uuid PRIMARY KEY,
    request_id uuid NOT NULL REFERENCES prompt_requests(id),
    approval_seq bigint,
    provider_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('steer','approval')),
    encrypted_input bytea,
    input_hash bytea NOT NULL CHECK (octet_length(input_hash)=32),
    accepted_text text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','resolved')),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX request_control_approval ON request_controls(request_id,approval_seq) WHERE approval_seq IS NOT NULL;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON request_stream_events,workspace_runtime,request_controls TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping runner streams'; END $$;
-- +goose StatementEnd

-- +goose Up
ALTER TABLE sessions ADD COLUMN permission_revision bigint NOT NULL DEFAULT 1;
CREATE TABLE prompt_requests (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL,
    session_id uuid NOT NULL,
    requester_user_id uuid NOT NULL REFERENCES users(id),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    owner_device_id uuid NOT NULL REFERENCES devices(id),
    permission_revision bigint NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    prompt text NOT NULL CHECK (length(prompt) BETWEEN 1 AND 65536),
    model text NOT NULL,
    effort text NOT NULL,
    speed text NOT NULL,
    state text NOT NULL CHECK (state IN ('pending_approval','queued','claimed','started','cancel_requested','outcome_unknown','completed','failed','cancelled')),
    lease_token uuid,
    lease_until timestamptz,
    started_at timestamptz,
    finished_at timestamptz,
    outcome_evidence text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id)
);
CREATE UNIQUE INDEX one_active_request_per_session ON prompt_requests(session_id)
 WHERE state IN ('claimed','started','cancel_requested','outcome_unknown');
CREATE INDEX request_queue ON prompt_requests(camp_id,owner_device_id,created_at) WHERE state='queued';
CREATE INDEX request_history ON prompt_requests(camp_id,created_at DESC,id);
CREATE INDEX request_leases ON prompt_requests(lease_until) WHERE state IN ('claimed','started','cancel_requested');
CREATE TABLE request_sources (
    request_id uuid NOT NULL REFERENCES prompt_requests(id),
    session_id uuid NOT NULL REFERENCES sessions(id),
    transcript_revision bigint NOT NULL,
    permission_revision bigint NOT NULL,
    PRIMARY KEY(request_id,session_id)
);
CREATE INDEX request_sources_session ON request_sources(session_id,request_id);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON prompt_requests,request_sources TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of deleting request history'; END $$;
-- +goose StatementEnd

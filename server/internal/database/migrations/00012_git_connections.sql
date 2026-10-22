-- +goose Up
CREATE TABLE git_connections (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    github_user_id bigint CHECK (github_user_id > 0),
    github_login text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','connected','needs_login','disconnected')),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    epoch bigint NOT NULL DEFAULT 1 CHECK (epoch > 0),
    encrypted_credentials bytea,
    access_expires_at timestamptz,
    refresh_claim_id uuid,
    refresh_started_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(owner_user_id,github_user_id),
    UNIQUE(id,owner_user_id),
    CHECK ((state='connected' AND encrypted_credentials IS NOT NULL AND github_user_id IS NOT NULL) OR (state<>'connected' AND encrypted_credentials IS NULL)),
    CHECK ((refresh_claim_id IS NULL) = (refresh_started_at IS NULL))
);
CREATE INDEX git_connections_owner ON git_connections(owner_user_id,created_at,id);
CREATE TABLE git_logins (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    connection_epoch bigint NOT NULL,
    state_hash bytea NOT NULL UNIQUE CHECK (octet_length(state_hash)=32),
    encrypted_context bytea,
    state text NOT NULL DEFAULT 'waiting' CHECK (state IN ('waiting','exchanging','connected','cancelled','expired','failed')),
    failure_code text NOT NULL DEFAULT '' CHECK (failure_code IN ('','provider_rejected','identity_mismatch','connection_changed','exchange_unknown')),
    claim_id uuid,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(connection_id,owner_user_id) REFERENCES git_connections(id,owner_user_id)
);
CREATE UNIQUE INDEX git_login_active ON git_logins(connection_id) WHERE state IN ('waiting','exchanging');
CREATE TABLE git_revocations (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    encrypted_credentials bytea NOT NULL,
    not_before timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    claim_id uuid,
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX git_revocations_due ON git_revocations(not_before);
CREATE TABLE git_webhook_deliveries (
    id uuid PRIMARY KEY,
    payload_hash bytea NOT NULL CHECK (octet_length(payload_hash)=32),
    received_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON git_connections,git_logins,git_revocations,git_webhook_deliveries TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping Git credentials'; END $$;
-- +goose StatementEnd

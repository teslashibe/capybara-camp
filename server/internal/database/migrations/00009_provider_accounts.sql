-- +goose Up
CREATE TABLE provider_accounts (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    provider text NOT NULL CHECK (provider IN ('Codex','Claude')),
    label text NOT NULL CHECK (length(label) BETWEEN 1 AND 120),
    verified_subject text CHECK (length(verified_subject) BETWEEN 1 AND 256),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(id,owner_user_id)
);
CREATE INDEX provider_accounts_owner ON provider_accounts(owner_user_id,created_at,id);
CREATE TABLE provider_installations (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL UNIQUE,
    owner_user_id uuid NOT NULL,
    location text NOT NULL CHECK (location='cloud'),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','connected','needs_login','disconnecting','disconnected')),
    epoch bigint NOT NULL DEFAULT 1 CHECK (epoch > 0),
    credential_revision bigint NOT NULL DEFAULT 0 CHECK (credential_revision >= 0),
    encrypted_credentials bytea,
    credential_expires_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(account_id,owner_user_id) REFERENCES provider_accounts(id,owner_user_id),
    UNIQUE(id,account_id,owner_user_id),
    CHECK ((state='connected' AND encrypted_credentials IS NOT NULL) OR (state<>'connected' AND encrypted_credentials IS NULL))
);
CREATE TABLE runner_grants (
    id uuid PRIMARY KEY,
    installation_id uuid NOT NULL REFERENCES provider_installations(id),
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
    epoch bigint NOT NULL CHECK (epoch > 0),
    cleanup_only boolean NOT NULL DEFAULT false,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX runner_grants_installation ON runner_grants(installation_id) WHERE revoked_at IS NULL;
CREATE TABLE provider_login_attempts (
    id uuid PRIMARY KEY,
    installation_id uuid NOT NULL REFERENCES provider_installations(id),
    grant_id uuid NOT NULL REFERENCES runner_grants(id),
    epoch bigint NOT NULL,
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','waiting','connected','failed','cancelled','expired')),
    claim_id uuid,
    result_hash bytea CHECK (result_hash IS NULL OR octet_length(result_hash)=32),
    failure_code text NOT NULL DEFAULT '' CHECK (failure_code IN ('','provider_rejected','identity_mismatch','runner_replaced')),
    encrypted_challenge bytea,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX provider_login_active ON provider_login_attempts(installation_id) WHERE state IN ('queued','running','waiting');
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON provider_accounts,provider_installations,runner_grants,provider_login_attempts TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping provider accounts'; END $$;
-- +goose StatementEnd

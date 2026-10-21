-- +goose Up
CREATE TABLE users (
    id uuid PRIMARY KEY,
    email text NOT NULL UNIQUE,
    display_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE auth_challenges (
    email text PRIMARY KEY,
    code_hash bytea NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    sent_at timestamptz NOT NULL DEFAULT now(),
    window_start timestamptz NOT NULL DEFAULT now(),
    sent_count integer NOT NULL DEFAULT 1
);
CREATE TABLE auth_limits (
    key_hash bytea PRIMARY KEY,
    window_start timestamptz NOT NULL,
    hits integer NOT NULL
);
CREATE TABLE devices (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    token_hash bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX devices_user ON devices(user_id);
CREATE TABLE camps (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    event_cursor bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE memberships (
    camp_id uuid NOT NULL REFERENCES camps(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id),
    role text NOT NULL CHECK (role IN ('owner','editor','viewer')),
    PRIMARY KEY(camp_id,user_id)
);
CREATE INDEX memberships_user ON memberships(user_id);
CREATE TABLE invitations (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL REFERENCES camps(id) ON DELETE CASCADE,
    email text NOT NULL,
    role text NOT NULL CHECK (role IN ('editor','viewer')),
    token_hash bytea NOT NULL UNIQUE,
    created_by uuid NOT NULL REFERENCES users(id),
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz
);
CREATE TABLE projects (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL REFERENCES camps(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    UNIQUE(camp_id,id)
);
CREATE TABLE events (
    camp_id uuid NOT NULL REFERENCES camps(id) ON DELETE CASCADE,
    cursor bigint NOT NULL,
    kind text NOT NULL,
    actor_id uuid NOT NULL REFERENCES users(id),
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(camp_id,cursor)
);
CREATE TABLE command_results (
    user_id uuid NOT NULL REFERENCES users(id),
    request_id uuid NOT NULL,
    action text NOT NULL,
    payload_hash bytea NOT NULL,
    encrypted_result bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(user_id,request_id)
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON users,auth_challenges,auth_limits,devices,
            camps,memberships,invitations,projects,events,command_results TO camp_runtime;
        GRANT SELECT ON goose_db_version TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Initial migration intentionally has no destructive automatic rollback.
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a backup instead of dropping Camp data'; END $$;
-- +goose StatementEnd

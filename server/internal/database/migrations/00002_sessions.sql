-- +goose Up
CREATE TABLE profiles (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    owner_device_id uuid NOT NULL REFERENCES devices(id),
    provider text NOT NULL CHECK (provider IN ('Codex','Claude')),
    local_id text NOT NULL CHECK (length(local_id)=64),
    label text NOT NULL CHECK (length(label) BETWEEN 1 AND 120),
    UNIQUE(owner_device_id,local_id),
    UNIQUE(id,owner_user_id,owner_device_id)
);
CREATE TABLE sessions (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL REFERENCES camps(id),
    project_id uuid NOT NULL,
    profile_id uuid NOT NULL,
    owner_user_id uuid NOT NULL,
    owner_device_id uuid NOT NULL,
    local_session_id text NOT NULL CHECK (length(local_session_id) BETWEEN 1 AND 128),
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 500),
    model text NOT NULL DEFAULT '',
    state text NOT NULL CHECK (state IN ('idle','running','waiting','done','unknown')),
    stage text NOT NULL CHECK (stage IN ('Ready','In progress','Needs you','Done')),
    external_owner boolean NOT NULL DEFAULT false,
    shared boolean NOT NULL,
    allow_requests boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1,
    transcript_revision bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(camp_id,id),
    UNIQUE(camp_id,profile_id,local_session_id),
    FOREIGN KEY(camp_id,project_id) REFERENCES projects(camp_id,id),
    FOREIGN KEY(profile_id,owner_user_id,owner_device_id) REFERENCES profiles(id,owner_user_id,owner_device_id)
);
CREATE INDEX sessions_recent ON sessions(camp_id,updated_at DESC,id);
CREATE TABLE messages (
    session_id uuid NOT NULL REFERENCES sessions(id),
    local_id text NOT NULL,
    ordinal bigint NOT NULL CHECK (ordinal>=0),
    PRIMARY KEY(session_id,local_id),
    UNIQUE(session_id,ordinal)
);
CREATE TABLE message_revisions (
    session_id uuid NOT NULL,
    local_id text NOT NULL,
    revision bigint NOT NULL,
    role text NOT NULL CHECK (role IN ('user','assistant','system','tool')),
    text text NOT NULL,
    model text NOT NULL DEFAULT '',
    effort text NOT NULL DEFAULT '',
    speed text NOT NULL DEFAULT '',
    removed boolean NOT NULL DEFAULT false,
    PRIMARY KEY(session_id,local_id,revision),
    FOREIGN KEY(session_id,local_id) REFERENCES messages(session_id,local_id)
);
ALTER TABLE events ADD COLUMN session_id uuid REFERENCES sessions(id);
CREATE TABLE event_recipients (
    camp_id uuid NOT NULL,
    cursor bigint NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id),
    PRIMARY KEY(camp_id,cursor,user_id),
    FOREIGN KEY(camp_id,cursor) REFERENCES events(camp_id,cursor) ON DELETE CASCADE
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON profiles,sessions,messages,message_revisions,event_recipients TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping shared transcripts'; END $$;
-- +goose StatementEnd

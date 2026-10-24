-- +goose Up
CREATE TABLE memory_spaces (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL REFERENCES camps(id),
    project_id uuid NOT NULL,
    owner_user_id uuid REFERENCES users(id),
    UNIQUE NULLS NOT DISTINCT(camp_id,project_id,owner_user_id),
    FOREIGN KEY(camp_id,project_id) REFERENCES projects(camp_id,id),
    UNIQUE(camp_id,id)
);
CREATE TABLE memory_observations (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL,
    space_id uuid NOT NULL,
    creator_user_id uuid NOT NULL REFERENCES users(id),
    source_session_id uuid NOT NULL,
    transcript_revision bigint NOT NULL CHECK(transcript_revision>0),
    permission_revision bigint NOT NULL CHECK(permission_revision>0),
    message_ids text[] NOT NULL CHECK(cardinality(message_ids) BETWEEN 1 AND 120),
    title text NOT NULL CHECK(octet_length(title) BETWEEN 1 AND 500),
    content text NOT NULL CHECK(octet_length(content) BETWEEN 1 AND 262144),
    content_hash text NOT NULL CHECK(content_hash ~ '^[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    engine_saved_at timestamptz,
    FOREIGN KEY(camp_id,space_id) REFERENCES memory_spaces(camp_id,id),
    FOREIGN KEY(camp_id,source_session_id) REFERENCES sessions(camp_id,id),
    UNIQUE(space_id,source_session_id,transcript_revision,permission_revision,content_hash)
);
CREATE INDEX memory_observations_space ON memory_observations(space_id,id);
ALTER TABLE events ADD COLUMN memory_observation_id uuid REFERENCES memory_observations(id);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON memory_spaces,memory_observations TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM memory_observations) THEN
        RAISE EXCEPTION 'Retain memory observations and the matching Cortex schema when restoring an older server';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE events DROP COLUMN memory_observation_id;
DROP TABLE memory_observations;
DROP TABLE memory_spaces;

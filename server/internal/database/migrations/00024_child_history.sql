-- +goose Up
CREATE TABLE session_children (
    session_id uuid PRIMARY KEY REFERENCES sessions(id),
    camp_id uuid NOT NULL,
    parent_session_id uuid NOT NULL,
    source_key text NOT NULL CHECK (source_key ~ '^[a-f0-9]{64}$'),
    UNIQUE(parent_session_id, source_key),
    UNIQUE(parent_session_id, session_id),
    CHECK(parent_session_id <> session_id),
    FOREIGN KEY(camp_id,parent_session_id) REFERENCES sessions(camp_id,id),
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id)
);
CREATE TABLE session_child_revisions (
    parent_session_id uuid NOT NULL,
    child_session_id uuid NOT NULL,
    revision bigint NOT NULL CHECK(revision > 0),
    transcript_revision bigint NOT NULL CHECK(transcript_revision >= 0),
    source_revision text NOT NULL CHECK(source_revision ~ '^[a-f0-9]{64}$'),
    name text NOT NULL CHECK(octet_length(name) BETWEEN 1 AND 500),
    phase text NOT NULL CHECK(octet_length(phase) <= 128),
    workflow text NOT NULL CHECK(octet_length(workflow) <= 256),
    state text NOT NULL CHECK(state IN ('unknown','running','done','failed','stopped','cancelled')),
    model text NOT NULL CHECK(octet_length(model) <= 200),
    complete boolean NOT NULL,
    removed boolean NOT NULL DEFAULT false,
    PRIMARY KEY(parent_session_id,child_session_id,revision),
    FOREIGN KEY(parent_session_id,child_session_id) REFERENCES session_children(parent_session_id,session_id)
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON session_children,session_child_revisions TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping child history'; END $$;
-- +goose StatementEnd

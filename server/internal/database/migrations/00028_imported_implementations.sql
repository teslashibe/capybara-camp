-- +goose Up
CREATE TABLE planning_imported_implementations (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    plan_version bigint NOT NULL,
    ordinal integer NOT NULL CHECK(ordinal>0),
    source_id text NOT NULL CHECK(source_id ~ '^[a-f0-9]{64}$'),
    session_id uuid NOT NULL,
    transcript_revision bigint NOT NULL CHECK(transcript_revision>=0),
    message_count integer NOT NULL CHECK(message_count BETWEEN 0 AND 2000),
    provider text NOT NULL CHECK(provider IN ('Codex','Claude')),
    state text NOT NULL CHECK(state IN ('approved','idle','complete','needs_review')),
    details jsonb NOT NULL,
    attempt_count integer NOT NULL CHECK(attempt_count BETWEEN 0 AND 200),
    FOREIGN KEY(camp_id,plan_id) REFERENCES planning_tickets(camp_id,id),
    FOREIGN KEY(plan_id,plan_version) REFERENCES planning_versions(plan_id,version),
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id),
    UNIQUE(plan_id,source_id),
    UNIQUE(plan_id,ordinal)
);
CREATE TABLE planning_imported_attempts (
    implementation_id uuid NOT NULL REFERENCES planning_imported_implementations(id),
    ordinal integer NOT NULL CHECK(ordinal>0),
    body jsonb NOT NULL,
    PRIMARY KEY(implementation_id,ordinal)
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON planning_imported_implementations,planning_imported_attempts TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM planning_imported_implementations) THEN
        RAISE EXCEPTION 'Retain imported implementation histories when restoring an older server';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE planning_imported_attempts;
DROP TABLE planning_imported_implementations;

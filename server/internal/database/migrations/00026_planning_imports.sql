-- +goose Up
ALTER TABLE planning_versions ADD COLUMN import_snapshot jsonb;
ALTER TABLE planning_versions DROP CONSTRAINT planning_versions_brief_check;
ALTER TABLE planning_versions ADD CONSTRAINT planning_versions_brief_check CHECK(octet_length(brief) BETWEEN 0 AND 8192);
ALTER TABLE planning_tickets DROP CONSTRAINT planning_tickets_title_check;
ALTER TABLE planning_tickets ADD CONSTRAINT planning_tickets_title_check CHECK(length(title) BETWEEN 1 AND 2000);
CREATE TABLE planning_imports (
    camp_id uuid NOT NULL REFERENCES camps(id),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    source_id text NOT NULL CHECK(source_id ~ '^[a-f0-9]{64}$'),
    plan_id uuid NOT NULL,
    input_sha256 bytea NOT NULL CHECK(octet_length(input_sha256)=32),
    PRIMARY KEY(camp_id,owner_user_id,source_id),
    FOREIGN KEY(camp_id,plan_id) REFERENCES planning_tickets(camp_id,id)
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON planning_imports TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM planning_imports) THEN
        RAISE EXCEPTION 'Retain an import-aware build while imported planning history exists';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE planning_imports;
ALTER TABLE planning_versions DROP COLUMN import_snapshot;
ALTER TABLE planning_versions DROP CONSTRAINT planning_versions_brief_check;
ALTER TABLE planning_versions ADD CONSTRAINT planning_versions_brief_check CHECK(octet_length(brief) BETWEEN 1 AND 8192);
ALTER TABLE planning_tickets DROP CONSTRAINT planning_tickets_title_check;
ALTER TABLE planning_tickets ADD CONSTRAINT planning_tickets_title_check CHECK(length(title) BETWEEN 1 AND 120);

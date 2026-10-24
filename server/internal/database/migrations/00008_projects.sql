-- +goose Up
ALTER TABLE projects ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision>0);
ALTER TABLE projects ADD COLUMN archived boolean NOT NULL DEFAULT false;
ALTER TABLE projects ADD COLUMN inbox boolean NOT NULL DEFAULT false;
ALTER TABLE projects ADD CONSTRAINT projects_inbox_active CHECK (NOT inbox OR NOT archived);
CREATE UNIQUE INDEX projects_one_inbox ON projects(camp_id) WHERE inbox;
INSERT INTO projects(id,camp_id,name,inbox) SELECT gen_random_uuid(),id,'Inbox',true FROM camps;
CREATE TABLE project_repository_links (
    camp_id uuid NOT NULL,
    project_id uuid NOT NULL,
    url text NOT NULL CHECK (length(url) BETWEEN 1 AND 500),
    PRIMARY KEY(project_id,url),
    FOREIGN KEY(camp_id,project_id) REFERENCES projects(camp_id,id)
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON project_repository_links TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Retain a compatible binary instead of dropping client projects'; END $$;
-- +goose StatementEnd

-- +goose Up
-- Workspaces pin the repository links and archived state of their project, not
-- its display name. Existing workspaces recorded projects.revision, so the new
-- counter starts there and moves only when links or archiving change.
ALTER TABLE projects ADD COLUMN authority_revision bigint NOT NULL DEFAULT 1;
UPDATE projects SET authority_revision=revision;

-- +goose Down
-- Schema 41 fences workspaces on every project revision, including renames.
ALTER TABLE projects DROP COLUMN authority_revision;

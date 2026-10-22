-- +goose Up
ALTER TABLE session_transfers
    ADD COLUMN base_commit text CHECK (base_commit ~ '^[a-f0-9]{40}$'),
    ADD COLUMN source_branch text,
    ADD COLUMN file_count integer CHECK (file_count BETWEEN 0 AND 256),
    ADD CONSTRAINT transfer_review_metadata CHECK (
        (base_commit IS NULL AND source_branch IS NULL AND file_count IS NULL)
        OR (base_commit IS NOT NULL AND source_branch IS NOT NULL AND file_count IS NOT NULL)
    );
ALTER TABLE ticket_workspaces ADD COLUMN transfer_id uuid REFERENCES session_transfers(id);
DROP INDEX ticket_workspace_pending;
CREATE UNIQUE INDEX ticket_workspace_pending ON ticket_workspaces(session_id)
    WHERE transfer_id IS NULL AND state IN ('queued','preparing','ready');
CREATE UNIQUE INDEX ticket_workspace_transfer ON ticket_workspaces(transfer_id)
    WHERE transfer_id IS NOT NULL AND state IN ('queued','preparing','ready');

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping transfer workspace bindings'; END $$;
-- +goose StatementEnd

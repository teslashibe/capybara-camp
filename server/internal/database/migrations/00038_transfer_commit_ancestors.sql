-- +goose Up
ALTER TABLE session_transfers ADD COLUMN remote_base_commit text,
    ADD CONSTRAINT transfer_remote_ancestor CHECK (remote_base_commit IS NULL OR
        (base_commit IS NOT NULL AND remote_base_commit ~ '^[a-f0-9]{40}$' AND remote_base_commit<>base_commit));
ALTER TABLE ticket_workspaces ADD COLUMN remote_base_commit text,
    ADD CONSTRAINT workspace_remote_ancestor CHECK (remote_base_commit IS NULL OR
        (transfer_id IS NOT NULL AND remote_base_commit ~ '^[a-f0-9]{40}$' AND remote_base_commit<>base_commit));

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    LOCK TABLE session_transfers, ticket_workspaces IN ACCESS EXCLUSIVE MODE;
    IF EXISTS(SELECT 1 FROM session_transfers WHERE remote_base_commit IS NOT NULL)
       OR EXISTS(SELECT 1 FROM ticket_workspaces WHERE remote_base_commit IS NOT NULL) THEN
        RAISE EXCEPTION 'Retain a compatible binary while unpublished commit transfers exist';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE ticket_workspaces DROP CONSTRAINT workspace_remote_ancestor, DROP COLUMN remote_base_commit;
ALTER TABLE session_transfers DROP CONSTRAINT transfer_remote_ancestor, DROP COLUMN remote_base_commit;

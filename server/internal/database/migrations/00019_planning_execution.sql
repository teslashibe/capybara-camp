-- +goose Up
ALTER TABLE planning_participants
    ADD COLUMN execution_location text,
    ADD COLUMN execution_epoch bigint,
    ADD COLUMN execution_workspace_id uuid,
    ADD CONSTRAINT planning_execution_bound CHECK (
      (execution_location IS NULL AND execution_epoch IS NULL AND execution_workspace_id IS NULL)
      OR (execution_location IS NOT NULL AND execution_epoch IS NOT NULL AND execution_epoch>0 AND (
        (execution_location='local' AND execution_workspace_id IS NULL)
        OR (execution_location='cloud' AND execution_workspace_id IS NOT NULL)
      ))
    );
-- Existing revisions retain unknown bindings. Never infer a historical
-- destination from today's assignment; restart captures a reviewed revision.

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping planning execution bindings'; END $$;
-- +goose StatementEnd

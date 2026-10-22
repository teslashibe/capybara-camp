-- +goose Up
ALTER TABLE sessions ADD COLUMN history_only boolean NOT NULL DEFAULT false;
ALTER TABLE sessions ADD CONSTRAINT sessions_history_only CHECK (
    NOT history_only OR (
        NOT allow_requests AND NOT external_owner
        AND state IN ('idle','done','unknown') AND stage <> 'In progress'
        AND execution_location='local' AND execution_epoch=1
        AND execution_workspace_id IS NULL
    )
);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM sessions WHERE history_only) THEN
        RAISE EXCEPTION 'Retain read-only history sources when restoring an older server';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE sessions DROP CONSTRAINT sessions_history_only;
ALTER TABLE sessions DROP COLUMN history_only;

-- +goose Up
ALTER TABLE planning_versions ADD COLUMN mode text NOT NULL DEFAULT 'comparison';
ALTER TABLE planning_versions DROP CONSTRAINT planning_versions_call_limit_check;
ALTER TABLE planning_versions ADD CONSTRAINT planning_versions_mode_check CHECK (
    (mode='single' AND call_limit=1 AND merge_slot=0)
    OR (mode='comparison' AND call_limit=5)
);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Retain a compatible binary rather than dropping planning mode history'; END $$;
-- +goose StatementEnd

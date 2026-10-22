-- +goose Up
ALTER TABLE request_controls ADD COLUMN resolution text NOT NULL DEFAULT '' CHECK (resolution IN ('','accepted','rejected'));
UPDATE request_controls SET resolution='accepted' WHERE kind='steer' AND accepted_text<>'';

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping control resolution'; END $$;
-- +goose StatementEnd

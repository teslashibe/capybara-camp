-- +goose Up
ALTER TABLE request_sources ADD COLUMN message_ids text[] NOT NULL DEFAULT '{}'
    CHECK (cardinality(message_ids) <= 120);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of widening saved context grants'; END $$;
-- +goose StatementEnd

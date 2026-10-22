-- +goose Up
ALTER TABLE session_transfers ADD COLUMN history_sha256 text
    CHECK(history_sha256 IS NULL OR history_sha256 ~ '^[a-f0-9]{64}$');

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM session_transfers WHERE history_sha256 IS NOT NULL) THEN
        RAISE EXCEPTION 'Retain a history-aware build while reviewed transfers exist';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE session_transfers DROP COLUMN history_sha256;

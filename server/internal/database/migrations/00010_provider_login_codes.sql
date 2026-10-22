-- +goose Up
ALTER TABLE provider_login_attempts
    ADD COLUMN code_id uuid,
    ADD COLUMN encrypted_code bytea,
    ADD COLUMN code_delivery_id uuid,
    ADD COLUMN code_consumed_at timestamptz,
    ADD CONSTRAINT provider_login_code_state CHECK (
        (code_id IS NOT NULL OR (encrypted_code IS NULL AND code_delivery_id IS NULL AND code_consumed_at IS NULL))
        AND (code_consumed_at IS NULL OR (code_delivery_id IS NOT NULL AND encrypted_code IS NULL))
        AND (encrypted_code IS NULL OR state='waiting')
    );

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping provider login state'; END $$;
-- +goose StatementEnd

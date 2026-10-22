-- +goose Up
ALTER TABLE transfer_handoffs
    ADD COLUMN commit_nonce uuid,
    ADD COLUMN commit_permissions jsonb,
    ADD COLUMN commit_approved boolean,
    ADD COLUMN commit_requested_at timestamptz,
    ADD COLUMN commit_hash bytea CHECK (commit_hash IS NULL OR octet_length(commit_hash)=32),
    ADD COLUMN execution_epoch bigint CHECK (execution_epoch IS NULL OR execution_epoch>0),
    ADD CONSTRAINT handoff_commit_request CHECK (
        (commit_nonce IS NULL AND commit_permissions IS NULL AND commit_approved IS NULL AND commit_requested_at IS NULL)
        OR (commit_nonce IS NOT NULL AND commit_permissions IS NOT NULL AND jsonb_typeof(commit_permissions)='object' AND commit_approved IS NOT NULL AND commit_requested_at IS NOT NULL)
    ),
    ADD CONSTRAINT handoff_commit_result CHECK (
        (state='committed' AND commit_nonce IS NOT NULL AND commit_hash IS NOT NULL AND execution_epoch IS NOT NULL)
        OR (state<>'committed' AND commit_hash IS NULL AND execution_epoch IS NULL)
    );
ALTER TABLE session_transfers DROP CONSTRAINT session_transfers_state_check;
ALTER TABLE session_transfers ADD CONSTRAINT session_transfers_state_check
    CHECK (state IN ('uploading','ready','cancelled','expired','completed'));

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of dropping handoff receipts'; END $$;
-- +goose StatementEnd

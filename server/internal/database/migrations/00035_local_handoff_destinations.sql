-- +goose Up
ALTER TABLE transfer_handoffs
    ALTER COLUMN workspace_id DROP NOT NULL,
    ALTER COLUMN workspace_revision DROP NOT NULL,
    ADD COLUMN local_profile_id uuid REFERENCES profiles(id),
    ADD COLUMN local_session_id text,
    ADD COLUMN local_purpose text,
    ADD COLUMN local_receipt_sha256 text,
    ADD COLUMN local_transcript_revision bigint,
    ADD CONSTRAINT handoff_destination CHECK (
        (workspace_id IS NOT NULL AND workspace_revision IS NOT NULL
         AND local_profile_id IS NULL AND local_session_id IS NULL AND local_purpose IS NULL
         AND local_receipt_sha256 IS NULL AND local_transcript_revision IS NULL)
        OR
        (workspace_id IS NULL AND workspace_revision IS NULL AND local_profile_id IS NOT NULL
         AND local_session_id IS NOT NULL AND local_session_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
         AND local_purpose IS NOT NULL AND local_purpose IN ('planning','implementation')
         AND local_receipt_sha256 IS NOT NULL AND local_receipt_sha256 ~ '^[a-f0-9]{64}$'
         AND local_transcript_revision IS NOT NULL AND local_transcript_revision >= 0)
    );

-- A local conversation may be shared to more than one Camp. Destination
-- collision checks need an owner-profile lookup without scanning every Camp.
CREATE INDEX sessions_profile_local_conversation ON sessions(profile_id,local_session_id);

-- +goose Down
LOCK TABLE transfer_handoffs IN ACCESS EXCLUSIVE MODE;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM transfer_handoffs WHERE local_profile_id IS NOT NULL) THEN
        RAISE EXCEPTION 'Retain a local-handoff-compatible binary while local destination receipts exist';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX sessions_profile_local_conversation;
ALTER TABLE transfer_handoffs
    DROP CONSTRAINT handoff_destination,
    DROP COLUMN local_profile_id,
    DROP COLUMN local_session_id,
    DROP COLUMN local_purpose,
    DROP COLUMN local_receipt_sha256,
    DROP COLUMN local_transcript_revision,
    ALTER COLUMN workspace_id SET NOT NULL,
    ALTER COLUMN workspace_revision SET NOT NULL;

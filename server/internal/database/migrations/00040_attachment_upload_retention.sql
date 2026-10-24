-- +goose Up
-- Existing uploads receive a fresh retention window on upgrade.
ALTER TABLE transcript_attachment_blobs ADD COLUMN last_uploaded_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX transcript_attachment_upload_expiry ON transcript_attachment_blobs(last_uploaded_at,camp_id);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT UPDATE(last_uploaded_at) ON transcript_attachment_blobs TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Schema 39 retains the bodies and does not run expiry cleanup.
DROP INDEX transcript_attachment_upload_expiry;
ALTER TABLE transcript_attachment_blobs DROP COLUMN last_uploaded_at;

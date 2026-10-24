-- +goose Up
-- Message revisions are immutable and only leave with their session, so a
-- published body stays published. Cleanup scans unpublished uploads only.
ALTER TABLE transcript_attachment_blobs ADD COLUMN published boolean NOT NULL DEFAULT false;
UPDATE transcript_attachment_blobs b SET published=true
 WHERE EXISTS(SELECT 1 FROM message_revisions v WHERE v.session_id=b.session_id
 AND v.attachments @> jsonb_build_array(jsonb_build_object('state','embedded','sha256',b.sha256)));
DROP INDEX transcript_attachment_upload_expiry;
CREATE INDEX transcript_attachment_upload_expiry ON transcript_attachment_blobs(last_uploaded_at,camp_id) WHERE NOT published;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT UPDATE(published) ON transcript_attachment_blobs TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Schema 40 cleanup rechecks every aged upload against message revisions.
DROP INDEX transcript_attachment_upload_expiry;
CREATE INDEX transcript_attachment_upload_expiry ON transcript_attachment_blobs(last_uploaded_at,camp_id);
ALTER TABLE transcript_attachment_blobs DROP COLUMN published;

-- +goose Up
ALTER TABLE message_revisions ADD COLUMN attachments jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD CONSTRAINT message_attachment_array CHECK (jsonb_typeof(attachments)='array' AND jsonb_array_length(attachments)<=64);
CREATE TABLE transcript_attachment_blobs (
    camp_id uuid NOT NULL,
    parent_session_id uuid NOT NULL,
    session_id uuid NOT NULL,
    sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    byte_count integer NOT NULL CHECK (byte_count BETWEEN 1 AND 16777216),
    encrypted_body bytea NOT NULL CHECK (octet_length(encrypted_body)=byte_count+28),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(session_id,sha256),
    FOREIGN KEY(camp_id,parent_session_id) REFERENCES sessions(camp_id,id) ON DELETE CASCADE,
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id) ON DELETE CASCADE
);
CREATE INDEX transcript_attachment_parent ON transcript_attachment_blobs(parent_session_id);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,DELETE ON transcript_attachment_blobs TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    LOCK TABLE transcript_attachment_blobs, message_revisions IN ACCESS EXCLUSIVE MODE;
    IF EXISTS(SELECT 1 FROM transcript_attachment_blobs)
       OR EXISTS(SELECT 1 FROM message_revisions WHERE attachments<>'[]'::jsonb) THEN
        RAISE EXCEPTION 'Retain a compatible binary while transcript attachments exist';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE transcript_attachment_blobs;
ALTER TABLE message_revisions DROP CONSTRAINT message_attachment_array, DROP COLUMN attachments;

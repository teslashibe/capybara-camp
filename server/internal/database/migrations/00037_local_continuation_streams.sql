-- +goose Up
ALTER TABLE transfer_handoffs ADD COLUMN local_provider_session_id text,
    ADD CONSTRAINT local_provider_session_binding CHECK (
        local_provider_session_id IS NULL OR (
            local_profile_id IS NOT NULL AND state='committed'
            AND octet_length(local_provider_session_id) BETWEEN 1 AND 200
            AND position('/' in local_provider_session_id)=0
            AND position(chr(92) in local_provider_session_id)=0
            AND position(chr(10) in local_provider_session_id)=0
            AND position(chr(13) in local_provider_session_id)=0
        )
    );
CREATE UNIQUE INDEX handoff_local_epoch ON transfer_handoffs(session_id,execution_epoch)
    WHERE state='committed' AND local_profile_id IS NOT NULL;
CREATE UNIQUE INDEX handoff_local_provider_identity ON transfer_handoffs(local_profile_id,local_provider_session_id)
    WHERE local_provider_session_id IS NOT NULL;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    LOCK TABLE transfer_handoffs IN ACCESS EXCLUSIVE MODE;
    IF EXISTS(SELECT 1 FROM transfer_handoffs WHERE local_provider_session_id IS NOT NULL)
       OR EXISTS(SELECT 1 FROM prompt_requests r JOIN transfer_handoffs h ON h.session_id=r.session_id AND h.execution_epoch=r.execution_epoch
                 WHERE r.kind='chat' AND r.execution_location='local'
                 AND (r.stream_seq>0 OR r.state IN ('started','cancel_requested','outcome_unknown'))
                 AND h.state='committed' AND h.local_profile_id IS NOT NULL) THEN
        RAISE EXCEPTION 'Retain a compatible binary while local continuation identities exist';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX handoff_local_provider_identity;
DROP INDEX handoff_local_epoch;
ALTER TABLE transfer_handoffs DROP CONSTRAINT local_provider_session_binding, DROP COLUMN local_provider_session_id;

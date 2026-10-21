-- +goose Up
CREATE TABLE notifications (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL REFERENCES camps(id) ON DELETE CASCADE,
    recipient_user_id uuid NOT NULL REFERENCES users(id),
    completion_key text NOT NULL CHECK (length(completion_key) BETWEEN 1 AND 160),
    session_id uuid REFERENCES sessions(id),
    request_id uuid REFERENCES prompt_requests(id),
    plan_id uuid,
    plan_version bigint,
    kind text NOT NULL CHECK (kind IN ('finished','attention','plan')),
    revision bigint NOT NULL DEFAULT 1,
    read_at timestamptz,
    archived boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(camp_id,recipient_user_id,completion_key),
    FOREIGN KEY(camp_id,recipient_user_id) REFERENCES memberships(camp_id,user_id) ON DELETE CASCADE,
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id),
    FOREIGN KEY(camp_id,plan_id) REFERENCES planning_tickets(camp_id,id),
    FOREIGN KEY(plan_id,plan_version) REFERENCES planning_versions(plan_id,version),
    CHECK ((session_id IS NOT NULL AND request_id IS NOT NULL AND plan_id IS NULL AND plan_version IS NULL)
        OR (session_id IS NULL AND request_id IS NULL AND plan_id IS NOT NULL AND plan_version IS NOT NULL))
);
CREATE INDEX notification_inbox ON notifications(camp_id,recipient_user_id,created_at DESC,id DESC);
ALTER TABLE events ADD COLUMN recipient_user_id uuid REFERENCES users(id);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON notifications TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of deleting recipient notification history'; END $$;
-- +goose StatementEnd

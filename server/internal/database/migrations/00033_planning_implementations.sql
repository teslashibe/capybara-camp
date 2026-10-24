-- +goose Up
ALTER TABLE planning_execution_approvals DROP CONSTRAINT planning_execution_approvals_state_check;
ALTER TABLE planning_execution_approvals ADD CONSTRAINT planning_execution_approvals_state_check CHECK(state IN ('pending','approved','cancelled','dispatched'));
CREATE TABLE planning_implementations (
    id uuid PRIMARY KEY,
    approval_id uuid NOT NULL UNIQUE REFERENCES planning_execution_approvals(id),
    camp_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    plan_version bigint NOT NULL,
    provider_session_id text CHECK(length(provider_session_id) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(id,camp_id,plan_id,plan_version),
    FOREIGN KEY(camp_id,plan_id) REFERENCES planning_tickets(camp_id,id),
    FOREIGN KEY(plan_id,plan_version) REFERENCES planning_versions(plan_id,version)
);
ALTER TABLE prompt_requests ADD COLUMN implementation_id uuid;
-- +goose StatementBegin
DO $$ DECLARE v record; BEGIN
    FOR v IN SELECT conname FROM pg_constraint WHERE conrelid='prompt_requests'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%kind%' LOOP
        EXECUTE format('ALTER TABLE prompt_requests DROP CONSTRAINT %I',v.conname);
    END LOOP;
END $$;
-- +goose StatementEnd
ALTER TABLE prompt_requests ADD CONSTRAINT prompt_requests_work_kind CHECK (
    (kind='chat' AND plan_id IS NULL AND plan_version IS NULL AND plan_step IS NULL AND implementation_id IS NULL)
 OR (kind='planning' AND plan_id IS NOT NULL AND plan_version IS NOT NULL AND plan_step IS NOT NULL AND plan_step BETWEEN 0 AND 4 AND implementation_id IS NULL)
 OR (kind='implementation' AND plan_id IS NOT NULL AND plan_version IS NOT NULL AND plan_step IS NULL AND implementation_id IS NOT NULL)
);
ALTER TABLE prompt_requests ADD CONSTRAINT prompt_requests_implementation_fk FOREIGN KEY(implementation_id,camp_id,plan_id,plan_version) REFERENCES planning_implementations(id,camp_id,plan_id,plan_version);
CREATE INDEX implementation_requests ON prompt_requests(implementation_id,created_at,id) WHERE implementation_id IS NOT NULL;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON planning_implementations TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    LOCK TABLE planning_implementations,planning_execution_approvals,prompt_requests IN ACCESS EXCLUSIVE MODE;
    IF EXISTS(SELECT 1 FROM planning_implementations) OR EXISTS(SELECT 1 FROM planning_execution_approvals WHERE state='dispatched') THEN
        RAISE EXCEPTION 'Retain implementation conversations and their provider identities when restoring an older server';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE prompt_requests DROP CONSTRAINT prompt_requests_implementation_fk;
ALTER TABLE prompt_requests DROP CONSTRAINT prompt_requests_work_kind;
ALTER TABLE prompt_requests DROP COLUMN implementation_id;
ALTER TABLE prompt_requests ADD CONSTRAINT prompt_requests_kind_check CHECK(kind IN ('chat','planning'));
ALTER TABLE prompt_requests ADD CONSTRAINT prompt_requests_plan_binding CHECK((kind='chat' AND plan_id IS NULL AND plan_version IS NULL AND plan_step IS NULL) OR (kind='planning' AND plan_id IS NOT NULL AND plan_version IS NOT NULL AND plan_step BETWEEN 0 AND 4));
DROP TABLE planning_implementations;
ALTER TABLE planning_execution_approvals DROP CONSTRAINT planning_execution_approvals_state_check;
ALTER TABLE planning_execution_approvals ADD CONSTRAINT planning_execution_approvals_state_check CHECK(state IN ('pending','approved','cancelled'));

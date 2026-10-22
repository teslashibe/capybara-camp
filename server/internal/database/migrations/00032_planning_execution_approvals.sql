-- +goose Up
CREATE TABLE planning_execution_approvals (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL,
    plan_id uuid NOT NULL,
    plan_version bigint NOT NULL,
    plan_revision bigint NOT NULL CHECK(plan_revision>0),
    plan_sha256 text NOT NULL CHECK(plan_sha256 ~ '^[a-f0-9]{64}$'),
    creator_user_id uuid NOT NULL REFERENCES users(id),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    owner_device_id uuid NOT NULL REFERENCES devices(id),
    session_id uuid NOT NULL,
    profile_id uuid NOT NULL,
    session_revision bigint NOT NULL CHECK(session_revision>0),
    transcript_revision bigint NOT NULL CHECK(transcript_revision>=0),
    permission_revision bigint NOT NULL CHECK(permission_revision>0),
    provider text NOT NULL CHECK(provider IN ('Codex','Claude')),
    model text NOT NULL CHECK(octet_length(model) BETWEEN 1 AND 200),
    effort text NOT NULL CHECK(octet_length(effort)<=64),
    speed text NOT NULL CHECK(speed IN ('standard','fast','ultrafast')),
    execution_location text NOT NULL CHECK(execution_location IN ('local','cloud')),
    execution_epoch bigint NOT NULL CHECK(execution_epoch>0),
    execution_workspace_id uuid REFERENCES ticket_workspaces(id),
    permissions jsonb NOT NULL CHECK(jsonb_typeof(permissions)='object'),
    execution_context text CHECK(execution_context ~ '^[a-f0-9]{64}$'),
    prompt text NOT NULL CHECK(octet_length(prompt) BETWEEN 1 AND 65536),
    state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','cancelled')),
    revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
    approved_by_device_id uuid REFERENCES devices(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    approved_at timestamptz,
    FOREIGN KEY(camp_id,plan_id) REFERENCES planning_tickets(camp_id,id),
    FOREIGN KEY(plan_id,plan_version) REFERENCES planning_versions(plan_id,version),
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id),
    FOREIGN KEY(profile_id,owner_user_id,owner_device_id) REFERENCES profiles(id,owner_user_id,owner_device_id),
    CHECK((execution_location='local' AND execution_workspace_id IS NULL) OR (execution_location='cloud' AND execution_workspace_id IS NOT NULL)),
    CHECK(state<>'approved' OR (approved_at IS NOT NULL AND approved_by_device_id IS NOT NULL AND (execution_location='cloud' OR execution_context IS NOT NULL)))
);
CREATE UNIQUE INDEX planning_execution_one_approval ON planning_execution_approvals(plan_id) WHERE state IN ('pending','approved');
CREATE TABLE planning_execution_sources (
    approval_id uuid NOT NULL REFERENCES planning_execution_approvals(id),
    camp_id uuid NOT NULL,
    session_id uuid NOT NULL,
    permission_revision bigint NOT NULL CHECK(permission_revision>0),
    transcript_revision bigint NOT NULL CHECK(transcript_revision>=0),
    PRIMARY KEY(approval_id,session_id),
    FOREIGN KEY(camp_id,session_id) REFERENCES sessions(camp_id,id)
);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON planning_execution_approvals,planning_execution_sources TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    LOCK TABLE planning_execution_approvals IN ACCESS EXCLUSIVE MODE;
    IF EXISTS(SELECT 1 FROM planning_execution_approvals) THEN
        RAISE EXCEPTION 'Retain implementation approvals and their exact plan bindings when restoring an older server';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE planning_execution_sources;
DROP TABLE planning_execution_approvals;

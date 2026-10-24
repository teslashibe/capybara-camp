-- +goose Up
CREATE TABLE planning_tickets (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL REFERENCES camps(id),
    project_id uuid NOT NULL,
    creator_user_id uuid NOT NULL REFERENCES users(id),
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
    shared boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1,
    active_version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(camp_id,id),
    FOREIGN KEY(camp_id,project_id) REFERENCES projects(camp_id,id)
);
CREATE TABLE planning_versions (
    plan_id uuid NOT NULL REFERENCES planning_tickets(id),
    version bigint NOT NULL,
    state text NOT NULL CHECK (state IN ('draft','running','paused','ready','cancelled','failed')),
    brief text NOT NULL CHECK (octet_length(brief) BETWEEN 1 AND 8192),
    sources jsonb NOT NULL,
    budget_policy text NOT NULL CHECK (budget_policy IN ('calls','strict_tokens')),
    call_limit integer NOT NULL DEFAULT 5 CHECK (call_limit=5),
    input_target bigint NOT NULL DEFAULT 40000 CHECK (input_target>0),
    output_target bigint NOT NULL DEFAULT 8000 CHECK (output_target>0),
    merge_slot smallint NOT NULL DEFAULT 0 CHECK (merge_slot IN (0,1)),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(plan_id,version)
);
CREATE TABLE planning_participants (
    plan_id uuid NOT NULL,
    version bigint NOT NULL,
    slot smallint NOT NULL CHECK (slot IN (0,1)),
    session_id uuid NOT NULL REFERENCES sessions(id),
    profile_id uuid NOT NULL REFERENCES profiles(id),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    owner_device_id uuid NOT NULL REFERENCES devices(id),
    permission_revision bigint NOT NULL,
    provider text NOT NULL CHECK (provider IN ('Codex','Claude')),
    model text NOT NULL,
    effort text NOT NULL,
    speed text NOT NULL,
    PRIMARY KEY(plan_id,version,slot),
    FOREIGN KEY(plan_id,version) REFERENCES planning_versions(plan_id,version)
);
ALTER TABLE prompt_requests ADD COLUMN kind text NOT NULL DEFAULT 'chat' CHECK (kind IN ('chat','planning'));
ALTER TABLE prompt_requests ADD COLUMN plan_id uuid;
ALTER TABLE prompt_requests ADD COLUMN plan_version bigint;
ALTER TABLE prompt_requests ADD COLUMN plan_step smallint;
ALTER TABLE prompt_requests ADD FOREIGN KEY(plan_id,plan_version) REFERENCES planning_versions(plan_id,version);
ALTER TABLE prompt_requests ADD CHECK ((kind='chat' AND plan_id IS NULL AND plan_version IS NULL AND plan_step IS NULL)
    OR (kind='planning' AND plan_id IS NOT NULL AND plan_version IS NOT NULL AND plan_step BETWEEN 0 AND 4));
CREATE TABLE planning_steps (
    plan_id uuid NOT NULL,
    version bigint NOT NULL,
    ordinal smallint NOT NULL CHECK (ordinal BETWEEN 0 AND 4),
    request_id uuid NOT NULL UNIQUE REFERENCES prompt_requests(id),
    output text NOT NULL DEFAULT '' CHECK (octet_length(output)<=8192),
    usage jsonb,
    PRIMARY KEY(plan_id,version,ordinal),
    FOREIGN KEY(plan_id,version) REFERENCES planning_versions(plan_id,version)
);
ALTER TABLE events ADD COLUMN plan_id uuid REFERENCES planning_tickets(id);
CREATE INDEX planning_recent ON planning_tickets(camp_id,updated_at DESC,id);
CREATE INDEX planning_request_versions ON prompt_requests(plan_id,plan_version) WHERE plan_id IS NOT NULL;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON planning_tickets,planning_versions,planning_participants,planning_steps TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Restore a compatible backup instead of deleting planning history'; END $$;
-- +goose StatementEnd

-- +goose Up
CREATE TABLE memory_jobs (
    id uuid PRIMARY KEY,
    camp_id uuid NOT NULL,
    space_id uuid NOT NULL,
    creator_user_id uuid NOT NULL REFERENCES users(id),
    state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','preparing','awaiting','running','result_ready','outcome_unknown','completed','failed','cancelled')),
    revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
    provider text NOT NULL CHECK(provider IN ('Codex','Claude')),
    model text NOT NULL CHECK(octet_length(model) BETWEEN 1 AND 160),
    effort text NOT NULL,
    speed text NOT NULL,
    location text NOT NULL CHECK(location IN ('local','cloud')),
    profile_id uuid,
    owner_device_id uuid REFERENCES devices(id),
    account_id uuid REFERENCES provider_accounts(id),
    installation_id uuid,
    installation_epoch bigint,
    observation_ids uuid[] NOT NULL CHECK(cardinality(observation_ids) BETWEEN 1 AND 200),
    memory_refs jsonb NOT NULL CHECK(jsonb_typeof(memory_refs)='array' AND jsonb_array_length(memory_refs)<=500),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    claim_id uuid,
    claim_until timestamptz,
    coordinator_id uuid,
    coordinator_until timestamptz,
    cortex_run_id text,
    prompt_hash text CHECK(prompt_hash ~ '^[a-f0-9]{64}$'),
    inference_system text,
    inference_prompt text,
    started_at timestamptz,
    inference_result jsonb CHECK(octet_length(inference_result::text)<=8388608),
    result_hash text CHECK(result_hash ~ '^[a-f0-9]{64}$'),
    failure_code text NOT NULL DEFAULT '' CHECK(octet_length(failure_code)<=64),
    FOREIGN KEY(camp_id,space_id) REFERENCES memory_spaces(camp_id,id),
    FOREIGN KEY(profile_id,creator_user_id,owner_device_id) REFERENCES profiles(id,owner_user_id,owner_device_id),
    FOREIGN KEY(installation_id,account_id,creator_user_id) REFERENCES provider_installations(id,account_id,owner_user_id),
    CHECK((location='local' AND profile_id IS NOT NULL AND owner_device_id IS NOT NULL AND account_id IS NULL AND installation_id IS NULL AND installation_epoch IS NULL) OR (location='cloud' AND profile_id IS NULL AND owner_device_id IS NULL AND account_id IS NOT NULL AND installation_id IS NOT NULL AND installation_epoch>0))
);
CREATE UNIQUE INDEX memory_jobs_active_space ON memory_jobs(space_id) WHERE state IN ('queued','preparing','awaiting','running','result_ready','outcome_unknown');
CREATE INDEX memory_jobs_owner ON memory_jobs(creator_user_id,created_at,id);
CREATE TABLE memory_job_sources (
    job_id uuid NOT NULL REFERENCES memory_jobs(id),
    observation_id uuid NOT NULL REFERENCES memory_observations(id),
    PRIMARY KEY(job_id,observation_id)
);
ALTER TABLE events ADD COLUMN memory_job_id uuid REFERENCES memory_jobs(id);
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime') THEN
        GRANT SELECT,INSERT,UPDATE,DELETE ON memory_jobs,memory_job_sources TO camp_runtime;
    END IF;
END $$;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM memory_jobs) THEN
        RAISE EXCEPTION 'Retain memory jobs and inference outcomes when restoring an older server';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE events DROP COLUMN memory_job_id;
DROP TABLE memory_job_sources;
DROP TABLE memory_jobs;

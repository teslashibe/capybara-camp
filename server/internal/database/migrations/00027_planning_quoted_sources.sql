-- +goose Up
ALTER TABLE planning_versions ADD COLUMN quoted_sources jsonb NOT NULL DEFAULT '[]'::jsonb CHECK(jsonb_typeof(quoted_sources)='array');

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM planning_versions WHERE jsonb_array_length(quoted_sources)>0) THEN
        RAISE EXCEPTION 'Retain quoted planning context when restoring an older server';
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE planning_versions DROP COLUMN quoted_sources;

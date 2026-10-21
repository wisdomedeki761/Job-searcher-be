-- jobs.search_config_id was referenced by the Go code but missing from 0001.
-- 0001 is already recorded as applied in existing databases, so this ships as
-- a new migration. Inserts/queries using this column failed until this runs.
ALTER TABLE jobs
    ADD COLUMN IF NOT EXISTS search_config_id UUID REFERENCES search_configs(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_jobs_search_config ON jobs (search_config_id);

-- Super admin and future admin accounts
CREATE TABLE IF NOT EXISTS admins (
    id            UUID PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL DEFAULT 'super_admin',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

-- Admin-defined search configurations (job type + AI profile + cron schedule)
CREATE TABLE IF NOT EXISTS search_configs (
    id               UUID PRIMARY KEY,
    job_type         TEXT NOT NULL,
    cron_expression  TEXT NOT NULL,
    timezone         TEXT NOT NULL DEFAULT 'UTC',
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    keywords         TEXT[] NOT NULL DEFAULT '{}',
    exclude_keywords TEXT[] NOT NULL DEFAULT '{}',
    locations        TEXT[] NOT NULL DEFAULT '{}',
    employment_type  TEXT NOT NULL DEFAULT 'any',
    work_mode        TEXT NOT NULL DEFAULT 'any',
    sources          TEXT[] NOT NULL DEFAULT '{}',
    lookback_hours   INT NOT NULL DEFAULT 24,
    ai_generated     BOOLEAN NOT NULL DEFAULT FALSE,
    last_run_at      TIMESTAMPTZ,
    next_run_at      TIMESTAMPTZ,
    created_by       UUID REFERENCES admins(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_search_configs_enabled ON search_configs (enabled);

-- Collected jobs
CREATE TABLE IF NOT EXISTS jobs (
    id                    UUID PRIMARY KEY,
    source                TEXT NOT NULL,
    source_post_id        TEXT NOT NULL DEFAULT '',
    source_url            TEXT NOT NULL UNIQUE,
    job_title             TEXT NOT NULL,
    company_name          TEXT NOT NULL DEFAULT '',
    location              TEXT NOT NULL DEFAULT '',
    employment_type       TEXT NOT NULL DEFAULT '',
    work_mode             TEXT NOT NULL DEFAULT '',
    description           TEXT NOT NULL DEFAULT '',
    application_url       TEXT NOT NULL DEFAULT '',
    posted_at             TIMESTAMPTZ,
    first_seen_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    content_hash          TEXT NOT NULL,
    classification_method TEXT NOT NULL DEFAULT 'rules',
    ai_confidence         DOUBLE PRECISION,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_jobs_content_hash ON jobs (content_hash);
CREATE INDEX IF NOT EXISTS idx_jobs_posted_at   ON jobs (posted_at);
CREATE INDEX IF NOT EXISTS idx_jobs_first_seen  ON jobs (first_seen_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_jobs_location    ON jobs (location);
CREATE INDEX IF NOT EXISTS idx_jobs_work_mode   ON jobs (work_mode);
CREATE INDEX IF NOT EXISTS idx_jobs_employment  ON jobs (employment_type);
CREATE INDEX IF NOT EXISTS idx_jobs_source      ON jobs (source);

-- Which search config first discovered / matches a job (a job may match many)
CREATE TABLE IF NOT EXISTS job_search_config_hits (
    job_id           UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    search_config_id UUID NOT NULL REFERENCES search_configs(id) ON DELETE CASCADE,
    first_hit_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, search_config_id)
);

-- One row per source execution inside a scheduled or manual run
CREATE TABLE IF NOT EXISTS search_runs (
    id               UUID PRIMARY KEY,
    search_config_id UUID NOT NULL REFERENCES search_configs(id) ON DELETE CASCADE,
    source           TEXT NOT NULL,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ,
    status           TEXT NOT NULL DEFAULT 'running',
    raw_count        INT NOT NULL DEFAULT 0,
    new_jobs_count   INT NOT NULL DEFAULT 0,
    duplicate_count  INT NOT NULL DEFAULT 0,
    rejected_count   INT NOT NULL DEFAULT 0,
    error            TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_search_runs_config  ON search_runs (search_config_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_search_runs_started ON search_runs (started_at DESC);
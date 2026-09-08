-- Applications: long-running processes (node/python/go) with lifecycle
-- state. Static and PHP sites remain on the websites table model — this
-- table EXTENDS websites for process-based runtimes.
CREATE TABLE applications (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id UUID NOT NULL UNIQUE REFERENCES websites (id) ON DELETE CASCADE,
    startup_command TEXT NOT NULL DEFAULT '',
    build_command TEXT NOT NULL DEFAULT '',
    startup_file TEXT NOT NULL DEFAULT '',
    internal_port INT NOT NULL DEFAULT 0 CHECK (internal_port BETWEEN 0 AND 65535),
    env_vars JSONB NOT NULL DEFAULT '{}'::jsonb,
    process_name TEXT NOT NULL DEFAULT '',
    health TEXT NOT NULL DEFAULT 'unknown' CHECK (health IN ('unknown', 'starting', 'running', 'stopped', 'crashed', 'unhealthy')),
    restarts INT NOT NULL DEFAULT 0,
    last_health_check TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'provision_app';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'start_app';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'stop_app';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'restart_app';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'build_app';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'app_status';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'app_logs';

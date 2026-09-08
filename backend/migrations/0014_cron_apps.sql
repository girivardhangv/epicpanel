CREATE TABLE cron_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    schedule TEXT NOT NULL CHECK (schedule ~ '^(\*|[0-9*/,-]+)(\s+(\*|[0-9*/,-]+)){4}$'),
    command TEXT NOT NULL CHECK (length(command) BETWEEN 1 AND 500),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    last_run_at TIMESTAMPTZ,
    last_status TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_cron_website ON cron_jobs (website_id);

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'sync_crontab';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'install_wordpress';

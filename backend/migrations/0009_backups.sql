ALTER TABLE websites ADD COLUMN backup_schedule TEXT NOT NULL DEFAULT 'off' CHECK (backup_schedule IN ('off', 'daily', 'weekly'));
ALTER TABLE websites ADD COLUMN backup_retention INT NOT NULL DEFAULT 5 CHECK (backup_retention BETWEEN 1 AND 30);
ALTER TABLE websites ADD COLUMN last_backup_at TIMESTAMPTZ;

CREATE TABLE backups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    type TEXT NOT NULL DEFAULT 'full' CHECK (type IN ('files', 'full')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'successful', 'failed')),
    trigger_type TEXT NOT NULL DEFAULT 'manual' CHECK (trigger_type IN ('manual', 'scheduled')),
    size_bytes BIGINT NOT NULL DEFAULT 0,
    paths JSONB NOT NULL DEFAULT '[]'::jsonb,
    databases JSONB NOT NULL DEFAULT '[]'::jsonb,
    error TEXT NOT NULL DEFAULT '',
    created_by UUID REFERENCES users (id) ON DELETE SET NULL,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_backups_website ON backups (website_id, created_at DESC);

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'create_backup';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'restore_backup';

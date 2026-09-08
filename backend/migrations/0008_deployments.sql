CREATE TABLE deployments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    branch TEXT NOT NULL DEFAULT '',
    commit_sha TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'successful', 'failed')),
    trigger_type TEXT NOT NULL DEFAULT 'manual' CHECK (trigger_type IN ('manual', 'rollback', 'auto')),
    rollback_of UUID REFERENCES deployments (id) ON DELETE SET NULL,
    release_dir TEXT NOT NULL DEFAULT '',
    log TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_by UUID REFERENCES users (id) ON DELETE SET NULL,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_deployments_website ON deployments (website_id, created_at DESC);

ALTER TABLE websites ADD COLUMN deploy_repo_url TEXT NOT NULL DEFAULT '';
ALTER TABLE websites ADD COLUMN deploy_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE websites ADD COLUMN deploy_token_encrypted BYTEA;
ALTER TABLE websites ADD COLUMN is_staging BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE websites ADD COLUMN staging_of UUID REFERENCES websites (id) ON DELETE SET NULL;

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'deploy_website';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'rollback_website';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'clone_staging';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'promote_staging';

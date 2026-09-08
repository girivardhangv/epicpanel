-- Phase 11: Backups — first-class backup/restore for every workload type.
--
-- Generalizes the Phase 4-era website-only backups table to the verbatim
-- master-doc types (account, database, website, minecraft world, discord bot,
-- full instance), adds sink targets (local / remote / object storage),
-- per-backup encryption material (wrapped data key), verification state and
-- the org-level schedule table (cron-driven, workload-scoped).
--
-- Every statement is re-runnable (IF NOT EXISTS / DO blocks): test harnesses
-- drop schema_migrations and re-apply the whole chain.

-- 1. Sink targets (Local | Remote | Object Storage — creds sealed at rest).
CREATE TABLE IF NOT EXISTS backup_targets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('local', 'remote', 's3')),
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    creds_enc TEXT NOT NULL DEFAULT '',
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

-- 2. Backups table generalization.
ALTER TABLE backups ADD COLUMN IF NOT EXISTS instance_id UUID REFERENCES minecraft_instances (id) ON DELETE CASCADE;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS bot_id UUID REFERENCES bot_instances (id) ON DELETE CASCADE;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS target_id UUID REFERENCES backup_targets (id) ON DELETE SET NULL;

-- website_id becomes nullable (non-website workload types).
ALTER TABLE backups ALTER COLUMN website_id DROP NOT NULL;

ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_type_check;
ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_type_chk;
DO $$ BEGIN
    ALTER TABLE backups ADD CONSTRAINT backups_type_chk CHECK (
        type IN ('files', 'full', 'account', 'database', 'website',
                 'minecraft_world', 'discord_bot', 'full_instance', 'website_files'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

ALTER TABLE backups ADD COLUMN IF NOT EXISTS verification TEXT NOT NULL DEFAULT 'pending'
    CHECK (verification IN ('pending', 'ok', 'failed'));
ALTER TABLE backups ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS encrypted BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS sink_kind TEXT NOT NULL DEFAULT 'local';
ALTER TABLE backups ADD COLUMN IF NOT EXISTS sink_ref JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS stored_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS target_server_id UUID REFERENCES servers (id) ON DELETE SET NULL;
ALTER TABLE backups ADD COLUMN IF NOT EXISTS key_enc TEXT NOT NULL DEFAULT '';

-- Legacy 'files'/'full' rows map onto the new vocabulary for the unified
-- engine; the pre-Phase-11 flow keeps writing 'website'.
DO $$ BEGIN
    UPDATE backups SET type = 'website_files' WHERE type = 'files';
    UPDATE backups SET type = 'full_instance' WHERE type = 'full';
EXCEPTION WHEN undefined_column THEN NULL; END $$;

ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_status_check;
ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_status_chk;
DO $$ BEGIN
    ALTER TABLE backups ADD CONSTRAINT backups_status_chk CHECK (
        status IN ('pending', 'running', 'successful', 'failed'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

CREATE INDEX IF NOT EXISTS idx_backups_org_created ON backups (organization_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_backups_instance ON backups (instance_id) WHERE instance_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_backups_bot ON backups (bot_id) WHERE bot_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_backups_verification ON backups (verification) WHERE verification = 'pending';

-- 3. Org-level backup schedules (cron-driven; website or bot scoped).
CREATE TABLE IF NOT EXISTS backup_schedules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    website_id UUID REFERENCES websites (id) ON DELETE CASCADE,
    bot_id UUID REFERENCES bot_instances (id) ON DELETE CASCADE,
    type TEXT NOT NULL DEFAULT 'website',
    cron TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_run_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (website_id IS NOT NULL OR bot_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_backup_schedules_due ON backup_schedules (next_run_at) WHERE enabled;

-- 4. Job types for the unified engine (agent ops in backup_ops2.go).
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'backup_run';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'backup_restore';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'backup_verify';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'backup_prune';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'terminate_backup';

-- 5. Retention/prune audit trail.
CREATE TABLE IF NOT EXISTS backup_prunes (
    id BIGSERIAL PRIMARY KEY,
    backup_id UUID NOT NULL REFERENCES backups (id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    reason TEXT NOT NULL DEFAULT 'retention',
    pruned_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

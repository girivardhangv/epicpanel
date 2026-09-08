-- Phase 7: Minecraft hosting as a first-class workload.
-- Minecraft is NOT a website with extra fields: its own table, verbatim
-- state machine (installing→stopped→starting→running→stopping→crashed,
-- failed/deleting/deleted), file layout (/srv/epicpanel/minecraft/<id>),
-- systemd unit namespace and lifecycle. The RCON password is stored ONLY
-- as AES-GCM ciphertext (secretbox) — plaintext never reaches this schema.

-- Enum creation is re-runnable (test harnesses drop schema_migrations but
-- leave the type behind; IF NOT EXISTS does not apply to CREATE TYPE).
DO $$ BEGIN
    CREATE TYPE mc_status AS ENUM (
        'installing', 'stopped', 'starting', 'running', 'stopping',
        'crashed', 'failed', 'deleting', 'deleted'
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS minecraft_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users (id),
    name TEXT NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,61}[a-z0-9]$'),
    -- Provider abstraction: vanilla | paper | purpur | fabric | forge |
    -- neoforge. Versions are validated against the provider's live
    -- manifest (runtime-fetched, cached) — never hardcoded here.
    provider TEXT NOT NULL CHECK (provider IN ('vanilla', 'paper', 'purpur', 'fabric', 'forge', 'neoforge')),
    version TEXT NOT NULL,
    java_major INT NOT NULL DEFAULT 21,
    status mc_status NOT NULL DEFAULT 'installing',
    -- What the customer asked for (running|stopped); agent truth reconciles
    -- against this — API acceptance is never success.
    desired_state TEXT NOT NULL DEFAULT 'stopped' CHECK (desired_state IN ('running', 'stopped')),
    -- Ports: one game port + one RCON port (separate ranges), allocated via
    -- the ports module and unique per server.
    port INT NOT NULL CHECK (port BETWEEN 1024 AND 65535),
    rcon_port INT NOT NULL CHECK (rcon_port BETWEEN 1024 AND 65535),
    -- JVM heap (Phase 9: derived from the plan RAM at create/update time).
    xmx_mb INT NOT NULL DEFAULT 1024 CHECK (xmx_mb >= 512),
    extra_args TEXT[] NOT NULL DEFAULT '{}',
    -- Crash recovery: systemd handles in-unit restarts (Restart=on-failure);
    -- the control plane reconciles what systemd gave up on.
    restart_policy TEXT NOT NULL DEFAULT 'on-failure' CHECK (restart_policy IN ('on-failure', 'always', 'no')),
    max_restarts INT NOT NULL DEFAULT 5 CHECK (max_restarts BETWEEN 0 AND 100),
    episode_restarts INT NOT NULL DEFAULT 0,
    episode_started_at TIMESTAMPTZ,
    restart_count INT NOT NULL DEFAULT 0,
    -- server.properties the customer set (panel-protected keys excluded;
    -- ports/rcon are columns + agent-managed, never here).
    properties JSONB NOT NULL DEFAULT '{}',
    -- RCON password: one AES-GCM ciphertext blob (base64). Write-only API.
    rcon_pass_enc TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    unit_state TEXT NOT NULL DEFAULT '',
    agent_seen_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name),
    UNIQUE (server_id, port),
    UNIQUE (server_id, rcon_port)
);

CREATE INDEX IF NOT EXISTS idx_mc_org ON minecraft_instances (organization_id);
CREATE INDEX IF NOT EXISTS idx_mc_server ON minecraft_instances (server_id);
CREATE INDEX IF NOT EXISTS idx_mc_status ON minecraft_instances (status) WHERE status <> 'deleted';

-- Scheduled tasks: restart/start/stop lifecycle + allowlisted console
-- commands only. Scheduled free-form commands would be shell by another
-- name and are rejected at create AND at fire time.
CREATE TABLE IF NOT EXISTS minecraft_schedules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id UUID NOT NULL REFERENCES minecraft_instances (id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('restart', 'start', 'stop', 'command')),
    cron TEXT NOT NULL,
    command TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_run_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (kind <> 'command' OR command <> '')
);

CREATE INDEX IF NOT EXISTS idx_mc_schedules_due ON minecraft_schedules (next_run_at) WHERE enabled;

-- World-scoped backup records (the Phase 11 full backup engine plugs in
-- here; Phase 7 ships the agent ops + these seam rows).
CREATE TABLE IF NOT EXISTS minecraft_world_backups (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id UUID NOT NULL REFERENCES minecraft_instances (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    sha256 TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (instance_id, name)
);

CREATE INDEX IF NOT EXISTS idx_mc_backups_instance ON minecraft_world_backups (instance_id, created_at DESC);

-- Minecraft jobs are first-class jobs: claimable by the node agent, linked
-- to the instance (not to a website — instances are their own workload).
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_install';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_start';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_stop';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_restart';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_kill';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_status';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_files';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_upload';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_logs';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_command';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_properties';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_backup_world';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_restore_world';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_metrics';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_delete';

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS minecraft_id UUID REFERENCES minecraft_instances (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_jobs_minecraft ON jobs (minecraft_id, created_at DESC);

-- Phase 8: Discord bot hosting as a first-class workload.
-- Bots are NOT websites: their own table, state machine, file layout
-- (/srv/epicpanel/bots/<bot_id>), systemd unit namespace and lifecycle.
-- Secrets (env vars, git tokens) are stored ONLY as AES-GCM ciphertext
-- (secretbox) — plaintext never reaches this schema.

-- Enum creation is re-runnable (test harnesses drop schema_migrations but
-- leave the type behind; IF NOT EXISTS does not apply to CREATE TYPE).
DO $$ BEGIN
    CREATE TYPE bot_status AS ENUM (
        'installing', 'stopped', 'starting', 'running', 'stopping',
        'crashed', 'failed', 'deleting', 'deleted'
    );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS bot_instances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users (id),
    name TEXT NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,61}[a-z0-9]$'),
    -- Runtime abstraction: node | python (java is foundation-only).
    runtime TEXT NOT NULL CHECK (runtime IN ('node', 'python', 'java')),
    runtime_version TEXT NOT NULL DEFAULT '',
    status bot_status NOT NULL DEFAULT 'installing',
    -- What the customer asked for (running|stopped); agent truth reconciles
    -- against this — API acceptance is never success.
    desired_state TEXT NOT NULL DEFAULT 'stopped' CHECK (desired_state IN ('running', 'stopped')),
    startup_file TEXT NOT NULL DEFAULT '',
    startup_command TEXT NOT NULL DEFAULT '',
    build_command TEXT NOT NULL DEFAULT '',
    -- Crash recovery: systemd handles in-unit restarts (Restart=on-failure);
    -- the control plane reconciles what systemd gave up on.
    restart_policy TEXT NOT NULL DEFAULT 'on-failure' CHECK (restart_policy IN ('on-failure', 'always', 'no')),
    max_restarts INT NOT NULL DEFAULT 5 CHECK (max_restarts BETWEEN 0 AND 100),
    episode_restarts INT NOT NULL DEFAULT 0,
    episode_started_at TIMESTAMPTZ,
    restart_count INT NOT NULL DEFAULT 0,
    -- Git deployment foundation. Token stored encrypted (base64 ciphertext).
    git_repo_url TEXT NOT NULL DEFAULT '',
    git_branch TEXT NOT NULL DEFAULT '',
    git_token_enc TEXT NOT NULL DEFAULT '',
    -- Env vars + secrets: one AES-GCM ciphertext blob of the JSON map.
    env_enc TEXT NOT NULL DEFAULT '',
    -- Optional network policy (systemd IPAddressAllow/Deny where supported).
    net_allow TEXT[] NOT NULL DEFAULT '{}',
    last_error TEXT NOT NULL DEFAULT '',
    unit_state TEXT NOT NULL DEFAULT '',
    agent_seen_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE INDEX IF NOT EXISTS idx_bots_org ON bot_instances (organization_id);
CREATE INDEX IF NOT EXISTS idx_bots_server ON bot_instances (server_id);
CREATE INDEX IF NOT EXISTS idx_bots_status ON bot_instances (status) WHERE status <> 'deleted';

-- Scheduled tasks: restarts (and start/stop) only — scheduled CUSTOM commands
-- would be arbitrary shell by another name and are rejected by design.
CREATE TABLE IF NOT EXISTS bot_schedules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    bot_id UUID NOT NULL REFERENCES bot_instances (id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('restart', 'start', 'stop')),
    cron TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_run_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_bot_schedules_due ON bot_schedules (next_run_at) WHERE enabled;

-- Bot jobs are first-class jobs: claimable by the node agent, linked to the
-- bot (not to a website — bots are their own workload).
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_install';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_deploy_git';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_upload';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_files';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_start';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_stop';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_restart';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_kill';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_status';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_logs';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_delete';

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS bot_id UUID REFERENCES bot_instances (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_jobs_bot ON jobs (bot_id, created_at DESC);

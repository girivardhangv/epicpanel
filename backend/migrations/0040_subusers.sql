-- Phase 16: Subusers (Pterodactyl parity) — per-server user permissions.
--
-- A subuser is a user granted a scoped set of permissions on ONE workload.
-- Pterodactyl models this as subusers(user_id, server_id, permissions[]); we
-- keep the same shape but key it by (workload_kind, workload_id) so the same
-- table serves Minecraft instances and Discord bots.
CREATE TABLE IF NOT EXISTS subusers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workload_kind TEXT NOT NULL CHECK (workload_kind IN ('minecraft', 'discord')),
    workload_id UUID NOT NULL,
    -- JSON array of canonical permission strings (e.g. ["control.console"]).
    permissions JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, workload_kind, workload_id)
);

CREATE INDEX IF NOT EXISTS idx_subusers_workload ON subusers (workload_kind, workload_id);
CREATE INDEX IF NOT EXISTS idx_subusers_user ON subusers (user_id);
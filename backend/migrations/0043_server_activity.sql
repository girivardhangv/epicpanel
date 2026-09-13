-- Phase 17: Per-server activity log (Pterodactyl parity).
--
-- This is a user-facing, per-server activity FEED: "who did what on this
-- server, from where". It is intentionally distinct from the platform audit
-- log (audit_logs), which is an admin/compliance record. One row per event;
-- instance-scoped and cascade-deleted with its server.
CREATE TABLE IF NOT EXISTS server_activities (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id UUID NOT NULL REFERENCES minecraft_instances (id) ON DELETE CASCADE,
    user_id UUID REFERENCES users (id) ON DELETE SET NULL,
    event TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_server_activities_instance
    ON server_activities (instance_id, created_at DESC);
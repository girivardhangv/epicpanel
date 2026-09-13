-- Pterodactyl alignment: ports become first-class ALLOCATIONS, not inline
-- columns. allocations(node, ip, port) is unique; the workload claims one
-- allocation via allocation_id. On delete the claim is released (FK ON DELETE
-- SET NULL), so a port is free the instant the row dies — no delete-timeout
-- hack, no double-booking.
--
-- We keep the legacy port/rcon_port columns in place (the agent + console
-- still read them) and backfill allocations from them, so this migration is
-- additive and safe on live data. The DROP makes re-runs deterministic for the
-- test harness (which drops schema_migrations and replays the chain); on a
-- live node the table does not exist yet so it is a no-op.
DROP TABLE IF EXISTS allocations CASCADE;
CREATE TABLE IF NOT EXISTS allocations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    ip TEXT NOT NULL DEFAULT '0.0.0.0',
    port INT NOT NULL CHECK (port BETWEEN 1024 AND 65535),
    kind TEXT NOT NULL DEFAULT 'game' CHECK (kind IN ('game', 'rcon')),
    workload_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (server_id, ip, port)
);

CREATE INDEX IF NOT EXISTS idx_alloc_server ON allocations (server_id);
CREATE INDEX IF NOT EXISTS idx_alloc_workload ON allocations (workload_id) WHERE workload_id IS NOT NULL;

-- Backfill from existing live instances (game + rcon) so nothing is lost.
-- JOIN servers guards against orphaned minecraft rows (test DBs replay the
-- chain after dropping servers, which would otherwise break the FK).
INSERT INTO allocations (server_id, ip, port, kind, workload_id)
SELECT mi.server_id, '0.0.0.0', mi.port, 'game', mi.id
FROM minecraft_instances mi
JOIN servers s ON s.id = mi.server_id
WHERE mi.status <> 'deleted'
ON CONFLICT (server_id, ip, port) DO NOTHING;

INSERT INTO allocations (server_id, ip, port, kind, workload_id)
SELECT mi.server_id, '0.0.0.0', mi.rcon_port, 'rcon', mi.id
FROM minecraft_instances mi
JOIN servers s ON s.id = mi.server_id
WHERE mi.status <> 'deleted'
ON CONFLICT (server_id, ip, port) DO NOTHING;
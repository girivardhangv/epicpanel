-- Soft-delete must not reserve names/ports forever. The original constraints
-- (0028) were hard UNIQUEs: they counted rows with status='deleted', so
-- deleting an instance kept its name and ports locked ("a minecraft instance
-- with this name already exists" after a delete — the reported regression).
-- Replace them with PARTIAL unique indexes that only cover live rows.
ALTER TABLE minecraft_instances DROP CONSTRAINT IF EXISTS minecraft_instances_organization_id_name_key;
ALTER TABLE minecraft_instances DROP CONSTRAINT IF EXISTS minecraft_instances_server_id_port_key;
ALTER TABLE minecraft_instances DROP CONSTRAINT IF EXISTS minecraft_instances_server_id_rcon_port_key;

CREATE UNIQUE INDEX IF NOT EXISTS uq_mc_org_name_live
    ON minecraft_instances (organization_id, name)
    WHERE status <> 'deleted';

CREATE UNIQUE INDEX IF NOT EXISTS uq_mc_server_port_live
    ON minecraft_instances (server_id, port)
    WHERE status <> 'deleted';

CREATE UNIQUE INDEX IF NOT EXISTS uq_mc_server_rcon_port_live
    ON minecraft_instances (server_id, rcon_port)
    WHERE status <> 'deleted';
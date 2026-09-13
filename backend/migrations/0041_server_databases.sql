-- Per-server databases (Pterodactyl parity).
--
-- Each Minecraft instance may own several databases (MySQL/Postgres) that the
-- node agent provisions. The engine/host is optional (database_host_id NULL):
-- the agent picks its local engine when the panel does not pin one. The
-- password is stored as ciphertext (password_enc) — never plaintext.
CREATE TABLE IF NOT EXISTS server_databases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id UUID NOT NULL REFERENCES minecraft_instances (id) ON DELETE CASCADE,
    database_host_id UUID,
    database_name TEXT NOT NULL,
    username TEXT NOT NULL,
    password_enc TEXT NOT NULL DEFAULT '',
    remote TEXT NOT NULL DEFAULT '%',
    max_connections INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (instance_id, database_name)
);

CREATE INDEX IF NOT EXISTS idx_server_db_instance ON server_databases (instance_id);
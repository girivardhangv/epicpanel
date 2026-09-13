-- Phase 16: Eggs & Nests (Pterodactyl parity) — server templating.
--
-- A NEST groups related EGGS. An EGG is the complete recipe to run a game
-- server: per-architecture Docker images, a startup command with {{VARIABLE}}
-- substitution, config-file templates, an install script run in its own
-- container, the stop command, and the user-editable variables.
--
-- Instances link to an egg (minecraft_instances.egg_id) and store their
-- resolved variable values in server_variables, mirroring Pterodactyl's
-- eggs / egg_variables / server_variables split.

CREATE TABLE IF NOT EXISTS nests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    author TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_nests_name ON nests (name);

CREATE TABLE IF NOT EXISTS eggs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    nest_id UUID NOT NULL REFERENCES nests (id) ON DELETE CASCADE,
    author TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    -- {"amd64": "ghcr.io/...", "arm64": "..."}
    docker_images JSONB NOT NULL DEFAULT '{}',
    startup TEXT NOT NULL DEFAULT '',
    -- [{"file":"server.properties","parser":"properties","replace":{...}}]
    config_files JSONB NOT NULL DEFAULT '[]',
    config_startup JSONB NOT NULL DEFAULT '{}',
    config_logs JSONB NOT NULL DEFAULT '{}',
    config_stop TEXT NOT NULL DEFAULT 'stop',
    config_from JSONB NOT NULL DEFAULT '{}',
    script_container TEXT NOT NULL DEFAULT '',
    script_entrypoint TEXT NOT NULL DEFAULT 'bash',
    install_script TEXT NOT NULL DEFAULT '',
    features JSONB NOT NULL DEFAULT '[]',
    file_denylist JSONB NOT NULL DEFAULT '[]',
    force_outgoing_ip BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (nest_id, name)
);
CREATE INDEX IF NOT EXISTS idx_eggs_nest ON eggs (nest_id);

CREATE TABLE IF NOT EXISTS egg_variables (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    egg_id UUID NOT NULL REFERENCES eggs (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    env_variable TEXT NOT NULL,
    default_value TEXT NOT NULL DEFAULT '',
    user_viewable BOOLEAN NOT NULL DEFAULT TRUE,
    user_editable BOOLEAN NOT NULL DEFAULT TRUE,
    rules TEXT NOT NULL DEFAULT 'required|string',
    kind TEXT NOT NULL DEFAULT 'string' CHECK (kind IN ('string','text','integer','boolean')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (egg_id, env_variable)
);
CREATE INDEX IF NOT EXISTS idx_egg_vars_egg ON egg_variables (egg_id);

CREATE TABLE IF NOT EXISTS mounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    target TEXT NOT NULL,
    read_only BOOLEAN NOT NULL DEFAULT FALSE,
    user_mountable BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (name)
);

CREATE TABLE IF NOT EXISTS mount_eggs (
    mount_id UUID NOT NULL REFERENCES mounts (id) ON DELETE CASCADE,
    egg_id UUID NOT NULL REFERENCES eggs (id) ON DELETE CASCADE,
    PRIMARY KEY (mount_id, egg_id)
);

CREATE TABLE IF NOT EXISTS mount_servers (
    mount_id UUID NOT NULL REFERENCES mounts (id) ON DELETE CASCADE,
    instance_id UUID NOT NULL REFERENCES minecraft_instances (id) ON DELETE CASCADE,
    PRIMARY KEY (mount_id, instance_id)
);

-- Instance -> egg link + per-instance variable values.
ALTER TABLE minecraft_instances
    ADD COLUMN IF NOT EXISTS egg_id UUID REFERENCES eggs (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_mc_egg ON minecraft_instances (egg_id) WHERE egg_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS server_variables (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_id UUID NOT NULL REFERENCES minecraft_instances (id) ON DELETE CASCADE,
    variable_id UUID NOT NULL REFERENCES egg_variables (id) ON DELETE CASCADE,
    value TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (instance_id, variable_id)
);
CREATE INDEX IF NOT EXISTS idx_server_vars_instance ON server_variables (instance_id);

-- Seed the Minecraft nest + vanilla egg so a fresh install has a usable
-- template without a manual import (idempotent for the test chain replay).
INSERT INTO nests (uuid, author, name, description)
VALUES (gen_random_uuid(), 'epicpanel', 'Minecraft', 'Minecraft Java Edition server providers')
ON CONFLICT (name) DO NOTHING;

INSERT INTO eggs (nest_id, author, name, description, docker_images, startup, config_stop, script_container, script_entrypoint)
SELECT n.id, 'epicpanel', 'Vanilla', 'Mojang vanilla Minecraft server',
       '{"amd64":"eclipse-temurin:21-jre-jammy"}'::jsonb,
       'java -Xms128M -Xmx{{SERVER_MEMORY}}M -jar {{SERVER_JARFILE}}',
       'stop', 'eclipse-temurin:21-jre-jammy', 'bash'
FROM nests n WHERE n.name = 'Minecraft'
ON CONFLICT (nest_id, name) DO NOTHING;

INSERT INTO egg_variables (egg_id, name, env_variable, default_value, user_viewable, user_editable, rules, kind)
SELECT e.id, 'Server Jar File', 'SERVER_JARFILE', 'server.jar', TRUE, TRUE, 'required|string', 'string'
FROM eggs e WHERE e.name = 'Vanilla'
ON CONFLICT (egg_id, env_variable) DO NOTHING;

INSERT INTO egg_variables (egg_id, name, env_variable, default_value, user_viewable, user_editable, rules, kind)
SELECT e.id, 'Server Memory', 'SERVER_MEMORY', '1024', FALSE, FALSE, 'required|integer', 'integer'
FROM eggs e WHERE e.name = 'Vanilla'
ON CONFLICT (egg_id, env_variable) DO NOTHING;
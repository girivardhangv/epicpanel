-- Phase 9: resource isolation (cPanel-style). Each website gets its own OS
-- user on Linux, its own PHP-FPM pool running as that user, site files owned
-- by that user, and optional per-site CPU/memory limits enforced via cgroups
-- (Linux) or Job Objects (Windows). Database isolation already exists (Phase 6
-- per-site DB users).

ALTER TABLE websites
    ADD COLUMN os_user TEXT NOT NULL DEFAULT '';  -- per-site OS user, e.g. web_<slug> (Linux only)

ALTER TABLE website_runtime_config
    ADD COLUMN cpu_limit_pct   INTEGER NOT NULL DEFAULT 0,  -- 0 = unlimited; 1..100 = CPU quota %
    ADD COLUMN memory_limit_mb INTEGER NOT NULL DEFAULT 0;  -- 0 = unlimited; >0 = memory ceiling MB

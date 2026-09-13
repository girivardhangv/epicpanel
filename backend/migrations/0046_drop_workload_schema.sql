-- Drop the first-class Minecraft/Discord workload schema.
--
-- The panel keeps its generic core (tenants/RBAC, host servers + agents,
-- websites, PHP/other runtimes incl. a plain Java runtime, certificates/DNS,
-- cron, backups-for-websites, billing). The Minecraft-instance and Discord-bot
-- subsystems are removed: their tables, the columns that pointed at them,
-- their status enums, and their seeded plan rows all go away here.
--
-- Migrations 0028-0034 that created this schema are committed and immutable,
-- so the leftover objects are torn down with a NEW migration (the whole chain
-- re-applies cleanly on a fresh/reset database). Every statement is re-runnable.

-- 1. Columns that reference the workload tables (must precede DROP TABLE).
ALTER TABLE subscriptions DROP COLUMN IF EXISTS bot_id;
ALTER TABLE subscriptions DROP COLUMN IF EXISTS instance_id;

ALTER TABLE jobs DROP COLUMN IF EXISTS minecraft_id;
ALTER TABLE jobs DROP COLUMN IF EXISTS bot_id;

ALTER TABLE backups DROP COLUMN IF EXISTS instance_id;
ALTER TABLE backups DROP COLUMN IF EXISTS bot_id;

-- 2. backup_schedules: drop the bot-scoped CHECK (it names bot_id), then the
--    column, then re-add a website-only invariant.
DO $$
DECLARE r record;
BEGIN
    FOR r IN
        SELECT con.conname
        FROM pg_constraint con
        WHERE con.conrelid = 'backup_schedules'::regclass
          AND con.contype = 'c'
          AND pg_get_constraintdef(con.oid) LIKE '%bot_id%'
    LOOP
        EXECUTE format('ALTER TABLE backup_schedules DROP CONSTRAINT %I', r.conname);
    END LOOP;
END $$;
ALTER TABLE backup_schedules DROP COLUMN IF EXISTS bot_id;
DO $$ BEGIN
    ALTER TABLE backup_schedules ADD CONSTRAINT backup_schedules_website_required
        CHECK (website_id IS NOT NULL);
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- 3. The workload tables themselves (CASCADE also removes their dependent
--    index/constraint objects).
DROP TABLE IF EXISTS minecraft_schedules CASCADE;
DROP TABLE IF EXISTS minecraft_world_backups CASCADE;
DROP TABLE IF EXISTS minecraft_instances CASCADE;
DROP TABLE IF EXISTS bot_schedules CASCADE;
DROP TABLE IF EXISTS bot_instances CASCADE;

-- 4. Status enums created alongside those tables.
DROP TYPE IF EXISTS mc_status;
DROP TYPE IF EXISTS bot_status;

-- 5. Backups: narrow the type CHECK to the surviving (website-oriented) set.
--    Legacy 'files'/'full' rows stay valid (0031 maps them to the new
--    vocabulary but the pre-Phase-11 flow still writes them). Only the
--    workload-specific 'minecraft_world'/'discord_bot' values are removed.
ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_type_chk;
ALTER TABLE backups DROP CONSTRAINT IF EXISTS backups_type_check;
DO $$ BEGIN
    ALTER TABLE backups ADD CONSTRAINT backups_type_chk CHECK (
        type IN ('files', 'full', 'account', 'database', 'website',
                 'full_instance', 'website_files'));
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- 6. Remove the seeded workload plans and restore a single web default.
--    Migration 0034 promoted a workload plan to be the platform default and
--    demoted the web default; with those plans gone we must re-establish a
--    web plan as the sole default so unassigned orgs resolve.
DELETE FROM hosting_packages WHERE kind IN ('minecraft', 'discord');

UPDATE hosting_packages SET is_default = FALSE;
UPDATE hosting_packages SET is_default = TRUE
    WHERE id = (
        SELECT id FROM hosting_packages
        WHERE kind = 'web'
        ORDER BY (name = 'Starter') DESC, created_at ASC
        LIMIT 1
    );

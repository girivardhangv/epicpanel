-- Setup tokens: one-time bootstrap links printed by the installer.
-- The first admin account can only be created with a valid token (1h TTL),
-- so attackers who find the panel URL cannot claim the panel.
CREATE TABLE IF NOT EXISTS setup_tokens (
    token_hash TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_setup_tokens_expiry ON setup_tokens (expires_at);

-- Live install progress: the agent streams stage updates while a job runs,
-- so the UI can show real activity instead of a fake percentage.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS progress INT NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS progress_step TEXT NOT NULL DEFAULT '';

-- Apache + OpenLiteSpeed as managed runtimes; relax version format so
-- major-only versions (node "22") and two-part (apache "2.4") both fit.
ALTER TABLE runtimes DROP CONSTRAINT IF EXISTS runtimes_type_check;
ALTER TABLE runtimes ADD CONSTRAINT runtimes_type_check
    CHECK (type IN ('php', 'node', 'python', 'go', 'apache', 'openlitespeed'));
ALTER TABLE runtimes DROP CONSTRAINT IF EXISTS runtimes_version_check;
ALTER TABLE runtimes ADD CONSTRAINT runtimes_version_check
    CHECK (version ~ '^[0-9]+(\.[0-9]+)?$');

-- PHP extension registry per installed runtime (toggled from the panel).
CREATE TABLE IF NOT EXISTS php_extensions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    runtime_id UUID NOT NULL REFERENCES runtimes (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'installing' CHECK (status IN ('installing', 'available', 'failed', 'removing')),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (runtime_id, name)
);

-- Per-site nginx directive snippets (rewrite rules + extra config).
CREATE TABLE IF NOT EXISTS website_configs (
    website_id UUID PRIMARY KEY REFERENCES websites (id) ON DELETE CASCADE,
    rewrite_rules TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- New job types (enum values cannot be used in the same transaction that
-- adds them, hence this lives in its own statement block at the end).
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'install_extension';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'remove_extension';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'detect_software';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'site_usage';

-- OLS extensions are per LSPHP version under the same runtime row.
ALTER TABLE php_extensions ADD COLUMN IF NOT EXISTS php_version TEXT NOT NULL DEFAULT '';
ALTER TABLE php_extensions DROP CONSTRAINT IF EXISTS php_extensions_runtime_id_name_key;
DO $$ BEGIN
    BEGIN
        ALTER TABLE php_extensions ADD CONSTRAINT php_extensions_runtime_id_name_php_version_key UNIQUE (runtime_id, name, php_version);
    EXCEPTION WHEN others THEN NULL; -- already exists
    END;
END $$;

-- Multi web server support: comma list, nginx always first (entry/TLS).
-- Values: 'nginx', 'nginx,apache', 'nginx,openlitespeed'.
ALTER TABLE websites ADD COLUMN IF NOT EXISTS web_servers TEXT NOT NULL DEFAULT 'nginx';

-- Persisted backend port for proxy modes (nginx,apache / nginx,openlitespeed).
-- Allocated by the control plane (unique per server), released on delete.
ALTER TABLE websites ADD COLUMN IF NOT EXISTS backend_port INT NOT NULL DEFAULT 0;

-- Allow the nginx-based multi web server modes in the original check.
ALTER TABLE websites DROP CONSTRAINT IF EXISTS websites_web_server_check;
DO $$ BEGIN
    BEGIN
        ALTER TABLE websites ADD CONSTRAINT websites_web_server_check
            CHECK (web_server IN ('nginx', 'nginx,apache', 'nginx,openlitespeed', 'apache', 'openlitespeed', 'none'));
    EXCEPTION WHEN others THEN NULL; -- already updated
    END;
END $$;

-- Per-site document-root override (relative to the site tree, e.g.
-- "public" for Laravel layouts). Empty = the standard public dir.
ALTER TABLE websites ADD COLUMN IF NOT EXISTS docroot_suffix TEXT NOT NULL DEFAULT '';

-- WordPress one-click flow marker on its auto-created database.
ALTER TABLE databases ADD COLUMN IF NOT EXISTS purpose TEXT NOT NULL DEFAULT '';

-- Pending WordPress installs: the install job chains off the database-ready
-- event and needs the admin details chosen in the panel UI.
CREATE TABLE IF NOT EXISTS wp_pending (
    db_id UUID PRIMARY KEY REFERENCES databases (id) ON DELETE CASCADE,
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    admin_user TEXT NOT NULL,
    admin_email TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Packages rework: strict memory/CPU limits, domain counting, checkbox lists.
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS memory_limit_mb INT NOT NULL DEFAULT 256;
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS cpu_cores NUMERIC(4,2) NOT NULL DEFAULT 1.00;
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS max_addon_domains INT NOT NULL DEFAULT 1;
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS max_subdomains INT NOT NULL DEFAULT 2;
-- legacy fpm fields retired (kept NOT NULL defaults so old rows survive)
ALTER TABLE hosting_packages ALTER COLUMN fpm_memory_limit SET DEFAULT '128M';
-- per-domain docroot override (addon domains can point at any site path)
ALTER TABLE domains ADD COLUMN IF NOT EXISTS docroot_suffix TEXT NOT NULL DEFAULT '';

UPDATE hosting_packages SET
  memory_limit_mb = 256, cpu_cores = 1.00, max_addon_domains = 2, max_subdomains = 3
WHERE name = 'Starter';
UPDATE hosting_packages SET
  memory_limit_mb = 512, cpu_cores = 2.00, max_addon_domains = 20, max_subdomains = 25
WHERE name = 'Pro';
UPDATE hosting_packages SET
  memory_limit_mb = 1024, cpu_cores = 4.00, max_addon_domains = 200, max_subdomains = 250
WHERE name = 'Enterprise';

-- Per-domain docroot override: addon/sub domains may serve any relative path
-- under the site tree instead of the site's default running directory.
ALTER TABLE domains ADD COLUMN IF NOT EXISTS docroot_suffix TEXT NOT NULL DEFAULT '';

-- Latest per-site resource usage snapshot (from site_usage jobs).
ALTER TABLE websites ADD COLUMN IF NOT EXISTS usage_cpu_percent DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE websites ADD COLUMN IF NOT EXISTS usage_memory_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE websites ADD COLUMN IF NOT EXISTS usage_disk_mb BIGINT NOT NULL DEFAULT 0;
ALTER TABLE websites ADD COLUMN IF NOT EXISTS usage_processes INT NOT NULL DEFAULT 0;
ALTER TABLE websites ADD COLUMN IF NOT EXISTS usage_sampled_at TIMESTAMPTZ;

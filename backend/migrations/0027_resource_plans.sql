-- Phase 9: unified resource & limit engine.
-- Plans are pure data: one row in hosting_packages + the resource matrix in
-- the typed quota columns. Adding a new plan changes only data, never code.
-- Verbatim plan names and the Minecraft 4GB matrix come from the master doc.

-- 1. Resource matrix columns (everything the legacy columns cannot express).
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'web'
    CHECK (kind IN ('web', 'minecraft', 'discord'));
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS max_bandwidth_mb BIGINT NOT NULL DEFAULT 0; -- 0 = unlimited
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS io_weight INT NOT NULL DEFAULT 0 CHECK (io_weight BETWEEN 0 AND 10000);
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS php_max_children INT NOT NULL DEFAULT 0 CHECK (php_max_children BETWEEN 0 AND 1000);
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS max_processes INT NOT NULL DEFAULT 0; -- 0 = unlimited
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS max_ports INT NOT NULL DEFAULT 0;
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS max_backups INT NOT NULL DEFAULT 0;
ALTER TABLE hosting_packages ADD COLUMN IF NOT EXISTS max_email_accounts INT NOT NULL DEFAULT 0;

-- 2. The 7 verbatim plans. Existing Starter/Pro rows are updated onto the
-- matrix; Business + Minecraft + Discord rows are created.
INSERT INTO hosting_packages
    (name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
     max_addon_domains, max_subdomains, allowed_runtimes, price_monthly_cents, is_default,
     max_bandwidth_mb, io_weight, php_max_children, max_processes, max_ports, max_backups, max_email_accounts)
VALUES
    ('Starter', 'web', 3, 2, 2048, 512, 1.00, 2, 3, '{static,php}', 500, FALSE,
     102400, 100, 16, 64, 0, 2, 5)
ON CONFLICT (name) DO UPDATE SET
    kind = 'web', max_bandwidth_mb = 102400, io_weight = 100, php_max_children = 16,
    max_processes = 64, max_ports = 0, max_backups = 2, max_email_accounts = 5,
    memory_limit_mb = GREATEST(hosting_packages.memory_limit_mb, 512),
    updated_at = now();

INSERT INTO hosting_packages
    (name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
     max_addon_domains, max_subdomains, allowed_runtimes, price_monthly_cents, is_default,
     max_bandwidth_mb, io_weight, php_max_children, max_processes, max_ports, max_backups, max_email_accounts)
VALUES
    ('Business', 'web', 100, 100, 102400, 2048, 4.00, 200, 250, '{static,php,node,python,go}', 2500, FALSE,
     5242880, 300, 64, 256, 0, 7, 200)
ON CONFLICT (name) DO NOTHING;

INSERT INTO hosting_packages
    (name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
     max_addon_domains, max_subdomains, allowed_runtimes, price_monthly_cents, is_default,
     max_bandwidth_mb, io_weight, php_max_children, max_processes, max_ports, max_backups, max_email_accounts)
VALUES
    ('Minecraft 2GB', 'minecraft', 1, 0, 20480, 2048, 2.00, 0, 0, '{minecraft}', 1000, FALSE,
     2097152, 200, 1, 128, 1, 3, 0)
ON CONFLICT (name) DO NOTHING;

-- Minecraft 4GB — verbatim from the master doc:
--   RAM: 4096 MB, CPU: 200%, Disk: 20 GB, Bandwidth: 2 TB, Ports: 1, Backups: 3
INSERT INTO hosting_packages
    (name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
     max_addon_domains, max_subdomains, allowed_runtimes, price_monthly_cents, is_default,
     max_bandwidth_mb, io_weight, php_max_children, max_processes, max_ports, max_backups, max_email_accounts)
VALUES
    ('Minecraft 4GB', 'minecraft', 1, 0, 20480, 4096, 2.00, 0, 0, '{minecraft}', 2000, FALSE,
     2097152, 200, 1, 256, 1, 3, 0)
ON CONFLICT (name) DO NOTHING;

INSERT INTO hosting_packages
    (name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
     max_addon_domains, max_subdomains, allowed_runtimes, price_monthly_cents, is_default,
     max_bandwidth_mb, io_weight, php_max_children, max_processes, max_ports, max_backups, max_email_accounts)
VALUES
    ('Discord Basic', 'discord', 1, 0, 2048, 512, 0.50, 0, 0, '{node,python}', 300, FALSE,
     524288, 100, 1, 16, 0, 0, 0)
ON CONFLICT (name) DO NOTHING;

INSERT INTO hosting_packages
    (name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
     max_addon_domains, max_subdomains, allowed_runtimes, price_monthly_cents, is_default,
     max_bandwidth_mb, io_weight, php_max_children, max_processes, max_ports, max_backups, max_email_accounts)
VALUES
    ('Discord Pro', 'discord', 1, 0, 5120, 1024, 1.00, 0, 0, '{node,python}', 700, FALSE,
     1048576, 100, 1, 32, 1, 1, 0)
ON CONFLICT (name) DO NOTHING;

-- Pro: update the existing row onto the matrix (keep its price/default).
UPDATE hosting_packages SET
    kind = 'web', max_bandwidth_mb = 1048576, io_weight = 200, php_max_children = 32,
    max_processes = 128, max_ports = 0, max_backups = 3, max_email_accounts = 50,
    memory_limit_mb = GREATEST(memory_limit_mb, 1024)
WHERE name = 'Pro';

-- 3. Bandwidth period accounting (RX+TX counters per account per month).
CREATE TABLE IF NOT EXISTS workload_resource_usage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    resource TEXT NOT NULL,
    period_start DATE NOT NULL,
    used NUMERIC NOT NULL DEFAULT 0,
    limit_value NUMERIC NOT NULL DEFAULT 0,
    unit TEXT NOT NULL DEFAULT '',
    measured_by TEXT NOT NULL DEFAULT '', -- agent mechanism (nftables/tc/xfs_quota/...)
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (website_id, resource, period_start)
);

-- IF NOT EXISTS everywhere: test harnesses drop schema_migrations but can
-- leave the table behind; the migration must be re-runnable.
CREATE INDEX IF NOT EXISTS idx_wru_org_period ON workload_resource_usage (organization_id, period_start);

-- 4. Agent enforcement job.
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'enforce_limits';

CREATE TABLE hosting_packages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 60),
    max_websites INT NOT NULL DEFAULT 1 CHECK (max_websites BETWEEN 0 AND 1000),
    max_databases INT NOT NULL DEFAULT 1 CHECK (max_databases BETWEEN 0 AND 1000),
    max_disk_mb INT NOT NULL DEFAULT 1024 CHECK (max_disk_mb BETWEEN 16 AND 1048576),
    fpm_memory_limit TEXT NOT NULL DEFAULT '128M' CHECK (fpm_memory_limit ~ '^[0-9]+[MG]$'),
    fpm_max_children INT NOT NULL DEFAULT 5 CHECK (fpm_max_children BETWEEN 1 AND 100),
    allowed_runtimes TEXT[] NOT NULL DEFAULT '{static,php}',
    price_monthly_cents INT NOT NULL DEFAULT 0 CHECK (price_monthly_cents >= 0),
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE organizations ADD COLUMN package_id UUID REFERENCES hosting_packages (id) ON DELETE SET NULL;

INSERT INTO hosting_packages (name, max_websites, max_databases, max_disk_mb, fpm_memory_limit, fpm_max_children, allowed_runtimes, is_default)
VALUES ('Starter', 3, 2, 2048, '128M', 5, '{static,php}', TRUE)
ON CONFLICT (name) DO NOTHING;
INSERT INTO hosting_packages (name, max_websites, max_databases, max_disk_mb, fpm_memory_limit, fpm_max_children, allowed_runtimes, price_monthly_cents)
VALUES ('Pro', 20, 20, 20480, '256M', 15, '{static,php,node,python}', 999)
ON CONFLICT (name) DO NOTHING;
INSERT INTO hosting_packages (name, max_websites, max_databases, max_disk_mb, fpm_memory_limit, fpm_max_children, allowed_runtimes, price_monthly_cents)
VALUES ('Enterprise', 1000, 1000, 1048576, '512M', 50, '{static,php,node,python,go}', 4999)
ON CONFLICT (name) DO NOTHING;

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'apply_quota';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'install_database_tools';

ALTER TABLE runtimes DROP CONSTRAINT IF EXISTS runtimes_version_check;
ALTER TABLE runtimes ADD CONSTRAINT runtimes_version_check CHECK (version ~ '^[0-9]+\.[0-9]+(\.[0-9]+)?$');

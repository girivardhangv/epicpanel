-- Phase 2: website hosting engine.
-- Adds server manageability metadata (agent ops endpoint), single-use
-- registration tokens, the domains/websites model, runtime configuration and
-- the provisioning jobs table. New granular permissions are seeded and granted
-- to the super_admin role.

-- ---------------------------------------------------------------------------
-- Servers: panel -> agent management channel
-- ---------------------------------------------------------------------------
ALTER TABLE servers ADD COLUMN agent_url    TEXT NOT NULL DEFAULT ''; -- advertised ops base URL, e.g. http://10.0.0.5:9200
ALTER TABLE servers ADD COLUMN ops_token    TEXT NOT NULL DEFAULT ''; -- shared secret the agent requires on every ops call; never returned by any API
ALTER TABLE servers ADD COLUMN capabilities JSONB NOT NULL DEFAULT '{}'::jsonb; -- last probed nginx/php matrix

-- ---------------------------------------------------------------------------
-- Registration tokens (replace the never-expiring shared settings key)
-- ---------------------------------------------------------------------------
CREATE TABLE server_registration_tokens (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash      TEXT NOT NULL UNIQUE,       -- SHA-256 of the plaintext token; plaintext shown exactly once
    label           TEXT NOT NULL DEFAULT '',
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    used_at         TIMESTAMPTZ,
    used_by_server  UUID REFERENCES servers(id) ON DELETE SET NULL,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX server_registration_tokens_active_idx
    ON server_registration_tokens (created_at DESC)
    WHERE used_at IS NULL AND revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Domains
-- ---------------------------------------------------------------------------
CREATE TABLE domains (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id   UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    domain      TEXT NOT NULL,                  -- normalized lowercase FQDN, optionally leftmost wildcard
    type        TEXT NOT NULL CHECK (type IN ('primary', 'alias', 'subdomain')),
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending', 'error')),
    website_id  UUID,                           -- set for aliases attached to a website (FK added below)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX domains_server_domain_key ON domains (server_id, domain);
CREATE INDEX domains_website_idx ON domains (website_id) WHERE website_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- Websites
-- ---------------------------------------------------------------------------
CREATE TABLE websites (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id      UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    domain_id      UUID NOT NULL REFERENCES domains(id) ON DELETE RESTRICT,
    name           TEXT NOT NULL DEFAULT '',
    document_root  TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'provisioning'
                   CHECK (status IN ('provisioning', 'active', 'disabled', 'error', 'deleting')),
    php_version    TEXT NOT NULL DEFAULT '',
    web_server     TEXT NOT NULL DEFAULT 'nginx',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX websites_domain_key ON websites (domain_id); -- one website per domain
CREATE INDEX websites_server_idx ON websites (server_id);
CREATE INDEX websites_status_idx ON websites (status);

ALTER TABLE domains
    ADD CONSTRAINT domains_website_fk FOREIGN KEY (website_id) REFERENCES websites(id) ON DELETE SET NULL;

-- ---------------------------------------------------------------------------
-- Per-website runtime configuration (PHP settings + generated artifacts)
-- ---------------------------------------------------------------------------
CREATE TABLE website_runtime_config (
    website_id         UUID PRIMARY KEY REFERENCES websites(id) ON DELETE CASCADE,
    php_settings       JSONB NOT NULL DEFAULT '{}'::jsonb,  -- validated subset: memory_limit, upload_max_filesize, ...
    php_address        TEXT NOT NULL DEFAULT '',            -- unix socket (linux) or 127.0.0.1:port (windows)
    nginx_config_name  TEXT NOT NULL DEFAULT '',            -- site file name managed by the agent
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Provisioning / lifecycle jobs
-- ---------------------------------------------------------------------------
CREATE TABLE jobs (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    type         TEXT NOT NULL CHECK (type IN ('provision_website', 'reconfigure_website', 'delete_website')),
    status       TEXT NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    server_id    UUID REFERENCES servers(id) ON DELETE SET NULL,
    website_id   UUID REFERENCES websites(id) ON DELETE SET NULL,
    progress     INTEGER NOT NULL DEFAULT 0,      -- 0..100
    message      TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    payload      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at   TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);
CREATE INDEX jobs_status_idx   ON jobs (status) WHERE status IN ('queued', 'running');
CREATE INDEX jobs_website_idx  ON jobs (website_id);

-- ---------------------------------------------------------------------------
-- Phase 2 permissions
-- ---------------------------------------------------------------------------
INSERT INTO permissions (code, description) VALUES
    ('servers.create',          'Create registration tokens and connect servers'),
    ('servers.delete',          'Remove servers and registration tokens'),
    ('domains.view',            'View domains'),
    ('domains.create',          'Create domains'),
    ('domains.manage',          'Manage domains'),
    ('domains.delete',          'Delete domains'),
    ('websites.logs.view',      'View website access and error logs'),
    ('websites.php.manage',     'Change the PHP runtime of websites'),
    ('websites.config.manage',  'Manage website aliases and PHP settings')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.name = 'super_admin'
  AND p.code IN ('servers.create', 'servers.delete',
                 'domains.view', 'domains.create', 'domains.manage', 'domains.delete',
                 'websites.logs.view', 'websites.php.manage', 'websites.config.manage')
ON CONFLICT DO NOTHING;

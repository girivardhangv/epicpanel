-- Phase 6: managed databases (MySQL/MariaDB + PostgreSQL).
-- The panel records database + user metadata; the actual DDL runs on the
-- managed server through the agent's typed db operations. Database user
-- passwords are NEVER stored — generated once, shown once, resettable.

CREATE TABLE databases (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id   UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    website_id  UUID REFERENCES websites(id) ON DELETE SET NULL,
    engine      TEXT NOT NULL CHECK (engine IN ('mysql', 'postgres')),
    name        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'provisioning'
                CHECK (status IN ('provisioning', 'active', 'error', 'deleting')),
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- A database name is unique per engine per server.
CREATE UNIQUE INDEX databases_server_engine_name_key ON databases (server_id, engine, name);
CREATE INDEX databases_website_idx ON databases (website_id) WHERE website_id IS NOT NULL;

CREATE TABLE database_users (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    database_id  UUID NOT NULL REFERENCES databases(id) ON DELETE CASCADE,
    username     TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'provisioning'
                 CHECK (status IN ('provisioning', 'active', 'error')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (database_id, username)
);

-- Jobs accepts the new database job types.
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check CHECK (type IN (
    'provision_website', 'reconfigure_website', 'delete_website',
    'issue_ssl', 'notify_alert',
    'provision_database', 'delete_database'
));

-- Granular database permissions (databases.view/create/delete seeded in 0001).
INSERT INTO permissions (code, description) VALUES
    ('databases.manage',       'Manage databases (users, passwords, grants)'),
    ('databases.users.manage', 'Create and remove database users')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.name = 'super_admin'
  AND p.code IN ('databases.view', 'databases.create', 'databases.delete',
                 'databases.manage', 'databases.users.manage')
ON CONFLICT DO NOTHING;

-- EpicPanel foundation schema.
-- Targets PostgreSQL >= 13 (gen_random_uuid built-in).

CREATE TABLE users (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username            TEXT NOT NULL,
    email               TEXT,
    display_name        TEXT NOT NULL DEFAULT '',
    password_hash       TEXT NOT NULL,           -- PHC-formatted Argon2id string
    is_active           BOOLEAN NOT NULL DEFAULT TRUE,
    failed_login_count  INTEGER NOT NULL DEFAULT 0,
    locked_until        TIMESTAMPTZ,
    last_login_at       TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_username_lower_key ON users ((lower(username)));
CREATE UNIQUE INDEX users_email_lower_key    ON users ((lower(email))) WHERE email IS NOT NULL AND email <> '';

CREATE TABLE roles (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    is_system   BOOLEAN NOT NULL DEFAULT FALSE, -- system roles cannot be deleted
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE permissions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE user_roles (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id    UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    granted_by UUID REFERENCES users(id) ON DELETE SET NULL,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);
CREATE INDEX user_roles_role_idx ON user_roles (role_id);

CREATE TABLE role_permissions (
    role_id       UUID NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_id UUID NOT NULL REFERENCES permissions(id) ON DELETE CASCADE,
    granted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (role_id, permission_id)
);
CREATE INDEX role_permissions_permission_idx ON role_permissions (permission_id);

CREATE TABLE sessions (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash    TEXT NOT NULL UNIQUE,      -- SHA-256 of the opaque cookie token
    csrf_token    TEXT NOT NULL,
    ip_address    TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    revoked_at    TIMESTAMPTZ,
    revoked_reason TEXT
);
CREATE INDEX sessions_user_idx    ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE installations (
    id           INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1), -- single-row table
    status       TEXT NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'completed')),
    instance_id  UUID NOT NULL,              -- persistent identity used by licensing & agents
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    panel_version TEXT NOT NULL DEFAULT ''
);

CREATE TABLE licenses (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    installation_id   INTEGER NOT NULL DEFAULT 1 UNIQUE REFERENCES installations(id),
    license_key_hint  TEXT NOT NULL DEFAULT '',  -- e.g. 'XXXX-…-3F21'; never the full key
    external_license_id  TEXT NOT NULL DEFAULT '',  -- id assigned by the licensing server
    plan              TEXT NOT NULL DEFAULT '',
    seats             INTEGER,
    status            TEXT NOT NULL
                      CHECK (status IN ('active', 'grace', 'expired', 'suspended', 'invalid', 'inactive'))
                      DEFAULT 'inactive',
    features          JSONB NOT NULL DEFAULT '{}'::jsonb,
    activation_fingerprint TEXT NOT NULL DEFAULT '',
    activated_at      TIMESTAMPTZ,
    last_validated_at TIMESTAMPTZ,
    expires_at        TIMESTAMPTZ,
    raw_payload       JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE audit_logs (
    id          BIGSERIAL PRIMARY KEY,
    actor_type  TEXT NOT NULL CHECK (actor_type IN ('user', 'system', 'agent', 'installer')) DEFAULT 'system',
    actor_id    UUID,
    actor_label TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    resource    TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    ip_address  TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',
    metadata    JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_logs_created_idx ON audit_logs (created_at DESC);
CREATE INDEX audit_logs_action_idx  ON audit_logs (action);
CREATE INDEX audit_logs_actor_idx   ON audit_logs (actor_id) WHERE actor_id IS NOT NULL;

CREATE TABLE system_settings (
    key         TEXT PRIMARY KEY,
    value       JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE servers (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    label          TEXT NOT NULL DEFAULT '',
    hostname       TEXT NOT NULL,
    os             TEXT NOT NULL,
    os_version     TEXT NOT NULL DEFAULT '',
    arch           TEXT NOT NULL,
    agent_version  TEXT NOT NULL DEFAULT '',
    specs          JSONB NOT NULL DEFAULT '{}'::jsonb,  -- cpu, ram, disk, network snapshots
    agent_token_hash TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'offline'
                   CHECK (status IN ('online', 'offline', 'revoked')),
    registered_ip  TEXT NOT NULL DEFAULT '',
    registered_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at   TIMESTAMPTZ
);
CREATE UNIQUE INDEX servers_agent_token_key ON servers (agent_token_hash);
CREATE INDEX servers_last_seen_idx ON servers (last_seen_at);

-- Seed permissions defined in the product RBAC charter. New modules append
-- their own codes via later migrations rather than mutating this list ad hoc.
INSERT INTO permissions (code, description) VALUES
    ('dashboard.view',      'View the dashboard'),
    ('server.view',         'View registered servers'),
    ('server.manage',       'Register, edit and revoke servers'),
    ('users.view',          'View panel users'),
    ('users.create',        'Create panel users'),
    ('users.edit',          'Edit panel users'),
    ('users.delete',        'Delete panel users'),
    ('roles.view',          'View roles'),
    ('roles.create',        'Create roles'),
    ('roles.edit',          'Edit roles'),
    ('roles.delete',        'Delete roles'),
    ('websites.view',       'View websites'),
    ('websites.create',     'Create websites'),
    ('websites.edit',       'Edit websites'),
    ('websites.delete',     'Delete websites'),
    ('databases.view',      'View databases'),
    ('databases.create',    'Create databases'),
    ('databases.delete',    'Delete databases'),
    ('settings.view',       'View settings'),
    ('settings.manage',     'Change settings'),
    ('license.view',        'View license status'),
    ('license.manage',      'Activate and manage the license')
ON CONFLICT (code) DO NOTHING;

-- System roles. The super_admin role receives every seeded permission; the
-- first administrator created by the installer is granted this role through
-- the standard RBAC machinery.
INSERT INTO roles (name, description, is_system) VALUES
    ('super_admin', 'Unrestricted administrative access (first administrator)', TRUE)
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE r.name = 'super_admin'
ON CONFLICT DO NOTHING;

INSERT INTO roles (name, description, is_system) VALUES
    ('operator', 'Day-to-day server operations without license or settings control', TRUE),
    ('viewer',   'Read-only access to permitted areas', TRUE)
ON CONFLICT (name) DO NOTHING;

INSERT INTO installations (id, status, instance_id)
VALUES (1, 'pending', gen_random_uuid())
ON CONFLICT (id) DO NOTHING;

CREATE TYPE db_engine AS ENUM ('mysql', 'mariadb', 'postgresql');
CREATE TYPE db_status AS ENUM ('pending', 'creating', 'ready', 'failed', 'deleting');

CREATE TABLE databases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    website_id UUID REFERENCES websites (id) ON DELETE SET NULL,
    engine db_engine NOT NULL,
    name TEXT NOT NULL CHECK (name ~ '^ep_[a-z0-9_]{1,60}$'),
    db_user TEXT NOT NULL CHECK (db_user ~ '^ep_[a-z0-9_]{1,60}$'),
    password_encrypted BYTEA,
    password_fingerprint TEXT NOT NULL DEFAULT '',
    status db_status NOT NULL DEFAULT 'pending',
    error_message TEXT NOT NULL DEFAULT '',
    created_by UUID NOT NULL REFERENCES users (id),
    provisioned_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (server_id, engine, name)
);

CREATE INDEX idx_databases_org ON databases (organization_id);
CREATE INDEX idx_databases_server ON databases (server_id, engine);

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'create_database';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'delete_database';

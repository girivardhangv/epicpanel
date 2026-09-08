CREATE TABLE websites (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$'),
    primary_domain TEXT NOT NULL DEFAULT '',
    runtime TEXT NOT NULL DEFAULT 'static' CHECK (runtime IN ('static', 'php', 'node', 'python', 'go')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'provisioning', 'ready', 'failed', 'deleting', 'deleted')),
    unix_user TEXT NOT NULL DEFAULT '',
    document_root TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_by UUID NOT NULL REFERENCES users (id),
    provisioned_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (server_id, name)
);

CREATE INDEX idx_websites_org ON websites (organization_id);
CREATE INDEX idx_websites_server ON websites (server_id);

CREATE TYPE job_type AS ENUM ('provision_website', 'delete_website');
CREATE TYPE job_status AS ENUM ('pending', 'running', 'success', 'failed');

CREATE TABLE jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    website_id UUID REFERENCES websites (id) ON DELETE SET NULL,
    type job_type NOT NULL,
    status job_status NOT NULL DEFAULT 'pending',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    result JSONB NOT NULL DEFAULT '{}'::jsonb,
    error TEXT NOT NULL DEFAULT '',
    attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts INT NOT NULL DEFAULT 3,
    claimed_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_jobs_server_status ON jobs (server_id, status);
CREATE INDEX idx_jobs_website ON jobs (website_id);

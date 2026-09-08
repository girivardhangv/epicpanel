ALTER TABLE websites ADD COLUMN runtime_version TEXT NOT NULL DEFAULT '';

CREATE TABLE runtimes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK (type IN ('php', 'node', 'python', 'go')),
    version TEXT NOT NULL CHECK (version ~ '^[0-9]+\.[0-9]+$'),
    status TEXT NOT NULL DEFAULT 'installing' CHECK (status IN ('installing', 'available', 'failed', 'removing')),
    error_message TEXT NOT NULL DEFAULT '',
    created_by UUID NOT NULL REFERENCES users (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (server_id, type, version)
);

CREATE INDEX idx_runtimes_server ON runtimes (server_id, type);

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'install_runtime';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'remove_runtime';

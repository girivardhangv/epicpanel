CREATE TABLE ssh_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    public_key TEXT NOT NULL CHECK (length(public_key) BETWEEN 80 AND 4000),
    fingerprint TEXT NOT NULL,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (website_id, fingerprint)
);

CREATE INDEX idx_ssh_keys_website ON ssh_keys (website_id);

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'sync_ssh_keys';

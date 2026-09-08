CREATE TABLE api_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    created_by UUID REFERENCES users (id) ON DELETE SET NULL,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token_hash TEXT NOT NULL UNIQUE,
    scopes TEXT[] NOT NULL DEFAULT '{}',
    last_used_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_api_tokens_org ON api_tokens (organization_id);

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE CHECK (email = lower(email)),
    password_hash TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    is_platform_admin BOOLEAN NOT NULL DEFAULT FALSE,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE CHECK (slug ~ '^([a-z0-9]{2}|[a-z0-9][a-z0-9-]{1,61}[a-z0-9])$'),
    created_by UUID NOT NULL REFERENCES users (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TYPE org_role AS ENUM ('owner', 'admin', 'developer', 'billing', 'support');

CREATE TABLE organization_members (
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role org_role NOT NULL DEFAULT 'developer',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, user_id)
);

CREATE TABLE sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    user_agent TEXT NOT NULL DEFAULT '',
    ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ
);

CREATE TABLE audit_logs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    organization_id UUID REFERENCES organizations (id) ON DELETE SET NULL,
    actor_user_id UUID REFERENCES users (id) ON DELETE SET NULL,
    actor_type TEXT NOT NULL DEFAULT 'user' CHECK (actor_type IN ('user', 'system', 'api_token')),
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    result TEXT NOT NULL DEFAULT 'success' CHECK (result IN ('success', 'failure')),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    ip TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_user_id ON sessions (user_id);
CREATE INDEX idx_audit_logs_org_created ON audit_logs (organization_id, created_at DESC);
CREATE INDEX idx_org_members_user ON organization_members (user_id);

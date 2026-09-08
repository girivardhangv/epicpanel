-- Phase 2: Identity + RBAC v2 foundation.
-- Role mapping (master doc -> existing org_role, no grants dropped):
--   Super Admin = users.is_platform_admin, Admin = admin, Support = support,
--   Reseller = reseller (new), Customer = owner. developer/billing remain.

ALTER TYPE org_role ADD VALUE IF NOT EXISTS 'reseller';

-- 2FA foundation: TOTP secret encrypted at rest (secretbox), challenge tokens
-- for the password->code login step, single-use hashed recovery codes.
ALTER TABLE users ADD COLUMN mfa_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN totp_secret_encrypted TEXT NOT NULL DEFAULT '';

CREATE TABLE mfa_challenges (
    token_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE recovery_codes (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash TEXT NOT NULL UNIQUE,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_recovery_codes_user_active ON recovery_codes (user_id) WHERE used_at IS NULL;

-- Service accounts: org-owned machine principals backed by an api token.
CREATE TABLE service_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (name ~ '^[a-zA-Z0-9][a-zA-Z0-9 ._-]{0,99}$'),
    created_by UUID REFERENCES users (id) ON DELETE SET NULL,
    token_id UUID,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

ALTER TABLE api_tokens ADD COLUMN kind TEXT NOT NULL DEFAULT 'user' CHECK (kind IN ('user', 'service'));
ALTER TABLE api_tokens ADD COLUMN service_account_id UUID REFERENCES service_accounts (id) ON DELETE CASCADE;

-- System actors legitimately have no user row (scheduled backups, runtime
-- adoption, WordPress auto-DB): relax the NOT NULL the audit flagged.
ALTER TABLE runtimes ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE databases ALTER COLUMN created_by DROP NOT NULL;

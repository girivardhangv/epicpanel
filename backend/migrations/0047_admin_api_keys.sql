-- Platform admin API keys (epa_): machine principals for full-panel
-- automation. Unlike org-confined epk_ tokens (ADR-027), these keys act as
-- the platform admin across all organizations — the API counterpart of an
-- admin session — but remain scope-checked (admin:read / admin:write for the
-- admin surface, org scopes for /v1/organizations/... routes), hashed at
-- rest, revocable and expirable. Creation/revocation stay session-only so a
-- leaked key can never mint or revoke other keys.
CREATE TABLE IF NOT EXISTS admin_api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    scopes TEXT[] NOT NULL DEFAULT '{}',
    last_used_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

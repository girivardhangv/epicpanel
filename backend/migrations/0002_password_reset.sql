-- Password reset machinery. Delivery (SMTP or other channel) ships in a later
-- phase; the token lifecycle is production-grade from day one so the panel can
-- recover from lost administrator credentials via server-side provisioning.

CREATE TABLE password_reset_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL UNIQUE,      -- SHA-256 of the opaque token
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    requested_ip TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX prt_user_idx    ON password_reset_tokens (user_id);
CREATE INDEX prt_expires_idx ON password_reset_tokens (expires_at);

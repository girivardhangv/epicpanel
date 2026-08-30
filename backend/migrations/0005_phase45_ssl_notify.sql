-- Phase 4 + 5: SSL/ACME certificates and alert notification channels.

-- Jobs table accepts the new Phase 4/5 job types.
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check CHECK (type IN (
    'provision_website', 'reconfigure_website', 'delete_website',
    'issue_ssl', 'notify_alert'
));

-- ---------------------------------------------------------------------------
-- Website TLS certificates. The panel stores certificate metadata issued by
-- the agent (Let's Encrypt via ACME, or mock/self-signed in development);
-- the actual PEM material lives on the server under the agent's certs dir.
-- ---------------------------------------------------------------------------
CREATE TABLE website_certificates (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id   UUID NOT NULL REFERENCES websites(id) ON DELETE CASCADE,
    provider     TEXT NOT NULL DEFAULT 'acme' CHECK (provider IN ('acme', 'mock')),
    domains      JSONB NOT NULL DEFAULT '[]'::jsonb,
    status       TEXT NOT NULL DEFAULT 'issued' CHECK (status IN ('issuing', 'issued', 'error', 'removed')),
    cert_path    TEXT NOT NULL DEFAULT '',
    key_path     TEXT NOT NULL DEFAULT '',
    auto_renew   BOOLEAN NOT NULL DEFAULT TRUE,
    issued_at    TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (website_id)
);
CREATE INDEX website_certificates_expiry_idx ON website_certificates (expires_at)
    WHERE status = 'issued' AND auto_renew;

-- ---------------------------------------------------------------------------
-- Notification channels (Phase 5). Delivery supports webhook (generic,
-- including Slack/Discord incoming webhooks) and SMTP email. Channel config
-- is stored as bounded JSONB; secrets are stored here because the operator
-- supplies them — they are never returned by the API.
-- ---------------------------------------------------------------------------
CREATE TABLE notification_channels (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    type        TEXT NOT NULL CHECK (type IN ('webhook', 'slack', 'discord', 'email')),
    config      JSONB NOT NULL DEFAULT '{}'::jsonb,
    severity    TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('warning', 'critical')),
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notification_channels_enabled_idx ON notification_channels (enabled);

CREATE TYPE domain_kind AS ENUM ('primary', 'alias');
CREATE TYPE ssl_mode AS ENUM ('none', 'selfsigned', 'letsencrypt');
CREATE TYPE ssl_state AS ENUM ('pending', 'issuing', 'active', 'failed');

CREATE TABLE domains (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    domain TEXT NOT NULL UNIQUE CHECK (domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'),
    kind domain_kind NOT NULL DEFAULT 'alias',
    redirect_to_https BOOLEAN NOT NULL DEFAULT TRUE,
    ssl_mode ssl_mode NOT NULL DEFAULT 'none',
    ssl_state ssl_state NOT NULL DEFAULT 'pending',
    ssl_issued_at TIMESTAMPTZ,
    ssl_expires_at TIMESTAMPTZ,
    ssl_error TEXT NOT NULL DEFAULT '',
    dns_verified_at TIMESTAMPTZ,
    dns_points_to_server BOOLEAN,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_domains_website ON domains (website_id);
CREATE INDEX idx_domains_ssl_expiry ON domains (ssl_mode, ssl_state, ssl_expires_at)
    WHERE ssl_mode = 'letsencrypt' AND ssl_state = 'active';

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'issue_certificate';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'verify_domain';

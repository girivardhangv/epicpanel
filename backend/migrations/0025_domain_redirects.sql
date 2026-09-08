-- Domain redirects: a source domain attached to a website forwards visitors
-- to an absolute http(s) target URL. The agent renders these into the vhost
-- (they travel in the provision payload as redirects:[{from,to,status}]).

CREATE TABLE IF NOT EXISTS domain_redirects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    from_domain TEXT NOT NULL UNIQUE CHECK (from_domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'),
    to_url TEXT NOT NULL,
    status_code INT NOT NULL DEFAULT 301 CHECK (status_code IN (301, 302, 307, 308)),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_domain_redirects_website ON domain_redirects (website_id);

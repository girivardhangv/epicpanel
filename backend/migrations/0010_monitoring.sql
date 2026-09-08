CREATE TABLE http_checks (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    domain TEXT NOT NULL DEFAULT '',
    status_code INT NOT NULL DEFAULT 0,
    latency_ms INT NOT NULL DEFAULT 0,
    up BOOLEAN NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_http_checks_website ON http_checks (website_id, checked_at DESC);

CREATE TABLE alerts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'critical')),
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    resource_name TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (type, resource_id, resolved_at)
);

CREATE INDEX idx_alerts_org ON alerts (organization_id, resolved_at, created_at DESC);
CREATE UNIQUE INDEX idx_alerts_unresolved ON alerts (type, resource_id) WHERE resolved_at IS NULL;

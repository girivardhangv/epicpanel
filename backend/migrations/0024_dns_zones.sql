-- DNS zones are stored as DESIRED state: the panel holds the zone (apex,
-- SOA, TTL, serial) and its records; an agent op publishes them to the
-- authoritative nameserver via the sync_dns_zone job (Phase 4).
-- SOA primary_ns/admin_email default to ns1.<domain>. / hostmaster.<domain>.
-- and are derived by the store at insert time (they depend on the row's
-- domain, so they cannot be static SQL defaults).

CREATE TABLE IF NOT EXISTS dns_zones (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    domain TEXT NOT NULL UNIQUE CHECK (domain ~ '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'),
    ttl INT NOT NULL DEFAULT 3600 CHECK (ttl BETWEEN 60 AND 604800),
    soa_primary_ns TEXT NOT NULL DEFAULT '',
    soa_admin_email TEXT NOT NULL DEFAULT '',
    soa_refresh INT NOT NULL DEFAULT 7200,
    soa_retry INT NOT NULL DEFAULT 1800,
    soa_expire INT NOT NULL DEFAULT 1209600,
    soa_minimum INT NOT NULL DEFAULT 86400,
    serial BIGINT NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_dns_zones_website ON dns_zones (website_id);

CREATE TABLE IF NOT EXISTS dns_records (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id UUID NOT NULL REFERENCES dns_zones (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'SRV', 'CAA')),
    value TEXT NOT NULL,
    ttl INT NOT NULL DEFAULT 3600 CHECK (ttl BETWEEN 60 AND 604800),
    priority INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (zone_id, name, type, value)
);

CREATE INDEX IF NOT EXISTS idx_dns_records_zone ON dns_records (zone_id);

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'sync_dns_zone';

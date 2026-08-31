-- Phase 8: DNS management. The panel stores zones + records as its own source
-- of truth and syncs them to a configured DNS provider (e.g. Cloudflare) via a
-- provider abstraction. zones/records rows can be managed even before a
-- provider is configured (status = 'pending').

CREATE TABLE dns_zones (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id   UUID REFERENCES servers(id) ON DELETE CASCADE,  -- optional: zone may be server-scoped
    domain      TEXT NOT NULL,
    provider    TEXT NOT NULL DEFAULT 'cloudflare',
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'synced', 'error')),
    provider_zone_id TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX dns_zones_domain_key ON dns_zones (domain);
CREATE INDEX dns_zones_server_idx ON dns_zones (server_id) WHERE server_id IS NOT NULL;

CREATE TABLE dns_records (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id    UUID NOT NULL REFERENCES dns_zones(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,                 -- relative name: '' | 'www' | 'sub'
    type       TEXT NOT NULL
               CHECK (type IN ('A','AAAA','CNAME','MX','TXT','NS','SRV')),
    value      TEXT NOT NULL,                 -- target / answer / value
    priority   INTEGER NOT NULL DEFAULT 0,    -- MX/SRV priority
    ttl        INTEGER NOT NULL DEFAULT 300,
    proxied    BOOLEAN NOT NULL DEFAULT false, -- Cloudflare orange-cloud flag (A/AAAA/CNAME)
    status     TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending','synced','error')),
    provider_record_id TEXT NOT NULL DEFAULT '',
    error      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX dns_records_zone_idx ON dns_records (zone_id);

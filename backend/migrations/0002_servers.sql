CREATE TABLE servers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    hostname TEXT NOT NULL DEFAULT '',
    os_info TEXT NOT NULL DEFAULT '',
    agent_version TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'online', 'offline', 'disabled')),
    registered_by UUID NOT NULL REFERENCES users (id),
    enrolled_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE server_registration_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    used_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE server_agent_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE TABLE server_metrics (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    cpu_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_total_bytes BIGINT NOT NULL DEFAULT 0,
    memory_used_bytes BIGINT NOT NULL DEFAULT 0,
    disk_total_bytes BIGINT NOT NULL DEFAULT 0,
    disk_used_bytes BIGINT NOT NULL DEFAULT 0,
    load1 DOUBLE PRECISION NOT NULL DEFAULT 0,
    load5 DOUBLE PRECISION NOT NULL DEFAULT 0,
    load15 DOUBLE PRECISION NOT NULL DEFAULT 0,
    collected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_servers_org ON servers (organization_id);
CREATE INDEX idx_server_reg_tokens_server ON server_registration_tokens (server_id);
CREATE INDEX idx_server_metrics_server_time ON server_metrics (server_id, collected_at DESC);

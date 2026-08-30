-- Phase 3: monitoring, telemetry and server health.
-- Raw metric samples, pre-aggregated hourly/daily rollups, the alert
-- foundation (rules + de-duplicated alert state) and monitoring permissions.

-- ---------------------------------------------------------------------------
-- Raw telemetry samples. One row per collection cycle per server. JSONB
-- columns carry the list-shaped metrics (disks, interfaces, processes,
-- services) so the table stays narrow while the payload stays bounded.
-- ---------------------------------------------------------------------------
CREATE TABLE server_metric_samples (
    id                  BIGSERIAL PRIMARY KEY,
    server_id           UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    sequence            BIGINT NOT NULL,             -- monotonic per agent
    agent_timestamp     TIMESTAMPTZ,                 -- agent clock (untrusted, informational)
    server_received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),  -- authoritative ordering clock
    agent_version       TEXT NOT NULL DEFAULT '',

    cpu_usage_percent   REAL,
    cpu_user_percent    REAL,
    cpu_system_percent  REAL,
    cpu_idle_percent    REAL,
    load_1m             REAL,
    load_5m             REAL,
    load_15m            REAL,

    memory_total_bytes       BIGINT,
    memory_used_bytes        BIGINT,
    memory_available_bytes   BIGINT,
    memory_free_bytes        BIGINT,
    memory_usage_percent     REAL,
    swap_total_bytes         BIGINT,
    swap_used_bytes          BIGINT,
    swap_usage_percent       REAL,

    uptime_seconds      BIGINT,

    disks               JSONB NOT NULL DEFAULT '[]'::jsonb,  -- [{mount, fs, total, used, available, usage_percent}]
    network             JSONB NOT NULL DEFAULT '[]'::jsonb,  -- [{interface, rx_bytes, tx_bytes, rx_packets, tx_packets, errors, drops}]
    processes           JSONB NOT NULL DEFAULT '[]'::jsonb,  -- [{name, pid, cpu_percent, memory_bytes, status}]
    services            JSONB NOT NULL DEFAULT '[]'::jsonb,  -- [{name, display_name, status, running, enabled}]

    monitoring_errors   JSONB NOT NULL DEFAULT '{}'::jsonb   -- per-collector honest errors
);
-- Dedup on (server, sequence): replays and duplicate delivery are no-ops.
CREATE UNIQUE INDEX server_metric_samples_seq_key ON server_metric_samples (server_id, sequence);
-- Time-series access pattern; ordering clock is the server's own.
CREATE INDEX server_metric_samples_time_idx ON server_metric_samples (server_id, server_received_at);

-- ---------------------------------------------------------------------------
-- Hourly aggregates (retained 30 days by default)
-- ---------------------------------------------------------------------------
CREATE TABLE server_metric_hourly (
    server_id      UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    bucket         TIMESTAMPTZ NOT NULL,               -- truncated to hour (UTC)
    samples        INTEGER NOT NULL DEFAULT 0,

    cpu_min        REAL, cpu_avg REAL, cpu_max REAL,
    mem_min        REAL, mem_avg REAL, mem_max REAL,
    load_max       REAL,
    disk_max       JSONB NOT NULL DEFAULT '{}'::jsonb, -- {mount: usage_percent} maxima
    network        JSONB NOT NULL DEFAULT '{}'::jsonb, -- {interface: {rx_first, rx_last, tx_first, tx_last}}
    uptime_max     BIGINT,

    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (server_id, bucket)
);

-- ---------------------------------------------------------------------------
-- Daily aggregates (retained 1 year by default)
-- ---------------------------------------------------------------------------
CREATE TABLE server_metric_daily (
    server_id      UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    bucket         DATE NOT NULL,
    samples        INTEGER NOT NULL DEFAULT 0,

    cpu_min        REAL, cpu_avg REAL, cpu_max REAL,
    mem_min        REAL, mem_avg REAL, mem_max REAL,
    disk_max       JSONB NOT NULL DEFAULT '{}'::jsonb,
    network        JSONB NOT NULL DEFAULT '{}'::jsonb, -- {interface: {rx_first, rx_last, tx_first, tx_last}} for the day

    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (server_id, bucket)
);

-- ---------------------------------------------------------------------------
-- Alert foundation: rules + de-duplicated alert state.
-- The partial unique index guarantees at most ONE active alert per
-- (rule, server): no alert storms while a condition persists.
-- ---------------------------------------------------------------------------
CREATE TABLE alert_rules (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT NOT NULL UNIQUE,
    rule_type        TEXT NOT NULL CHECK (rule_type IN
                     ('cpu_high', 'memory_high', 'disk_high', 'server_offline', 'service_stopped')),
    threshold        REAL,                 -- percent / minutes depending on type
    duration_seconds INTEGER NOT NULL DEFAULT 300,
    severity         TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('warning', 'critical')),
    enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE alerts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_id         UUID NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    server_id       UUID REFERENCES servers(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'triggered'
                    CHECK (status IN ('triggered', 'acknowledged', 'resolved')),
    severity        TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('warning', 'critical')),
    metric_value    REAL,
    threshold       REAL,
    message         TEXT NOT NULL DEFAULT '',
    triggered_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_breached_at TIMESTAMPTZ,
    acknowledged_at TIMESTAMPTZ,
    acknowledged_by UUID REFERENCES users(id) ON DELETE SET NULL,
    resolved_at     TIMESTAMPTZ,
    resolved_reason TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX alerts_active_key ON alerts (rule_id, server_id)
    WHERE status IN ('triggered', 'acknowledged');
CREATE INDEX alerts_status_idx ON alerts (status, triggered_at DESC);
CREATE INDEX alerts_server_idx ON alerts (server_id, triggered_at DESC);

-- Default rule set. Thresholds are editable through the API (settings.manage).
INSERT INTO alert_rules (name, rule_type, threshold, duration_seconds, severity) VALUES
    ('cpu_high_critical',    'cpu_high',       90, 300,  'critical'),
    ('memory_high_critical', 'memory_high',    90, 300,  'critical'),
    ('disk_high_warning',    'disk_high',      90, 600,  'warning'),
    ('server_offline',       'server_offline',  5, 0,    'critical'),
    ('service_stopped',      'service_stopped', 0, 300,  'warning')
ON CONFLICT (name) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Monitoring permissions
-- ---------------------------------------------------------------------------
INSERT INTO permissions (code, description) VALUES
    ('monitoring.view',          'View monitoring data and alerts'),
    ('monitoring.server.view',   'View server metrics and health'),
    ('monitoring.website.view',  'View website health'),
    ('monitoring.processes.view','View server process lists'),
    ('monitoring.services.view', 'View server service health')
ON CONFLICT (code) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id FROM roles r CROSS JOIN permissions p
WHERE r.name = 'super_admin'
  AND p.code IN ('monitoring.view', 'monitoring.server.view',
                 'monitoring.website.view', 'monitoring.processes.view',
                 'monitoring.services.view')
ON CONFLICT DO NOTHING;

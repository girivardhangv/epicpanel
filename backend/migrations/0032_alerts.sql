-- Phase 13: Monitoring & Observability — alert rule engine, notification
-- channels and delivery tracking on top of the Phase 10 `alerts` feed table
-- (0010). The alerts table stays the single feed (adminview's read model and
-- the website health checker keep working); this migration extends it and
-- adds the rule/channel machinery.
--
-- Every statement is re-runnable (IF NOT EXISTS / DO blocks): test harnesses
-- drop schema_migrations and re-apply the whole chain.

-- ---------------------------------------------------------------------------
-- 1. Alert rules (platform-wide; the WHM admin role IS the authorization).
--
-- Verbatim alert types (master doc):
--   CPU > threshold | RAM > threshold | Disk > threshold | Node offline
--   Service down | Backup failed | Provisioning failed | SSL expiration
--   Container crashed
-- plus workload threshold metrics covering the overview tree:
--   mc_tps | mc_mspt | mc_players | discord_cpu | discord_ram | discord_uptime
--
-- Class:  threshold  — metric vs threshold, evaluated per live sample sweep.
--         state      — boolean condition (offline / down / failed / crashed).
--         time       — calendar window (SSL expiry in N days).
-- Scope:  node       — scope_id = server id (empty = every node).
--         account    — scope_id = website id (empty = every hosting account).
--         plan       — scope_id = hosting package NAME (empty = every plan).
--         fleet      — scope-independent (state/time classes).
--
-- Hysteresis: a threshold alerts after `duration_seconds` of CONTINUOUS
-- breach (consecutive evaluations with sweep cadence) and clears when the
-- value recovers past `recovery_margin` under the threshold.
CREATE TABLE IF NOT EXISTS alert_rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    rule_class TEXT NOT NULL CHECK (rule_class IN ('threshold', 'state', 'time')),
    metric TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT 'fleet' CHECK (scope IN ('node', 'account', 'plan', 'fleet')),
    scope_id TEXT NOT NULL DEFAULT '',
    comparison TEXT NOT NULL DEFAULT 'gt' CHECK (comparison IN ('gt', 'lt')),
    threshold DOUBLE PRECISION NOT NULL DEFAULT 0,
    duration_seconds INT NOT NULL DEFAULT 30,      -- consecutive-breach window (hysteresis in)
    recovery_margin DOUBLE PRECISION NOT NULL DEFAULT 5,
    window_days INT NOT NULL DEFAULT 0,            -- time-class windows (SSL: 30/14/7)
    severity TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'critical')),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_alert_rules_enabled ON alert_rules (enabled, rule_class);

-- ---------------------------------------------------------------------------
-- 2. Notification channels. In-app is built in; email/webhook are the
-- foundation (webhook = signed HTTP POST; email = sender interface, no SMTP
-- credentials exist in scope — logged sender ships, honest gap in handoff).
CREATE TABLE IF NOT EXISTS alert_channels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL CHECK (kind IN ('in_app', 'email', 'webhook')),
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

DO $$ BEGIN
    CREATE TYPE alert_delivery_status AS ENUM ('delivered', 'failed', 'skipped');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- One delivery record per (alert, channel) attempt — the audit trail of who
-- was told, when, and whether it worked.
CREATE TABLE IF NOT EXISTS alert_deliveries (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    alert_id UUID NOT NULL REFERENCES alerts (id) ON DELETE CASCADE,
    channel_id UUID REFERENCES alert_channels (id) ON DELETE SET NULL,
    channel_kind TEXT NOT NULL,
    status alert_delivery_status NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_alert_deliveries_alert ON alert_deliveries (alert_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- 3. Extend the alerts feed additively (0010 columns stay untouched).
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS rule_id UUID REFERENCES alert_rules (id) ON DELETE SET NULL;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS occurrences INT NOT NULL DEFAULT 1;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS acknowledged_at TIMESTAMPTZ;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS acknowledged_by UUID REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS resolved_by UUID REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE INDEX IF NOT EXISTS idx_alerts_state ON alerts (resolved_at NULLS FIRST, severity, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_resource ON alerts (resource_type, resource_id);

-- Seed the built-in in-app channel (idempotent; the alert feed itself is the
-- in-app delivery — this row makes the channel explicit for admin config).
INSERT INTO alert_channels (name, kind, config)
VALUES ('in-app', 'in_app', '{"note": "alerts feed + live WebSocket push"}'::jsonb)
ON CONFLICT (name) DO NOTHING;

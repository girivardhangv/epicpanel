-- Lifecycle reasons (0050): WHY a site is suspended, when it happened, and
-- the per-site bandwidth override. Suspension is a state + a reason (ADR-047
-- state machine stays; reasons are data, not new states). 'terminated' joins
-- the status machine for the explicit terminate -> purge lifecycle.

ALTER TABLE websites
    ADD COLUMN IF NOT EXISTS suspension_reason TEXT
        CONSTRAINT websites_suspension_reason_check
        CHECK (suspension_reason IN ('manual', 'bandwidth_exhausted', 'abuse', 'payment', 'admin', 'system', 'attack')),
    ADD COLUMN IF NOT EXISTS suspended_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS suspension_metadata JSONB,
    ADD COLUMN IF NOT EXISTS terminated_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS termination_reason TEXT,
    ADD COLUMN IF NOT EXISTS bandwidth_limit_mb BIGINT;

-- Extend the status machine with 'terminated' (drop/re-add like 0026 did;
-- the 0003 constraint was an unnamed inline constraint that PostgreSQL
-- auto-named websites_status_check).
ALTER TABLE websites DROP CONSTRAINT IF EXISTS websites_status_check;
ALTER TABLE websites ADD CONSTRAINT websites_status_check
    CHECK (status IN ('pending', 'provisioning', 'ready', 'failed', 'deleting', 'deleted', 'suspended', 'terminated'));

-- Per-site bandwidth history (completed 60s agent access-log windows rolled
-- up into hourly + daily buckets; hourly pruned > 90d, daily kept
-- indefinitely). Access-log bytes are response (egress-dominated) bytes —
-- the authoritative quota number remains the nftables RX+TX high-water in
-- workload_resource_usage.
CREATE TABLE IF NOT EXISTS website_bandwidth_samples (
    website_id  UUID        NOT NULL REFERENCES websites(id) ON DELETE CASCADE,
    hour_bucket TIMESTAMPTZ NOT NULL,
    rx_bytes    BIGINT      NOT NULL DEFAULT 0,
    tx_bytes    BIGINT      NOT NULL DEFAULT 0,
    requests    BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (website_id, hour_bucket)
);

CREATE TABLE IF NOT EXISTS website_bandwidth_daily (
    website_id UUID NOT NULL REFERENCES websites(id) ON DELETE CASCADE,
    day        DATE NOT NULL,
    rx_bytes   BIGINT NOT NULL DEFAULT 0,
    tx_bytes   BIGINT NOT NULL DEFAULT 0,
    requests   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (website_id, day)
);

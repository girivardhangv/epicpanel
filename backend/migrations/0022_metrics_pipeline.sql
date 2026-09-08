-- 0022: Phase 3 real-time metrics pipeline.
-- Extends server_metrics with the full node collection list (network RX/TX,
-- disk I/O rates, swap, TCP connections, process count, inode usage,
-- degraded flag) and adds the 5-minute rollup table for long-range graphs.

ALTER TABLE server_metrics
    ADD COLUMN IF NOT EXISTS swap_total_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS swap_used_bytes BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS network_rx_bps DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS network_tx_bps DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS read_bps DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS write_bps DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tcp_established INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tcp_total INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS processes INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS inodes_total BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS inodes_used BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS degraded BOOLEAN NOT NULL DEFAULT FALSE;

-- Long-range history: 5-minute buckets built hourly from raw rows
-- (raw rows are pruned after 24h by the metrics writer).
CREATE TABLE IF NOT EXISTS server_metrics_rollup_5m (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id UUID NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    bucket TIMESTAMPTZ NOT NULL,
    cpu_avg DOUBLE PRECISION NOT NULL DEFAULT 0,
    cpu_max DOUBLE PRECISION NOT NULL DEFAULT 0,
    mem_used_avg DOUBLE PRECISION NOT NULL DEFAULT 0,
    mem_total_max DOUBLE PRECISION NOT NULL DEFAULT 0,
    load1_avg DOUBLE PRECISION NOT NULL DEFAULT 0,
    rx_avg DOUBLE PRECISION NOT NULL DEFAULT 0,
    tx_avg DOUBLE PRECISION NOT NULL DEFAULT 0,
    UNIQUE (server_id, bucket)
);

CREATE INDEX IF NOT EXISTS idx_server_metrics_rollup_time
    ON server_metrics_rollup_5m (server_id, bucket DESC);

-- History writer throughput: batch inserts by time window.
CREATE INDEX IF NOT EXISTS idx_server_metrics_time
    ON server_metrics (collected_at);

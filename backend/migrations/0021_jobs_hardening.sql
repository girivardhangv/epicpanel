-- Phase 2: Job system hardening (audit: no lease/reaper, no backoff, no
-- idempotency keys) + missing indexes found by the audit.

ALTER TABLE jobs ADD COLUMN idempotency_key TEXT;
ALTER TABLE jobs ADD COLUMN visible_after TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE jobs ADD COLUMN lease_expires_at TIMESTAMPTZ;

-- At most one live job per idempotency key: retries of the same logical
-- operation collapse instead of duplicating side effects.
CREATE UNIQUE INDEX idx_jobs_idempotency_active ON jobs (idempotency_key)
    WHERE idempotency_key IS NOT NULL AND status IN ('pending', 'running');

-- Claim path: (server, status) filtered by visibility, ordered by creation.
CREATE INDEX idx_jobs_claim ON jobs (server_id, status, visible_after, created_at);

-- Audit-flagged missing indexes for hot queries.
CREATE INDEX IF NOT EXISTS idx_domains_org ON domains (organization_id);
CREATE INDEX IF NOT EXISTS idx_websites_usage_scan ON websites (usage_sampled_at) WHERE status = 'ready';
CREATE INDEX IF NOT EXISTS idx_websites_backup_scan ON websites (last_backup_at) WHERE backup_schedule <> 'off';

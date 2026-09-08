-- Account lifecycle: suspend/resume websites as first-class agent jobs.
-- The status CHECK in 0003 was an unnamed inline constraint; PostgreSQL
-- auto-named it websites_status_check.

ALTER TABLE websites DROP CONSTRAINT IF EXISTS websites_status_check;
ALTER TABLE websites ADD CONSTRAINT websites_status_check
    CHECK (status IN ('pending', 'provisioning', 'ready', 'failed', 'deleting', 'deleted', 'suspended'));

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'suspend_website';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'resume_website';

CREATE INDEX IF NOT EXISTS idx_websites_org_status ON websites (organization_id, status);

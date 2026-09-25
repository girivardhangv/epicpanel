-- Termination lifecycle (0051): terminate_website is a first-class agent
-- job alongside suspend/resume. The websites status machine gained
-- 'terminated' in 0050; this adds the job type. Purge reuses the existing
-- delete_website job, so no further enum value is needed.

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'terminate_website';

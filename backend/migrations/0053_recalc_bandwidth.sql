-- Bandwidth recalculation / reconciliation (0053): recalc_bandwidth is an
-- admin-invoked agent job that re-scans the platform accounting logs
-- (/var/log/epicpanel/bandwidth.log generations) for one site + day range.
-- The control plane's apply fanout can only RAISE the monthly high-water
-- (GREATEST) — repair never moves billed usage backwards.

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'recalc_bandwidth';

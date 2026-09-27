-- Plain-fetch deploys (0055): a git deploy is clone + activate BY DEFAULT.
-- The composer/npm build gate is opt-in per site (deploy_auto_build) — the
-- panel cannot know what the app needs, and a forced build turned every
-- fresh clone into a failed deploy (composer missing, sqlite absent, wrong
-- php). App owners run composer/artisan themselves via the Commands runner
-- or flip this toggle.

ALTER TABLE websites ADD COLUMN IF NOT EXISTS deploy_auto_build boolean NOT NULL DEFAULT false;

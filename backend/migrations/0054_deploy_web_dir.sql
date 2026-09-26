-- Deployment running directory (0054): the repo-relative web dir inside a
-- git release (Forge-style "web directory" — e.g. "public" for Laravel,
-- "web" for legacy Symfony). Empty = release root (static/plain PHP).
-- The agent resolves the vhost/FPM docroot as <site>/public/<web_dir>,
-- which follows the release symlink, so deploys keep swapping atomically
-- underneath it. A manual site-level docroot_suffix (one-click Laravel's
-- app/public, per-domain overrides) still wins when set.

ALTER TABLE websites ADD COLUMN IF NOT EXISTS deploy_web_dir text NOT NULL DEFAULT '';

-- App-platform serving mode for dynamic-runtime websites (node/python/go):
-- nginx reverse-proxies to the site's application process on a private
-- loopback port; the process runs as the site unix user under a managed
-- systemd unit (agent app_ops.go). These columns persist the app's desired
-- state control-plane side, mirroring how runtime_version drives FPM pools
-- for PHP sites. Env vars are stored encrypted (same secretbox scheme as
-- deploy_token_encrypted) because they routinely hold secrets.

ALTER TABLE websites ADD COLUMN app_startup_command TEXT NOT NULL DEFAULT '';
ALTER TABLE websites ADD COLUMN app_build_command TEXT NOT NULL DEFAULT '';
ALTER TABLE websites ADD COLUMN app_port INTEGER NOT NULL DEFAULT 0;
ALTER TABLE websites ADD COLUMN app_desired_state TEXT NOT NULL DEFAULT 'stopped'
    CHECK (app_desired_state IN ('running', 'stopped'));
ALTER TABLE websites ADD COLUMN app_env_encrypted BYTEA;

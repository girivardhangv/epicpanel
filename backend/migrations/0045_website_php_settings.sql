-- Phase 14: per-site PHP INI settings (cPanel MultiPHP INI Editor equivalent).
-- One row per website; keys/values are validated against a fixed allowlist in
-- Go (internal/websites/php_settings.go) before they reach the agent, so a
-- stored row can never inject arbitrary php_admin_value directives.
CREATE TABLE IF NOT EXISTS website_php_settings (
    website_id UUID PRIMARY KEY REFERENCES websites (id) ON DELETE CASCADE,
    settings JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
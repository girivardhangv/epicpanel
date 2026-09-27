-- Context-aware nginx configuration system (ADR-068).
-- website_configs gains a structured, versioned JSONB document next to the
-- legacy rewrite_rules string. The legacy column stays authoritative for
-- sites that never save a structured config (agent falls back to it), so
-- existing sites render identically after upgrade.

ALTER TABLE website_configs
    ADD COLUMN IF NOT EXISTS config_json JSONB NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS version INT NOT NULL DEFAULT 1;

-- Append-only version history (last 20 kept at runtime by the store).
CREATE TABLE IF NOT EXISTS website_config_versions (
    website_id  UUID   NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    version     INT    NOT NULL,
    config_json JSONB  NOT NULL,
    created_by  UUID,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (website_id, version)
);

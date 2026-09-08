-- System settings: key-value store for panel configuration
CREATE TABLE system_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Default settings
INSERT INTO system_settings (key, value) VALUES
    ('panel_hostname', ''),
    ('setup_completed', 'false'),
    ('panel_version', '1.0.0')
ON CONFLICT (key) DO NOTHING;

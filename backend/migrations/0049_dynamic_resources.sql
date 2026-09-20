-- Dynamic resources (traffic-adaptive allocation + bot defense) + Free Perk.
--
-- Per-site feature columns on websites:
--   dynamic_enabled  the site opted into traffic-adaptive allocation
--   dynamic_tier     current allocation tier (0 = floor, 1 = package base,
--                    2..N = multiples of the base; see internal/traffic)
--   dynamic_state    active | busy (protection: floor limits, still served)
--                    | suspended_attack (deallocated + disabled)
-- The allocator (internal/traffic) is the only writer of tier/state besides
-- API toggles; every transition lands in dynamic_resource_events.

ALTER TABLE websites ADD COLUMN IF NOT EXISTS dynamic_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE websites ADD COLUMN IF NOT EXISTS dynamic_tier INT NOT NULL DEFAULT 0;
ALTER TABLE websites ADD COLUMN IF NOT EXISTS dynamic_state TEXT NOT NULL DEFAULT 'active'
    CHECK (dynamic_state IN ('active', 'busy', 'suspended_attack'));
ALTER TABLE websites ADD COLUMN IF NOT EXISTS free_perk BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_websites_dynamic_enabled ON websites (dynamic_enabled)
    WHERE dynamic_enabled;

-- Decision/audit trail for the allocator (rendered in the site's dynamic
-- resources card; doubles as the attack history).
CREATE TABLE IF NOT EXISTS dynamic_resource_events (
    id             BIGSERIAL PRIMARY KEY,
    website_id     UUID NOT NULL REFERENCES websites(id) ON DELETE CASCADE,
    organization_id UUID,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind           TEXT NOT NULL CHECK (kind IN
        ('enable', 'disable', 'scale_up', 'scale_down', 'busy',
         'suspend_attack', 'restore', 'perk_assigned', 'perk_removed')),
    from_tier      INT,
    to_tier        INT,
    score          DOUBLE PRECISION,
    reason         TEXT
);
CREATE INDEX IF NOT EXISTS idx_dyn_events_website ON dynamic_resource_events (website_id, created_at DESC);

-- The Free Perk is a hosting_packages row (kind 'free_perk') so admins edit
-- its resource numbers through the existing package CRUD — data, not code.
ALTER TABLE hosting_packages DROP CONSTRAINT IF EXISTS hosting_packages_kind_check;
ALTER TABLE hosting_packages ADD CONSTRAINT hosting_packages_kind_check
    CHECK (kind IN ('web', 'minecraft', 'discord', 'free_perk'));

INSERT INTO hosting_packages
    (name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
     max_addon_domains, max_subdomains, allowed_runtimes, price_monthly_cents, is_default,
     max_bandwidth_mb, io_weight, php_max_children, max_processes, max_ports, max_backups, max_email_accounts)
VALUES
    ('Free Perk', 'free_perk', 0, 0, 1024, 64, 0.20, 0, 0, '{static,php,node,python,go}',
     0, FALSE, 10240, 50, 2, 16, 0, 0, 0)
ON CONFLICT (name) DO UPDATE SET
    kind = 'free_perk',
    updated_at = now();

-- Panel-wide configuration. dynamic_resources_enabled is the master switch:
-- when off, the allocator is inert and dynamic sites converge back to their
-- package base limits (one-time sweep, same as disabling per site).
INSERT INTO system_settings (key, value) VALUES
    ('dynamic_resources_enabled', 'false'),
    ('dynamic_floor_memory_mb', '32'),
    ('dynamic_attack_windows', '2'),
    ('dynamic_recover_windows', '4'),
    ('free_perk_max_sites_per_user', '1')
ON CONFLICT (key) DO NOTHING;

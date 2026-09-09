-- Phase 8 fix: the Discord create gate (phase8_discord.go) requires the
-- org's effective plan kind to be 'discord'. Unassigned orgs fall back to
-- the platform default package — Starter (kind web) — so every fresh org
-- was hard-403'd on bot creation ("the organization plan does not include
-- Discord bot hosting") and the feature was dead on arrival.
--
-- Fix (data, not code): seed an assignable Discord plan and make it the
-- platform default. Unassigned orgs then resolve kind=discord and pass the
-- gate; admins can still assign any other plan (a non-Discord package on an
-- org stays a genuine, fail-closed 403). Starter keeps serving as the web
-- default for the website flow via PlanFromPackage fallbacks.
INSERT INTO hosting_packages
    (name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
     max_addon_domains, max_subdomains, allowed_runtimes, price_monthly_cents, is_default,
     max_bandwidth_mb, io_weight, php_max_children, max_processes, max_ports, max_backups, max_email_accounts)
VALUES
    ('Discord Starter', 'discord', 1, 0, 2048, 512, 0.50, 0, 0, '{node,python}', 0, TRUE,
     524288, 100, 1, 16, 0, 0, 0)
ON CONFLICT (name) DO NOTHING;

-- Exactly ONE default: the newer Discord Starter wins (ORDER BY created_at
-- LIMIT 1 in PlanForOrg), the legacy web default is demoted.
UPDATE hosting_packages SET is_default = FALSE
WHERE is_default AND name <> 'Discord Starter';


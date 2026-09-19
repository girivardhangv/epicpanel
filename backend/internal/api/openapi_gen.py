#!/usr/bin/env python3
"""Generates internal/api/openapi.json — the machine-readable API contract.

The route table below is the single editing point: add a route line, run
`python3 openapi_gen.py`, and the embedded spec stays in sync (ADR-041 noted
hand-editing the JSON drifts; this script is the regeneration tooling).

Route tuple: (method, path, summary, tag, security, extra)
  security: True  -> bearer required (default)
            False -> public endpoint (no security requirement)
  extra:    optional dict merged into the operation object (x-scope, description)

Run from this directory:  python3 openapi_gen.py
"""
import json

BEARER = [{"bearer": []}]
PUBLIC = []

S = []  # (method, path, summary, tag, security, extra)

def R(method, path, summary, tag, secure=True, **extra):
    S.append((method.upper(), path, summary, tag, BEARER if secure else PUBLIC, extra or None))

# ---------------------------------------------------------------- auth -----
t = "Auth & MFA"
R("post", "/v1/auth/register", "Register user (first user = platform admin; locked after setup)", t, False)
R("post", "/v1/auth/login", "Login (sets session cookie; returns bearer-capable token)", t, False)
R("post", "/v1/auth/logout", "Logout (revoke session)", t)
R("get", "/v1/auth/me", "Current user", t)
R("post", "/v1/auth/mfa/setup", "Start MFA enrollment", t)
R("post", "/v1/auth/mfa/enable", "Enable MFA with TOTP code", t)
R("post", "/v1/auth/mfa/disable", "Disable MFA", t)
R("post", "/v1/auth/mfa/verify", "Verify MFA during login", t, False)

# ----------------------------------------------------------------- orgs ----
t = "Organizations"
R("post", "/v1/organizations", "Create organization (creator becomes owner)", t, x_scope="org:write")
R("get", "/v1/organizations", "List own organizations", t, x_scope="org:read")
R("get", "/v1/organizations/{org_id}", "Organization details (billing+)", t, x_scope="org:read")
R("patch", "/v1/organizations/{org_id}", "Rename organization (admin+)", t, x_scope="org:write")
R("get", "/v1/organizations/{org_id}/members", "List members (billing+)", t, x_scope="org:read")
R("post", "/v1/organizations/{org_id}/members", "Add member by email (admin+)", t, x_scope="org:write")
R("patch", "/v1/organizations/{org_id}/members/{user_id}", "Change member role (admin+)", t, x_scope="org:write")
R("delete", "/v1/organizations/{org_id}/members/{user_id}", "Remove member (admin+; last owner protected)", t, x_scope="org:write")

# --------------------------------------------------------------- servers ---
t = "Servers & Agent"
R("post", "/v1/organizations/{org_id}/servers", "Register server (platform admin; one-time reg token)", t, x_scope="servers:write")
R("get", "/v1/organizations/{org_id}/servers", "List org servers (billing+)", t, x_scope="servers:read")
R("get", "/v1/organizations/{org_id}/servers/{server_id}", "Server details (billing+)", t, x_scope="servers:read")
R("delete", "/v1/organizations/{org_id}/servers/{server_id}", "Delete server (platform admin)", t, x_scope="servers:write")
R("post", "/v1/organizations/{org_id}/servers/{server_id}/registration-token", "Rotate one-time registration token (platform admin)", t, x_scope="servers:write")
R("get", "/v1/organizations/{org_id}/servers/{server_id}/metrics", "Latest server metrics (billing+)", t, x_scope="servers:read")
R("get", "/v1/organizations/{org_id}/servers/metrics", "Latest metrics for all servers (billing+)", t, x_scope="servers:read")
R("get", "/v1/organizations/{org_id}/servers/{server_id}/metrics/history", "Metrics history (billing+)", t, x_scope="servers:read")
R("get", "/v1/organizations/{org_id}/servers/{server_id}/jobs", "Recent jobs for a server (billing+)", t, x_scope="servers:read")
R("get", "/v1/organizations/{org_id}/servers/capacity", "Placement/capacity overview (platform admin)", t, x_scope="servers:read")
R("patch", "/v1/organizations/{org_id}/servers/{server_id}/maintenance", "Maintenance mode (platform admin)", t, x_scope="servers:write")
R("post", "/v1/organizations/{org_id}/servers/{server_id}/database-tools", "Install phpMyAdmin/Adminer (platform admin)", t, x_scope="servers:write")
t = "Agent channel (machine)"
R("post", "/v1/agent/enroll", "Exchange one-time reg token for agent token", t, False)
R("post", "/v1/agent/heartbeat", "Agent heartbeat + metrics (Bearer agt_)", t)
R("get", "/v1/agent/stream", "Agent SSE event stream (Bearer agt_)", t)
R("post", "/v1/agent/jobs/claim", "Claim next pending job (Bearer agt_)", t)
R("post", "/v1/agent/jobs/{job_id}/result", "Report job outcome (Bearer agt_)", t)
R("post", "/v1/agent/jobs/{job_id}/progress", "Report job progress (Bearer agt_)", t)

# -------------------------------------------------------------- websites ---
t = "Websites"
R("post", "/v1/organizations/{org_id}/websites", "Create website -> 202 + job (server_id optional: auto-placement)", t, x_scope="websites:write")
R("get", "/v1/organizations/{org_id}/websites", "List websites (billing+)", t, x_scope="websites:read")
R("get", "/v1/organizations/{org_id}/websites/{website_id}", "Website details (billing+)", t, x_scope="websites:read")
R("patch", "/v1/organizations/{org_id}/websites/{website_id}", "Update website (runtime/docroot) -> reconcile job", t, x_scope="websites:write")
R("delete", "/v1/organizations/{org_id}/websites/{website_id}", "Delete website -> delete job (admin+)", t, x_scope="websites:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/jobs", "Website job history", t, x_scope="websites:read")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/config", "Rendered site config (billing+)", t, x_scope="websites:read")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/usage", "Per-site resource usage (billing+)", t, x_scope="websites:read")
R("put", "/v1/organizations/{org_id}/websites/{website_id}/config/rewrite", "Set nginx rewrite rules (developer+)", t, x_scope="websites:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/php-settings", "Per-site PHP settings", t, x_scope="websites:read")
R("put", "/v1/organizations/{org_id}/websites/{website_id}/php-settings", "Update PHP settings -> job", t, x_scope="websites:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/suspend", "Suspend site (admin+)", t, x_scope="websites:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/resume", "Resume site (admin+)", t, x_scope="websites:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/staging", "Create staging clone (admin+)", t, x_scope="deployments:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/promote", "Promote staging -> production (admin+)", t, x_scope="deployments:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/wordpress", "WordPress one-click install (developer+)", t, x_scope="websites:write")

t = "File manager"
R("get", "/v1/organizations/{org_id}/websites/{website_id}/files", "List directory (path query)", t, x_scope="websites:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/files", "Create file/dir", t, x_scope="websites:write")
R("patch", "/v1/organizations/{org_id}/websites/{website_id}/files", "Rename/move", t, x_scope="websites:write")
R("delete", "/v1/organizations/{org_id}/websites/{website_id}/files", "Delete file/dir", t, x_scope="websites:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/files/content", "Read file (editor, 2MB cap)", t, x_scope="websites:read")
R("put", "/v1/organizations/{org_id}/websites/{website_id}/files/content", "Write file", t, x_scope="websites:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/files/download", "Download file or whole-site tar.gz", t, x_scope="websites:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/files/upload", "Multipart upload (32MB cap)", t, x_scope="websites:write")

# ------------------------------------------------- domains / DNS / SSL -----
t = "Domains, DNS & SSL"
R("get", "/v1/organizations/{org_id}/websites/{website_id}/domains", "List website domains (billing+)", t, x_scope="domains:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/domains", "Add alias/subdomain (developer+)", t, x_scope="domains:write")
R("patch", "/v1/organizations/{org_id}/domains/{domain_id}", "Update domain docroot (developer+)", t, x_scope="domains:write")
R("delete", "/v1/organizations/{org_id}/websites/{website_id}/domains/{domain_id}", "Remove alias (admin+)", t, x_scope="domains:write")
R("post", "/v1/organizations/{org_id}/domains/{domain_id}/ssl", "Set SSL mode none|selfsigned|letsencrypt -> cert job", t, x_scope="domains:write")
R("post", "/v1/organizations/{org_id}/domains/{domain_id}/verify-dns", "Queue DNS verification -> job", t, x_scope="domains:write")
R("get", "/v1/organizations/{org_id}/domains", "List org domains (billing+)", t, x_scope="domains:read")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/redirects", "List redirects", t, x_scope="domains:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/redirects", "Create redirect", t, x_scope="domains:write")
R("patch", "/v1/organizations/{org_id}/redirects/{redirect_id}", "Update redirect", t, x_scope="domains:write")
R("delete", "/v1/organizations/{org_id}/redirects/{redirect_id}", "Delete redirect", t, x_scope="domains:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/dns-zone", "Get DNS zone (billing+)", t, x_scope="domains:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/dns-zone", "Create zone for primary domain (developer+)", t, x_scope="domains:write")
R("delete", "/v1/organizations/{org_id}/dns-zones/{zone_id}", "Delete zone (admin+)", t, x_scope="domains:write")
R("post", "/v1/organizations/{org_id}/dns-zones/{zone_id}/records", "Create DNS record (developer+)", t, x_scope="domains:write")
R("patch", "/v1/organizations/{org_id}/dns-records/{record_id}", "Update DNS record (developer+)", t, x_scope="domains:write")
R("delete", "/v1/organizations/{org_id}/dns-records/{record_id}", "Delete DNS record (admin+)", t, x_scope="domains:write")
R("post", "/v1/organizations/{org_id}/dns-zones/{zone_id}/publish", "Publish zone to agent -> job", t, x_scope="domains:write")

# -------------------------------------------------------------- databases --
t = "Databases"
R("post", "/v1/organizations/{org_id}/databases", "Create database {name,server_id,engine,website_id?} -> 202 + job", t, x_scope="databases:write")
R("get", "/v1/organizations/{org_id}/databases", "List databases (billing+)", t, x_scope="databases:read")
R("get", "/v1/organizations/{org_id}/databases/{db_id}", "Database details (billing+)", t, x_scope="databases:read")
R("delete", "/v1/organizations/{org_id}/databases/{db_id}", "Drop database -> job (admin+)", t, x_scope="databases:write")
R("get", "/v1/organizations/{org_id}/databases/{db_id}/credentials", "Reveal credentials (developer+, audited)", t, x_scope="databases:read")
R("get", "/v1/organizations/{org_id}/databases/{db_id}/pma-sso", "phpMyAdmin SSO URL (developer+)", t, x_scope="databases:read")

# ------------------------------------------------------------- deployments -
t = "Deployments"
R("get", "/v1/organizations/{org_id}/websites/{website_id}/deployments", "Deployment history (billing+)", t, x_scope="deployments:read")
R("patch", "/v1/organizations/{org_id}/websites/{website_id}/deployment-config", "Set repo/branch/token (developer+)", t, x_scope="deployments:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/deploy", "Trigger deploy -> job", t, x_scope="deployments:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/rollback", "Rollback to last release (admin+)", t, x_scope="deployments:write")

# ----------------------------------------------------------------- backups -
t = "Backups"
R("get", "/v1/organizations/{org_id}/websites/{website_id}/backups", "Backup history (billing+)", t, x_scope="backups:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/backups", "Manual backup -> job", t, x_scope="backups:write")
R("patch", "/v1/organizations/{org_id}/websites/{website_id}/backup-config", "Schedule + retention (admin+)", t, x_scope="backups:write")
R("post", "/v1/organizations/{org_id}/backups/{backup_id}/restore", "Restore backup -> job (admin+)", t, x_scope="backups:write")
R("get", "/v1/organizations/{org_id}/backup-targets", "List backup targets (Phase 11)", t, x_scope="backups:read")
R("post", "/v1/organizations/{org_id}/backup-targets", "Create backup target", t, x_scope="backups:write")
R("delete", "/v1/organizations/{org_id}/backup-targets/{target_id}", "Delete backup target", t, x_scope="backups:write")
R("get", "/v1/organizations/{org_id}/backups2", "List backups (unified engine)", t, x_scope="backups:read")
R("post", "/v1/organizations/{org_id}/backups2", "Create backup {type,website_id?,target_id?,encrypt?,verify?}", t, x_scope="backups:write")
R("post", "/v1/organizations/{org_id}/backups2/{backup_id}/restore", "Restore (unified engine)", t, x_scope="backups:write")
R("post", "/v1/organizations/{org_id}/backups2/{backup_id}/verify", "Verify backup archive", t, x_scope="backups:write")
R("get", "/v1/organizations/{org_id}/backup-schedules", "List backup schedules", t, x_scope="backups:read")
R("post", "/v1/organizations/{org_id}/backup-schedules", "Create backup schedule {type,website_id?,cron}", t, x_scope="backups:write")
R("delete", "/v1/organizations/{org_id}/backup-schedules/{schedule_id}", "Delete backup schedule", t, x_scope="backups:write")

# ------------------------------------------------- runtimes / apps / cron --
t = "Runtimes & Applications"
R("get", "/v1/organizations/{org_id}/servers/{server_id}/runtimes", "List runtimes on server (billing+)", t, x_scope="servers:read")
R("post", "/v1/organizations/{org_id}/servers/{server_id}/runtimes", "Install runtime {type,version} -> job (admin+)", t, x_scope="runtimes:write")
R("delete", "/v1/organizations/{org_id}/servers/{server_id}/runtimes/{runtime_id}", "Remove runtime (refcount-guarded; admin+)", t, x_scope="runtimes:write")
R("get", "/v1/organizations/{org_id}/servers/{server_id}/runtimes/{runtime_id}/extensions", "List runtime extensions", t, x_scope="runtimes:read")
R("post", "/v1/organizations/{org_id}/servers/{server_id}/runtimes/{runtime_id}/extensions", "Install/remove extension -> job", t, x_scope="runtimes:write")
R("post", "/v1/organizations/{org_id}/servers/{server_id}/detect-software", "Detect installed software -> job (admin+)", t, x_scope="runtimes:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/application", "App process config (billing+)", t, x_scope="websites:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/application", "Create app process -> build/start jobs", t, x_scope="websites:write")
R("patch", "/v1/organizations/{org_id}/websites/{website_id}/application", "Update app + restart", t, x_scope="websites:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/application/start", "Start app", t, x_scope="websites:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/application/stop", "Stop app", t, x_scope="websites:write")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/application/restart", "Restart app", t, x_scope="websites:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/application/status", "App status -> job", t, x_scope="websites:read")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/application/logs", "App logs -> job", t, x_scope="websites:read")

t = "Cron, FTP & SSH"
R("get", "/v1/organizations/{org_id}/websites/{website_id}/crons", "List cron jobs", t, x_scope="websites:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/crons", "Create cron {schedule,command} -> sync job", t, x_scope="websites:write")
R("patch", "/v1/organizations/{org_id}/crons/{cron_id}", "Toggle pause/resume", t, x_scope="websites:write")
R("delete", "/v1/organizations/{org_id}/crons/{cron_id}", "Delete cron -> resync", t, x_scope="websites:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/ftp-accounts", "List FTP/SFTP accounts", t, x_scope="websites:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/ftp-accounts", "Create FTP account -> sync job", t, x_scope="websites:write")
R("post", "/v1/organizations/{org_id}/ftp-accounts/{account_id}/password", "Change FTP password", t, x_scope="websites:write")
R("post", "/v1/organizations/{org_id}/ftp-accounts/{account_id}/reveal", "Reveal FTP password (audited)", t, x_scope="websites:read")
R("delete", "/v1/organizations/{org_id}/ftp-accounts/{account_id}", "Delete FTP account -> resync (admin+)", t, x_scope="websites:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/ssh-keys", "List site SSH keys", t, x_scope="websites:read")
R("post", "/v1/organizations/{org_id}/websites/{website_id}/ssh-keys", "Add SSH key -> sync job", t, x_scope="websites:write")
R("delete", "/v1/organizations/{org_id}/ssh-keys/{key_id}", "Delete SSH key -> resync", t, x_scope="websites:write")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/terminal", "Web terminal (WebSocket; interactive session only — API keys refused)", t)

# ------------------------------------------------- monitoring / alerts -----
t = "Monitoring & Alerts"
R("get", "/v1/organizations/{org_id}/alerts", "List alerts (billing+)", t, x_scope="alerts:read")
R("get", "/v1/organizations/{org_id}/websites/{website_id}/health", "Recent HTTP health checks (billing+)", t, x_scope="monitoring:read")
t = "Admin Alerts & Observability"
R("get", "/v1/admin/alerts", "All alerts (admin surface)", t, x_scope="admin:read")
R("post", "/v1/admin/alerts/{alert_id}/ack", "Acknowledge alert", t, x_scope="admin:write")
R("post", "/v1/admin/alerts/{alert_id}/resolve", "Resolve alert", t, x_scope="admin:write")
R("get", "/v1/admin/alert-rules", "List alert rules", t, x_scope="admin:read")
R("post", "/v1/admin/alert-rules", "Create alert rule", t, x_scope="admin:write")
R("patch", "/v1/admin/alert-rules/{rule_id}", "Update alert rule", t, x_scope="admin:write")
R("delete", "/v1/admin/alert-rules/{rule_id}", "Delete alert rule", t, x_scope="admin:write")
R("get", "/v1/admin/observability/nodes", "Node observability rollup", t, x_scope="admin:read")
R("get", "/v1/admin/observability/services", "Service observability rollup", t, x_scope="admin:read")
R("get", "/v1/admin/observability/customers", "Customer observability rollup", t, x_scope="admin:read")
R("get", "/v1/admin/observability/workloads", "Workload observability rollup", t, x_scope="admin:read")

# ------------------------------------------------- API access management ---
t = "API Access (tokens, keys, service accounts)"
R("get", "/v1/organizations/{org_id}/api-tokens", "List org API tokens (org admin+)", t, x_scope="org:read")
R("post", "/v1/organizations/{org_id}/api-tokens", "Create org API token (raw shown once)", t, x_scope="org:write")
R("delete", "/v1/organizations/{org_id}/api-tokens/{token_id}", "Revoke org API token", t, x_scope="org:write")
R("get", "/v1/organizations/{org_id}/service-accounts", "List service accounts (platform admin)", t, x_scope="org:read")
R("post", "/v1/organizations/{org_id}/service-accounts", "Create service account (platform admin)", t, x_scope="org:write")
R("delete", "/v1/organizations/{org_id}/service-accounts/{sa_id}", "Delete service account (platform admin)", t, x_scope="org:write")
R("get", "/v1/admin/api-keys", "List platform admin API keys (epa_)", t, x_scope="admin:read")
R("post", "/v1/admin/api-keys", "Create platform admin API key (admin SESSION only; raw shown once; scope '*' = full control)", t, x_scope="admin:write")
R("delete", "/v1/admin/api-keys/{key_id}", "Revoke platform admin API key (admin SESSION only)", t, x_scope="admin:write")

# ----------------------------------------------------- packages / users ----
t = "Hosting Packages & Users"
R("get", "/v1/admin/packages", "List hosting packages", t, x_scope="admin:read")
R("post", "/v1/admin/packages", "Create hosting package", t, x_scope="admin:write")
R("patch", "/v1/admin/packages/{pkg_id}", "Update hosting package (sites converge via enforce jobs)", t, x_scope="admin:write")
R("delete", "/v1/admin/packages/{pkg_id}", "Delete hosting package", t, x_scope="admin:write")
R("post", "/v1/admin/organizations/{org_id}/package", "Assign package to organization", t, x_scope="admin:write")
R("get", "/v1/organizations/{org_id}/package", "Org package + usage (billing+)", t, x_scope="org:read")
R("get", "/v1/admin/users", "List platform users", t, x_scope="admin:read")
R("post", "/v1/admin/users", "Create user account (cPanel model: admins create customers)", t, x_scope="admin:write")

# ------------------------------------------------- admin WHM + jobs console
t = "Admin WHM Console"
R("get", "/v1/adminview/overview", "Platform overview rollup", t, x_scope="admin:read")
R("get", "/v1/adminview/servers", "All servers view", t, x_scope="admin:read")
R("get", "/v1/adminview/accounts", "All hosting accounts view", t, x_scope="admin:read")
R("get", "/v1/adminview/organizations", "All organizations view", t, x_scope="admin:read")
R("get", "/v1/adminview/domains", "All domains view", t, x_scope="admin:read")
R("get", "/v1/adminview/databases", "All databases view", t, x_scope="admin:read")
R("get", "/v1/adminview/backups", "All backups view", t, x_scope="admin:read")
R("get", "/v1/adminview/dns-zones", "All DNS zones view", t, x_scope="admin:read")
R("get", "/v1/adminview/ports", "Backend port allocations view", t, x_scope="admin:read")
R("get", "/v1/adminview/alerts", "All alerts view", t, x_scope="admin:read")
R("get", "/v1/adminview/users", "All users view", t, x_scope="admin:read")
R("get", "/v1/adminview/jobs", "All jobs view", t, x_scope="admin:read")
R("get", "/v1/adminview/jobs/dead-letter", "Dead-letter jobs view", t, x_scope="admin:read")
R("get", "/v1/adminview/live", "Live metrics frames (in-memory store)", t, x_scope="admin:read")
R("post", "/v1/adminview/accounts/{website_id}/suspend", "Suspend any account (cross-org, audited)", t, x_scope="admin:write")
R("post", "/v1/adminview/accounts/{website_id}/resume", "Resume any account (cross-org, audited)", t, x_scope="admin:write")
R("post", "/v1/adminview/jobs/{job_id}/retry", "Retry job", t, x_scope="admin:write")
R("post", "/v1/adminview/jobs/{job_id}/cancel", "Cancel job", t, x_scope="admin:write")
R("get", "/v1/jobs", "Recent/dead-letter jobs (platform admin)", t, x_scope="admin:read")

# ---------------------------------------------------------------- billing --
t = "Billing (org side)"
R("get", "/v1/organizations/{org_id}/billing/overview", "Billing overview", t, x_scope="billing:read")
R("get", "/v1/organizations/{org_id}/billing/subscriptions", "List subscriptions", t, x_scope="billing:read")
R("get", "/v1/organizations/{org_id}/billing/subscriptions/{subscription_id}", "Subscription details", t, x_scope="billing:read")
R("post", "/v1/organizations/{org_id}/billing/subscriptions/{subscription_id}/cancel", "Cancel subscription", t, x_scope="billing:write")
R("get", "/v1/organizations/{org_id}/billing/products", "List purchasable products", t, x_scope="billing:read")
R("post", "/v1/organizations/{org_id}/billing/orders", "Create order", t, x_scope="billing:write")
R("get", "/v1/organizations/{org_id}/billing/orders", "List orders", t, x_scope="billing:read")
R("post", "/v1/organizations/{org_id}/billing/orders/{order_id}/pay", "Pay order", t, x_scope="billing:write")
R("get", "/v1/organizations/{org_id}/billing/invoices", "List invoices", t, x_scope="billing:read")
R("get", "/v1/organizations/{org_id}/billing/invoices/{invoice_id}", "Invoice details", t, x_scope="billing:read")
R("post", "/v1/organizations/{org_id}/billing/invoices/{invoice_id}/pay", "Pay invoice", t, x_scope="billing:write")
R("get", "/v1/organizations/{org_id}/billing/payment-method", "Get payment method", t, x_scope="billing:read")
R("put", "/v1/organizations/{org_id}/billing/payment-method", "Set payment method", t, x_scope="billing:write")
t = "Billing (admin side)"
R("get", "/v1/admin/billing/plans", "List billing plans", t, x_scope="admin:read")
R("get", "/v1/admin/billing/products", "List all products", t, x_scope="admin:read")
R("post", "/v1/admin/billing/products", "Create product", t, x_scope="admin:write")
R("patch", "/v1/admin/billing/products/{product_id}", "Update product", t, x_scope="admin:write")
R("get", "/v1/admin/billing/invoices", "List all invoices", t, x_scope="admin:read")
R("post", "/v1/admin/billing/invoices", "Issue invoice", t, x_scope="admin:write")
R("post", "/v1/admin/billing/invoices/{invoice_id}/void", "Void invoice", t, x_scope="admin:write")
R("get", "/v1/admin/billing/settings", "Billing settings", t, x_scope="admin:read")
R("patch", "/v1/admin/billing/settings", "Update billing settings", t, x_scope="admin:write")
R("get", "/v1/admin/billing/subscriptions", "List all subscriptions", t, x_scope="admin:read")
R("post", "/v1/admin/billing/subscriptions/{subscription_id}/retry", "Retry subscription billing", t, x_scope="admin:write")
R("post", "/v1/admin/billing/subscriptions/{subscription_id}/terminate", "Terminate subscription", t, x_scope="admin:write")
R("post", "/v1/billing/webhook/{provider}", "Provider webhook (signature-authenticated)", t, False)

# ------------------------------------------------------- audit / settings --
t = "Audit, Setup & Settings"
R("get", "/v1/audit-logs", "Audit trail (org members billing+; all orgs for platform admin)", t, x_scope="audit:read")
R("get", "/v1/setup/status", "First-boot setup status", t, False)
R("post", "/v1/setup", "Complete first-boot setup", t, False)
R("post", "/v1/setup/verify-hostname", "Verify panel hostname", t, False)
R("get", "/v1/setup/software", "Installable software catalog", t, False)
R("post", "/v1/setup/software", "Install software -> job", t, False)
R("get", "/v1/setup/jobs", "Setup job feed", t, False)
R("get", "/v1/settings", "Panel settings (platform admin)", t, x_scope="admin:read")
R("patch", "/v1/settings/hostname", "Set panel hostname", t, x_scope="admin:write")

# -------------------------------------------------------------- ws/system --
t = "System"
R("get", "/v1/ws", "WebSocket event stream (session or org token; org-scoped frames)", t)
R("get", "/v1/openapi.json", "This spec", t, False)
R("get", "/healthz", "Liveness (DB ping)", t, False)
R("get", "/readyz", "Readiness gate", t, False)

# ------------------------------------------------------------------ emit ---

def build():
    paths: dict = {}
    for method, path, summary, tag, secure, extra in S:
        op = {
            "summary": summary,
            "tags": [tag],
            "security": secure,
            "responses": {
                "200": {"description": "ok"},
                "default": {"description": "error", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}},
            },
        }
        if extra:
            op.update(extra)
        if "{org_id}" in path or "{website_id}" in path:
            op.setdefault("parameters", [])
        paths.setdefault(path, {})[method.lower()] = op

    tag_names = []
    for _, _, _, tag, _, _ in S:
        if tag not in tag_names:
            tag_names.append(tag)

    spec = {
        "openapi": "3.0.3",
        "info": {
            "title": "EpicPanel API",
            "version": "1.0.0",
            "description": (
                "EpicPanel hosting control plane. Canonical prefix: /api/v1 (the /v1 spelling is equivalent).\n\n"
                "Principal types (Authorization: Bearer <token>):\n"
                "- Session: browser cookie or session token (full role of the user).\n"
                "- `epk_` org API token: org-confined automation identity; deny-by-default scopes\n"
                "  (x_scope on each operation). Never inherits platform-admin. CSRF-exempt.\n"
                "- `epa_` platform admin API key: operates across ALL organizations with the\n"
                "  platform-admin role; needs the org scope for /v1/organizations/... routes and\n"
                "  admin:read/admin:write for the /v1/admin* + /v1/jobs surface. Create/revoke via\n"
                "  admin SESSION only (POST/DELETE /v1/admin/api-keys).\n"
                "- `agt_` agent token: data-plane agent channel only.\n\n"
                "Mutating infrastructure operations (provision, install, deploy, backup, DNS sync, ...)\n"
                "return 202 with a job_id; poll GET .../jobs/{id} or subscribe to GET /v1/ws.\n\n"
                "Errors: JSON {error: {code, message, details?}}. Rate limits: 10/min auth, 300/min API."
            ),
        },
        "servers": [{"url": "/"}],
        "tags": [{"name": t} for t in tag_names],
        "paths": paths,
        "components": {
            "securitySchemes": {
                "bearer": {"type": "http", "scheme": "bearer", "description": "Session token, epk_ org token, epa_ platform key, or agt_ agent token"},
            },
            "schemas": {
                "Error": {
                    "type": "object",
                    "properties": {
                        "error": {
                            "type": "object",
                            "properties": {
                                "code": {"type": "string"},
                                "message": {"type": "string"},
                                "details": {},
                            },
                        }
                    },
                }
            },
        },
    }
    return spec


if __name__ == "__main__":
    spec = build()
    out = json.dumps(spec, indent=1, ensure_ascii=False) + "\n"
    with open("openapi.json", "w") as f:
        f.write(out)
    n_ops = len(S)
    print(f"openapi.json written: {len(spec['paths'])} paths, {n_ops} operations")

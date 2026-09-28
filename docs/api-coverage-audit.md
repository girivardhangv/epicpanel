# API Coverage Audit (2026-09-27)

Companion to ADR-068/069 work: an audit of panel features vs REST endpoints,
plus the gaps found and what was built to close them. Source of truth for the
route inventory: `internal/api/phase12_security_test.go` (phase12Routes).

## Already fully API-backed (no action needed)

Every UI operation in the platform panel, customer cPanel (`/customer`) and
admin WHM (`/admin`) maps to a REST endpoint:

- Websites lifecycle (create/delete/suspend/resume/terminate/purge/reconcile),
  runtime switches, docroot, running-dir options
- Domains, aliases, SSL modes, DNS verify; DNS zones + records + publish
- Databases: create/list/reveal/drop + phpMyAdmin/Adminer SSO + database-tools deploy
- File manager: list/read/write/create/rename(=move)/delete/download/upload
- Crons, FTP accounts, SSH keys, web terminal (WS), backups (manual + scheduled
  + restore + verify), staging clone/promote, git deploys + rollback
- Applications (node/python/go): create/config/start/stop/restart/logs/env
- One-clicks: WordPress, Laravel; site commands; PHP settings; bandwidth
  (quota, history, recalc); alerts; dynamic resources; Free Perk
- Admin surface: users, packages, billing (products/plans/invoices/orders/
  subscriptions), api-keys, adminview (accounts/databases/jobs/live/...),
  observability, alert rules, service accounts, software installer, MFA

## Gaps found → built in this change set

| Gap | Endpoint(s) added | Notes |
|---|---|---|
| Archive extraction (zip/tar/tar.gz) | `POST .../websites/{id}/files/extract` | zip-slip name validation, symlink/device entry refusal, 512 MB/entry + 2 GB total + 20k entry caps |
| Recursive copy | `POST .../websites/{id}/files/copy` | boundary-safe, refuses copying a dir into itself, 2 GB cap |
| Zip/compress selection | `POST .../websites/{id}/files/compress` | produces a standard zip inside the site tree |
| Move | existing `PATCH .../files` (rename) | UI action added (destination-folder prompt) in both file managers |
| WHM-style service restart | `POST .../servers/{server_id}/services/{service}/restart` | strict unit allowlist (nginx, apache2, lsws, openlitespeed, php<ver>-fpm, mysql, mariadb, postgresql); new `restart_service` agent job (migration 0057); agent re-validates + verifies `is-active` after restart; platform-admin only |
| On-demand serving reconcile | `POST .../websites/{website_id}/reconcile` | re-enqueues the idempotent provision job (same path as the hourly sweep); admin+; ops recovery without waiting for the sweep |

## Known remaining gaps (deliberate)

- **Backup archive download** — backups live on the agent's filesystem; the
  agent protocol (claim/result jobs) has no file-streaming channel. Would
  need a designed transfer path (chunked job results or an agent-side
  authenticated fetch); not a quick endpoint.
- **Email** — feature explicitly deferred (no backend).
- **Dedicated IP pool** — not implemented (admin UI notes this).
- **Support/ticket queue** — not implemented (admin UI notes this).

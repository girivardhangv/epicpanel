# apps/customer (cPanel experience)

Phase 5 customer control panel. Consumes `packages/{core,ui,icons,charts,tables,forms,design-system}`
— never fork components: the admin WHM app (Phase 6) reuses the same packages.

## Run

```sh
npm run dev:customer     # http://localhost:5174  (proxies /v1 -> 127.0.0.1:8080, ws:true)
npm run build            # typecheck + root app + customer app
```

## Screens

Dashboard (WS-live resource cards + freshness badges), Websites, Domains,
DNS zone editor, File Manager, FTP accounts, Databases (+phpMyAdmin SSO),
PHP versions & extensions, Cron Jobs, Backups, Metrics history, SSL/TLS,
Security (2FA + API keys), Account.

Email accounts: DEFERRED — the backend module does not exist yet (see
`phases/phase-05-customer-cpanel.md` handoff). No fake data is rendered.

Terminal & SSH keys are intentionally absent from this app: admin-only,
enforced server-side (admin rank) as well as in the UI.

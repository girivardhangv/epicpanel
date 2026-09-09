# EpicPanel Customer Guide

Welcome to EpicPanel. This guide covers the everyday tasks from the
customer panel. If something here does not match what you see, your
provider may have customized the menu.

## 1. Log in

- Open your panel URL (your provider sends it, e.g.
  `https://panel.example.com`).
- First login: use the invite link from your welcome email; it walks you
  through setting your password. Enable two-factor authentication in
  Account → Security (TOTP app; recovery codes are shown once — save them).

## 2. Websites (shared hosting)

Create: Websites → Add website → pick a domain, PHP version, and
template. Provisioning runs in the background and the card flips to
Ready when done (usually under a minute).

- **Files**: built-in file manager, or SFTP (Accounts → FTP; SFTP uses
  your site's SSH account).
- **Databases**: Databases → create; the panel shows the DSN and can
  open phpMyAdmin (Adminer gate) with one click.
- **Domains & DNS**: Domains → add; point the nameservers your provider
  gave you, or manage records in the DNS editor. SSL certificates are
  issued automatically and renew themselves.
- **PHP**: change version / extensions per site (Software → PHP).
- **Staging**: Websites → Staging → clone; test there, then Promote to
  push it live. Promotion is a background job — you will get an event in
  the bell menu.
- **Usage**: the site card shows CPU/RAM/disk against your plan in real
  time (LIVE badge = current data; STALE means the node hasn't reported
  in a few seconds).

## 3. WordPress

Websites → WordPress → install (pick the site, admin user, password).
Updates for plugins/themes are listed under WordPress in the site menu;
backups taken before updates are restorable from Backups.

## 4. Minecraft servers

Minecraft → Create server → pick the plan, Minecraft version, and
region. When it is Ready:

- Start / Stop / Restart from the server card.
- Console tab: live server console (you can type commands).
- `server.properties`, plugins, and world files are editable in Files.
- Backups: Backups tab — world backups are instant snapshots; restore
  replaces the current world, so export first if unsure.
- Player count, TPS, and MSPT are on the live graph.

## 5. Discord bots

Bots → Add bot → paste your bot token (from the Discord developer
portal), pick the runtime (Python/Node), and upload your code (git URL
or zip). Deployments list each build; the Logs tab streams stdout.
Environment variables (secrets) are edited in the bot's Settings and
injected at runtime. Schedules (restarts, tasks) are under Schedules.

## 6. Billing

Billing → Subscriptions shows your plans, next invoice dates, and usage.
Invoices appear under Invoices; payment methods under Payment methods.

- If a payment fails, your account enters a grace period (you get an
  email with the date); services suspend after the grace window if
  unpaid, and terminate after the final notice. Everything is shown on
  the subscription card — no surprises.
- Upgrades/downgrades: Change plan on the subscription; provisioning
  adjusts your limits and prorates the invoice.

## 7. Backups

Backups tab per site/server/bot:

- Scheduled backups run automatically per your plan (see the schedule
  row); you can also Run now.
- Each backup shows verified status and size. Verified = the panel
  re-checked the artifact after upload.
- Restore: pick the backup → Restore. For websites this runs as a job
  (watch the bell/events); for Minecraft worlds it is a world-level
  replace. Restores never leave a partial state marked complete.
- Keep important data off-node too: backups protect against mistakes and
  hardware failure on the node, but your provider's retention policy is
  the final word (ask support for yours).

## 8. Alerts and status

The bell icon shows events for your resources: jobs finished, backups,
billing notices, alerts (e.g. `website down`). If your site is flagged
down, check: domain expiry/DNS, then disk quota, then contact support
with the timestamp — the panel timestamps are the same ones support sees.

## 9. Good to know

- Everything you start in the panel is a background job; the bell and
  the task list show progress. You never lose your place by navigating
  away.
- The LIVE/STALE badge on metrics is honest: STALE means we have no
  fresh sample — it is never faked as live.
- Two-factor is strongly recommended; API tokens (Account → API tokens)
  are scoped to one organization and can be revoked instantly.

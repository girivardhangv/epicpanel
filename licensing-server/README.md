# EpicPanel Licensing Server

A lightweight, dependency-free licensing backend for EpicPanel. Plain PHP + SQLite
(no Composer, no DB server, no frameworks). Implements exactly the contract the
panel expects:

| Endpoint | Request body | Response |
|----------|--------------|----------|
| `POST /v1/activate`   | `{license_key, fingerprint}` | LicenseResponse |
| `POST /v1/validate`   | `{fingerprint}`             | LicenseResponse |
| `POST /v1/deactivate` | `{license_id, fingerprint}` | `{"ok":true}` |
| `GET  /v1/health`     | —                           | `{"ok":true}` |

`fingerprint` is the panel's persistent `instance_id` (a UUID), so a license key
cannot float between servers.

## Requirements

- PHP 8.0+ with `pdo_sqlite` (bundled by default in all distro packages)
- Any web server that can run PHP (nginx + php-fpm, Apache, or the built-in server)

## Quick start (development)

```bash
php -S 0.0.0.0:9911 -t licensing-server licensing-server/index.php
```

## Install on an EXISTING hosting panel (cPanel / aaPanel / DirectAdmin / ...)

You already have a VPS with a web hosting panel — **you don't need `deploy.sh`**.
This is plain PHP + SQLite, so it runs as a normal website on your panel:

1. **Create a website / subdomain** in your panel, e.g. `license.example.com`
   (document root e.g. `~/license.example.com/public_html`).

2. **Upload these files** into that document root:
   ```
   index.php
   lib.php
   .htaccess          (routes /v1/activate -> index.php)
   var/               (writable SQLite folder; already contains a deny .htaccess)
   ```
   Leave `cli.php` on the server but it can stay too — it refuses to run over HTTP.

3. **Set the PHP version** to **8.0+** in the panel (any recent version works).

4. **Make sure `var/` is writable** by the web user so the SQLite DB can be created:
   - cPanel: right-click → Permissions → 755 on the `var` folder, and 775 if needed.
   - aaPanel: File manager → `var` → Permissions → 755, owner = www.
   (Or, simpler: pre-create the DB from the shell — see below — so the web server
   only ever reads it.)

5. **Verify** it's live:
   ```bash
   curl https://license.example.com/v1/health
   # {"ok":true,"service":"epicpanel-licensing"}
   ```

6. **Point panels at it:**
   ```bash
   EPICPANEL_LICENSE_API_URL=https://license.example.com
   ```

> Tip: the SQLite file is stored at `var/licensing.db`. If your panel doesn't let
> the web user write files, create the DB from the shell first:
> `php cli.php list` (this builds the schema), then ensure `var/licensing.db`
> is readable by the web user (e.g. `chmod 644 var/licensing.db`).

## Standalone install on a fresh VPS (no existing panel)

```bash
curl -fsSL https://get.epichostly.in/licensing | bash -s -- --domain license.example.com
```

This installs PHP + SQLite + nginx, sets up a systemd service, gets a Let's
Encrypt TLS cert and opens the firewall. Full source: `deploy.sh`.

## Production (nginx + php-fpm)

1. Copy this folder to `/srv/licensing`.
2. Ensure the `var/` directory is writable by php-fpm (the SQLite DB lives there):
   ```bash
   mkdir -p /srv/licensing/var && chown www-data:www-data /srv/licensing/var
   ```
3. nginx site:
   ```nginx
   server {
       listen 80;
       server_name licenses.example.com;
       root /srv/licensing;
       index index.php;

       location / {
           try_files $uri $uri/ /index.php$is_args$args;
       }
       location ~ \.php$ {
           include fastcgi_params;
           fastcgi_pass unix:/run/php/php8.2-fpm.sock;   # adjust version
           fastcgi_param SCRIPT_FILENAME $document_root/index.php;
       }
   }
   ```
4. Restart nginx + php-fpm. Optionally add TLS via certbot.

## Managing licenses (CLI)

```bash
# Create a license key
php cli.php generate --plan starter --seats 1 --days 365 --name "Acme Corp" --features "nginx,php,db"

# Create a lifetime license
php cli.php generate --plan pro --seats 5 --name "Beta Tester"

# List all licenses
php cli.php list

# Revoke / suspend / unsuspend by key or ID
php cli.php revoke EPIC-XXXXX-XXXXX-XXXXX-XXXXX
php cli.php suspend EPIC-XXXXX-XXXXX-XXXXX-XXXXX

# Check what a fingerprint (installation) currently validates to
php cli.php status <instance-id>
```

## Wiring the panel to it

In the installer or the panel's config, set:

```bash
EPICPANEL_LICENSE_API_URL=https://licenses.example.com
```

The panel then calls `POST /v1/activate` during first-run with the license key
you generated, and re-validates periodically.

## Data

- `var/licensing.db` — SQLite database (WAL mode). Back it up with your normal
  backup routine, or use `sqlite3 var/licensing.db ".backup backup.db"`.
- License keys are stored **hashed** (SHA-256); the plaintext is shown only once
  at generation time.

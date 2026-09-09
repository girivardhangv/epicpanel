# EpicPanel on a VPS — Install / Update / Uninstall guide

## 1. Requirements

- Ubuntu 20.04+ / Debian 11+ (apt), root access
- 1 vCPU / 1 GB RAM minimum (2 GB comfortable)
- Ports: 22 (ssh), 8080 (panel, or 80/443 behind nginx), 80/443 for customer sites later

## 2. Install (one command)

```bash
curl -fsSL https://get.epichostly.in | bash
```

or from the repo:

```bash
git clone <your-repo> && cd epicpanel-2
sudo bash install.sh
```

What it does: installs PostgreSQL + nginx, creates the `epicpanel` DB role
(random password in `/opt/epicpanel/db_password`), installs the two binaries
(`epicpanel-api`, `epicpanel-agent`) as systemd services, runs migrations,
enrolls the local agent, and prints a **one-time setup link** (valid 1 h).
Open it in the browser to become the admin (install required software, verify
hostname, create the admin account).

Reprint the setup link anytime:

```bash
sudo epicpanel-api setup-token
```

## 3. Where things live

| Path | What |
|---|---|
| `/usr/local/bin/epicpanel-api` | control plane binary (`.prev` = last release) |
| `/usr/local/bin/epicpanel-agent` | node agent binary (`.prev` = last release) |
| `/etc/systemd/system/epicpanel-{api,agent}.service` | services |
| `/etc/epicpanel/api.env` | DB URL, secret key, bind addr |
| `/etc/epicpanel/agent.env` | agent token + control plane URL |
| `/var/backups/epicpanel/` | pre-migrate dumps (7 kept) |
| `/srv/epicpanel/` | customer websites + agent data (never touched by installer/uninstaller) |

## 4. Update (you have an updater)

Same script is the updater — backups the DB **before** migrating, keeps the old
binaries as `.prev`:

```bash
sudo epicpanel-update          # helper installed on the box
# or:
sudo bash install.sh update
# or simply re-run:
curl -fsSL https://get.epichostly.in | bash
```

## 5. Rollback a bad update

```bash
sudo bash install.sh rollback   # restores .prev binaries and restarts
# DB restore if a migration broke something:
ls -1t /var/backups/epicpanel/epicpanel-pre-migrate-*.sql.gz | head -1
gunzip -c <file> | sudo -u postgres psql epicpanel
```

## 6. Uninstall

```bash
sudo bash install.sh uninstall            # removes services + binaries, KEEPS DB + config + backups
sudo bash install.sh uninstall --dry-run  # preview only
sudo bash install.sh uninstall --purge    # also drops DB, role, /etc/epicpanel, backups
```

`/srv/epicpanel` (customer websites) is **never** removed automatically —
manual `rm -rf` only when you are certain. nginx/postgres packages stay
installed (other services may use them).

## 7. Serving the installer: Cloudflare Worker

`deploy/cloudflare/installer-worker.js` powers:

- `get.epichostly.in` → serves `install.sh` (from R2, 60 s edge cache)
- `downloads.epichostly.in/latest/<binary>` → resolves `latest.json` and
  streams the binary from R2 (versioned objects are immutable-cached)

One-time setup:

```bash
npm i -g wrangler
wrangler login
wrangler r2 bucket create epicpanel-releases

# publish the installer
wrangler r2 object put epicpanel-releases/installer/install.sh --file install.sh

# publish binaries (build first: cd backend && ./build-release.sh <version>)
wrangler r2 object put epicpanel-releases/releases/v1.0.0/epicpanel-api-linux-amd64 --file bin/epicpanel-api-linux-amd64
wrangler r2 object put epicpanel-releases/releases/v1.0.0/epicpanel-agent-linux-amd64 --file bin/epicpanel-agent-linux-amd64
# arm64 the same way with -linux-arm64

# point "latest" at the new version
echo '{"version":"v1.0.0"}' > latest.json
wrangler r2 object put epicpanel-releases/releases/latest.json --file latest.json

# deploy the worker (fill your hosts in wrangler.toml inside the file's comment)
cd deploy/cloudflare && wrangler deploy
```

Then in Cloudflare DNS, add the two hostnames as **custom domains** on the
worker (get.epichostly.in, downloads.epichostly.in).

### Releasing a new version

```bash
cd backend && ./build-release.sh v1.0.1          # builds 4 binaries into bin/
./build-release.sh upload v1.0.1                 # wrangler puts them to R2
echo '{"version":"v1.0.1"}' > latest.json && wrangler r2 object put epicpanel-releases/releases/latest.json --file latest.json
```

Users update with `sudo epicpanel-update`.

## 8. DNS / panel hostname

Point an A record at the VPS for the panel hostname you'll verify in setup
(e.g. `panel.example.com`). The setup wizard checks it. For customer sites,
customers point their domains at the VPS IP (or a wildcard `*.example.com`).

# EpicPanel on a VPS — Install / Update / Uninstall guide

## 1. Requirements

- Ubuntu 20.04+ / Debian 11+ (apt), root access
- 1 vCPU / 1 GB RAM minimum (2 GB comfortable)
- Ports: 22 (ssh), 8080 (panel, or 80/443 behind nginx), 80/443 for customer sites later

## 2. Install (one command)

```bash
curl -fsSL https://raw.githubusercontent.com/girivardhangv/epicpanel/main/install.sh | bash
```

or from the repo:

```bash
git clone https://github.com/girivardhangv/epicpanel.git && cd epicpanel
sudo bash install.sh
```

What it does: installs PostgreSQL + nginx, creates the `epicpanel` DB role
(random password in `/opt/epicpanel/db_password`), installs the two binaries
(`epicpanel-api`, `epicpanel-agent`) as systemd services, runs migrations,
enrolls the local agent, and prints a **one-time setup link** (valid 1 h).
Open it in the browser to become the admin (install required software, verify
hostname, create the admin account).

If the link does not open, a firewall is blocking 8080 — the installer
opens ufw itself; cloud security groups must be allowed by hand
(`sudo ufw allow 8080/tcp` + provider dashboard), then reprint.
On an already-set-up panel `setup-token` prints the login URL instead
of minting a link (the wizard is one-shot).

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
curl -fsSL https://raw.githubusercontent.com/girivardhangv/epicpanel/main/install.sh | bash
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

## 7. Serving the installer: GitHub Releases

The installer script is served directly from GitHub, and pulls the compiled binaries from GitHub Releases automatically.

### Releasing a new version

To publish a new release:
1. Ensure you have the GitHub CLI (`gh`) installed and authenticated (`gh auth login`).
2. Run the build script:

```bash
cd backend && ./build-release.sh upload v1.0.1
```

This will compile the binaries for `amd64` and `arm64`, and create a new GitHub Release with the tag `v1.0.1`, uploading the binaries directly to it.
Users update simply by running `sudo epicpanel-update`.

## 8. DNS / panel hostname

Point an A record at the VPS for the panel hostname you'll verify in setup
(e.g. `panel.example.com`). The setup wizard checks it. For customer sites,
customers point their domains at the VPS IP (or a wildcard `*.example.com`).

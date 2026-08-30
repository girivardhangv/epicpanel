# EpicPanel Installation

**Philosophy: install EpicPanel first, install the hosting stack later.**

The one-command installer sets up **EpicPanel and its own PostgreSQL only**.
Nginx, PHP, MariaDB, Redis, Node.js, Docker, etc. are installed later from the
EpicPanel web UI (the Software Manager), only when the administrator chooses.

## One command

```bash
curl -fsSL https://get.epichostly.in | bash
```

Windows (elevated PowerShell):

```powershell
powershell -c "irm https://get.epichostly.in | iex"
```

The installer is **idempotent and resumable** — re-running it detects completed
steps and continues instead of reinstalling.

### What it does (Linux)
1. Detects OS (Debian/Ubuntu → apt, RHEL/Rocky/Alma/Fedora → dnf) and CPU arch (amd64/arm64).
2. Verifies minimum requirements (≥1 CPU, ≥1 GB RAM, ≥2 GB disk) and root privileges.
3. Installs **PostgreSQL** (EpicPanel's own database) and provisions the `epicpanel` role + database.
4. Creates the `epicpanel` system user and `/opt`, `/etc/epicpanel`, `/var/lib/epicpanel`, `/var/log/epicpanel`.
5. Downloads the EpicPanel binary from GitHub Releases and verifies its SHA-256 checksum.
6. Writes `/etc/epicpanel/epicpanel.env` (0640) and installs a hardened `systemd` unit.
7. Starts the service, health-checks `/healthz`, and prints the panel URL.

### What it does (Windows)
Same flow using winget for PostgreSQL (when available), a startup Scheduled Task
instead of systemd, and a firewall rule for the panel port.

## Overridable environment

| Variable | Default | Purpose |
| --- | --- | --- |
| `EPICPANEL_REPO` | `girivardhangv/epicpanel` | GitHub `owner/repo` to download releases from |
| `EPICPANEL_VERSION` | `latest` | Release tag (or `latest`) |
| `EPICPANEL_PORT` | `8080` | Panel listen port |
| `EPICPANEL_DATABASE_DSN` | (auto) | Supply your own Postgres DSN to skip auto-provisioning |

Example (pin a version, custom port):

```bash
curl -fsSL https://get.epichostly.in | EPICPANEL_VERSION=v1.0.0 EPICPANEL_PORT=8443 bash
```

## Distribution — is GitHub enough?

**Yes.** GitHub Releases host the binaries + `checksums.txt`; GitHub Actions
(`.github/workflows/release.yml`) cross-compiles all four targets, embeds the
web UI into a single binary, and publishes the release. Push a `v*` tag to ship.

For a polished public install you additionally want:
- **A domain** — point `get.epichostly.in` at the install script (see below).
- **Checksums** — already generated and verified by the installer.
- **Optional:** an Authenticode code-signing certificate (removes the Windows
  SmartScreen warning) and GPG-signed releases. Neither is required to function.

### Serving `https://get.epichostly.in`

`curl | bash` needs that URL to return the script. Simplest options:
- A tiny redirect/rewrite on your web host to the GitHub raw script:
  `https://raw.githubusercontent.com/girivardhangv/epicpanel/main/installer/install.sh`
- Or serve `installer/install.sh` from a static host / CDN at that URL.
- Windows: the same host should return `installer/install.ps1` for `irm`.

Detect the OS on the edge (User-Agent) or use two endpoints (`/install.sh`,
`/install.ps1`) and document both commands.

## After install

Open `http://<server-ip>:8080` to run the web setup wizard (license, admin
account, security). Then use **Software** in the panel to install the hosting
stack you actually need.

## Uninstall (Linux)

```bash
systemctl disable --now epicpanel
rm -f /etc/systemd/system/epicpanel.service && systemctl daemon-reload
rm -rf /opt/epicpanel /etc/epicpanel /var/lib/epicpanel /var/log/epicpanel /usr/local/bin/epicpanel
```

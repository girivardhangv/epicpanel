#!/usr/bin/env bash
# ============================================================================
# EpicPanel installer — one command to a running panel.
#
#   curl -fsSL https://get.epichostly.com | bash
#   (or: bash install.sh)
#
# What it does:
#   1. Installs system prerequisites (postgres, nginx) if missing
#   2. Installs/updates the epicpanel-api + epicpanel-agent binaries and services
#   3. Waits until the control plane is healthy and the agent is enrolled
#   4. Prints the one-time setup link (valid 1 hour) to open in your browser
#
# Safe to re-run: it upgrades in place and never touches website data.
#
# Ordering guarantee (Phase 15): on every run, IF the panel DB already
# exists, a timestamped pg_dump backup is taken BEFORE the new binary can
# run its migrations. Rollback note at the bottom of this file.
# ============================================================================
set -Eeuo pipefail

EPIC_DIR="/opt/epicpanel"
BIN_DIR="/usr/local/bin"
ENV_FILE="/etc/epicpanel/api.env"
DB_NAME="epicpanel"
DB_USER="epicpanel"
DB_PASS_FILE="$EPIC_DIR/db_password"

log()  { printf '\033[1;36m[epicpanel]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[epicpanel]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[epicpanel]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "Run as root: sudo bash install.sh"

command -v systemctl >/dev/null || fail "systemd is required (Ubuntu 20.04+/Debian 11+/Debian-based)"

# --- 0. Dependency check ----------------------------------------------------
# Everything the installer itself depends on. Missing pieces are installed in
# step 1, but curl/pg_dump must be judged early: backups happen before any
# migration and without pg_dump there is no backup.
for dep in curl psql pg_dump; do
  command -v "$dep" >/dev/null || MISSING="$MISSING $dep"
done
if [ -n "${MISSING:-}" ]; then
  log "Installing bootstrap dependencies ($MISSING)…"
  apt-get update -y >/dev/null 2>&1 || warn "apt-get update had warnings (continuing)"
  apt-get install -y --no-install-recommends postgresql-client >/dev/null 2>&1 \
    || fail "failed to install postgresql-client (need psql + pg_dump)"
fi
for dep in curl psql pg_dump; do
  command -v "$dep" >/dev/null || fail "dependency missing: $dep (install it manually and re-run)"
done

# --- 0. Base tools ---------------------------------------------------------
export DEBIAN_FRONTEND=noninteractive
if command -v apt-get >/dev/null; then
  log "Installing prerequisites (curl, postgresql, nginx)…"
  apt-get update -y >/dev/null 2>&1 || warn "apt-get update had warnings (continuing)"
  apt-get install -y --no-install-recommends curl ca-certificates gnupg postgresql nginx >/dev/null 2>&1 \
    || fail "failed to install prerequisites"
else
  fail "Only Debian/Ubuntu (apt) is supported by this installer right now"
fi

systemctl enable --now postgresql >/dev/null 2>&1 || true
systemctl enable --now nginx >/dev/null 2>&1 || true

# --- 1. Panel DB -----------------------------------------------------------
log "Preparing the panel database…"
mkdir -p "$EPIC_DIR"
if [ ! -f "$DB_PASS_FILE" ]; then
  tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32 >"$DB_PASS_FILE"
  chmod 600 "$DB_PASS_FILE"
fi
DB_PASS="$(cat "$DB_PASS_FILE")"
sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='$DB_USER'" | grep -q 1 \
  || sudo -u postgres psql -c "CREATE ROLE $DB_USER LOGIN PASSWORD '$DB_PASS';" >/dev/null
sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'" | grep -q 1 \
  || sudo -u postgres createdb -O "$DB_USER" "$DB_NAME" >/dev/null
sudo -u postgres psql -c "ALTER ROLE $DB_USER WITH PASSWORD '$DB_PASS';" >/dev/null

# --- 1b. Backup before migrate -----------------------------------------------
# On upgrades (DB already exists), take a timestamped dump BEFORE the new
# binary runs migrations. Migrations run lazily at api start, so this must
# precede `systemctl start epicpanel-api` (step 5). Keep the last 7.
if sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'" | grep -q 1; then
  BACKUP_DIR="/var/backups/epicpanel"
  BACKUP_FILE="$BACKUP_DIR/epicpanel-pre-migrate-$(date +%Y%m%d-%H%M%S).sql.gz"
  log "Backing up the panel DB before migration -> $BACKUP_FILE"
  mkdir -p "$BACKUP_DIR" && chmod 700 "$BACKUP_DIR"
  sudo -u postgres pg_dump "$DB_NAME" | gzip -9 >"$BACKUP_FILE" \
    || fail "pg_dump failed — refusing to continue without a pre-migrate backup"
  chmod 600 "$BACKUP_FILE"
  ls -1t "$BACKUP_DIR"/epicpanel-pre-migrate-*.sql.gz 2>/dev/null | tail -n +8 | xargs -r rm -f --
  log "Backup complete ($(du -h "$BACKUP_FILE" | cut -f1)); retention: 7 most recent"
  PRE_MIGRATE_BACKUP="$BACKUP_FILE"
fi

# --- 2. Panel secret -------------------------------------------------------
if [ -f "$ENV_FILE" ]; then
  . "$ENV_FILE"
else
  SECRET="$(tr -dc 'a-f0-9' </dev/urandom | head -c 64)"
  cat >"$ENV_FILE" <<EOF
EPICPANEL_DATABASE_URL=postgres://$DB_USER:$DB_PASS@127.0.0.1:5432/$DB_NAME?sslmode=disable
EPICPANEL_HTTP_ADDR=0.0.0.0:8080
EPICPANEL_SECRET_KEY=$SECRET
EPICPANEL_ENV=production
EOF
  chmod 600 "$ENV_FILE"
  . "$ENV_FILE"
fi

# --- 3. Binaries -----------------------------------------------------------
ARCH="$(uname -m)"; case "$ARCH" in x86_64) ARCH=amd64;; aarch64) ARCH=arm64;; esac
RELEASE_BASE="${EPICPANEL_DOWNLOAD_BASE:-https://downloads.epichostly.com/latest}"
log "Downloading EpicPanel binaries ($ARCH)…"
tmp="$(mktemp -d)"
for f in epicpanel-api epicpanel-agent; do
  curl -fsSL -o "$tmp/$f" "$RELEASE_BASE/${f}-linux-${ARCH}" \
    || fail "download $f failed — set EPICPANEL_DOWNLOAD_BASE or install from source"
  # Keep the currently-running binary as the rollback target.
  [ -f "$BIN_DIR/$f" ] && cp -a "$BIN_DIR/$f" "$BIN_DIR/$f.prev"
  install -m 0755 "$tmp/$f" "$BIN_DIR/$f"
done
rm -rf "$tmp"

# --- 4. Services -----------------------------------------------------------
log "Installing services…"
cat >/etc/systemd/system/epicpanel-api.service <<EOF
[Unit]
Description=EpicPanel Control Plane
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
EnvironmentFile=$ENV_FILE
ExecStart=$BIN_DIR/epicpanel-api
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

cat >/etc/systemd/system/epicpanel-agent.service <<EOF
[Unit]
Description=EpicPanel Agent
After=network-online.target epicpanel-api.service

[Service]
ExecStart=$BIN_DIR/epicpanel-agent run
Environment=EPICPANEL_CONTROL_PLANE_URL=http://127.0.0.1:8080
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

# Keep the FPM socket dir across reboots (tmpfs).
echo 'd /run/epicpanel/php-fpm 0755 root root -' >/etc/tmpfiles.d/epicpanel-fpm.conf
systemd-tmpfiles --create /etc/tmpfiles.d/epicpanel-fpm.conf

systemctl daemon-reload
systemctl enable --now epicpanel-api

# --- 5. Migration-before-start ----------------------------------------------
# Migrations are applied by the api binary itself at startup; starting the
# service IS the migration step. The pre-migrate backup (step 1b) is already
# on disk, so any migration failure is recoverable. Wait for health, which
# only answers 200 once migrations + listeners are up.
log "Waiting for the control plane (this also runs pending migrations)…"
for i in $(seq 1 30); do
  curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break
  [ "$i" = 30 ] && fail "control plane did not become healthy after migration — check: journalctl -u epicpanel-api${PRE_MIGRATE_BACKUP:+ (restore point: $PRE_MIGRATE_BACKUP)}"
  sleep 1
done

if ! systemctl is-active --quiet epicpanel-agent; then
  REG="$(curl -fsS -X POST http://127.0.0.1:8080/v1/internal/agent-bootstrap 2>/dev/null | tr -d '"' || true)"
  if [ -n "${REG:-}" ]; then
    "$BIN_DIR/epicpanel-agent" enroll --url http://127.0.0.1:8080 --token "$REG" --no-run >/dev/null 2>&1 || true
  fi
  systemctl enable --now epicpanel-agent || true
fi

# --- 6. Setup link ---------------------------------------------------------
log "Minting your one-time setup link (valid 1 hour)…"
ENV_LINE="$(set -a; . "$ENV_FILE"; set +a; "$BIN_DIR/epicpanel-api" setup-token 2>/dev/null || true)"
SETUP_URL="$(printf '%s' "$ENV_LINE" | sed -n 's/^PANEL_SETUP_URL=//p')"

IP="$(curl -fsS --max-time 4 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')"

cat <<EOF

  ============================================================
   EpicPanel is installed and running.

   Open the panel:

       http://$IP:8080

   One-time setup link (valid 1 hour, single use):

       $SETUP_URL

   The wizard will: install required software, verify your
   hostname, and create your admin account.

   Re-print the link later:
       sudo epicpanel-api setup-token

   Manage services:
       systemctl {status|restart} epicpanel-api epicpanel-agent

   ROLLBACK (release checklist / upgrade path):
       If this upgrade misbehaves:
         1. Stop services:      systemctl stop epicpanel-api epicpanel-agent
         2. Drop + recreate:    sudo -u postgres dropdb epicpanel
                                sudo -u postgres createdb -O epicpanel epicpanel
         3. Restore the dump taken before migration (path printed above, or):
                                ls -1t /var/backups/epicpanel/epicpanel-pre-migrate-*.sql.gz | head -1
                                gunzip -c <file> | sudo -u postgres psql epicpanel
         4. Reinstall the previous binaries (keep the prior release under
            /usr/local/bin/epicpanel-{api,agent}.prev if you want one-command
            rollback) and: systemctl start epicpanel-api epicpanel-agent
       Website data lives on the node agent; this restores the panel DB only.
   ============================================================
EOF

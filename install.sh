#!/usr/bin/env bash
# ============================================================================
# EpicPanel installer / updater / uninstaller — one script for the lifecycle.
#
#   Install (fresh VPS):
#     curl -fsSL https://raw.githubusercontent.com/girivardhangv/epicpanel/main/install.sh | bash
#     (or: bash install.sh)
#
#   Update (same script, explicit):
#     bash install.sh update
#     (or on the box: sudo epicpanel-update)
#
#   Uninstall:
#     bash install.sh uninstall            # keeps the panel DB + backups
#     bash install.sh uninstall --purge    # also drops DB, role, config, backups
#
#   Rollback binaries after a bad update:
#     bash install.sh rollback
#
# What install does:
#   1. Installs system prerequisites (postgres, nginx) if missing
#   2. Installs/updates the epicpanel-api + epicpanel-agent binaries and services
#   3. Waits until the control plane is healthy and the agent is enrolled
#   4. Prints the one-time setup link (valid 1 hour) to open in your browser
#
# Safe to re-run: it upgrades in place and never touches website data
# (/srv/epicpanel is NEVER removed by this script — manual step only).
#
# Ordering guarantee (Phase 15): on every run, IF the panel DB already
# exists, a timestamped pg_dump backup is taken BEFORE the new binary can
# run its migrations. Rollback note at the bottom of the install output.
# ============================================================================
set -Eeuo pipefail

EPIC_DIR="/opt/epicpanel"
BIN_DIR="/usr/local/bin"
ETC_DIR="/etc/epicpanel"
ENV_FILE="$ETC_DIR/api.env"
DB_NAME="epicpanel"
DB_USER="epicpanel"
DB_PASS_FILE="$EPIC_DIR/db_password"
UPDATE_HELPER="$BIN_DIR/epicpanel-update"

# Distribution comes entirely from GitHub: the installer itself from the main
# branch (raw), binaries + web bundle from the latest GitHub Release assets.
GH_REPO="girivardhangv/epicpanel"
RAW_BASE="https://raw.githubusercontent.com/$GH_REPO/main"
RELEASES_BASE="https://github.com/$GH_REPO/releases/latest/download"

log()  { printf '\033[1;36m[epicpanel]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[epicpanel]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[epicpanel]\033[0m %s\n' "$*" >&2; exit 1; }

# random_bytes N — N lowercase-hex chars without the SIGPIPE-kill trap.
# `tr </dev/urandom | head` dies under `set -o pipefail` when head closes the
# pipe (tr exits 141 → set -e aborts the whole installer silently). od reads
# a bounded block; no pipe kill possible. 2 hex chars per byte.
random_bytes() {
  local n="$1"
  head -c $(( (n + 1) / 2 )) /dev/urandom | od -An -tx1 | tr -d ' \n' | cut -c1-"$n"
}

# ERR trap: never die silently — print the failing line + location.
trap 'fail "installer aborted at line $LINENO (set -x and re-run for details)"' ERR

usage() {
  cat <<EOF
Usage: bash install.sh [command]

Commands:
  install     Install or update the panel (default when piped from curl)
  update      Alias of install (explicit upgrade path)
  agent       Install ONLY the node agent (add this VPS to an existing panel)
              --url http://panel:8080 --token <registration-token>
  uninstall   Remove services + binaries; keeps DB unless --purge
              --purge   also drop the panel DB, role, config and backups
              --dry-run print what would be removed, change nothing
  rollback    Restore the previous binaries (*.prev) after a bad update
EOF
}

# ---------------------------------------------------------------------------
# install / update
# ---------------------------------------------------------------------------
do_install() {
  [ "$(id -u)" -eq 0 ] || fail "Run as root: sudo bash install.sh"

  command -v systemctl >/dev/null || fail "systemd is required (Ubuntu 20.04+/Debian 11+/Debian-based)"

  # --- 0. Dependency check ---------------------------------------------------
  # Everything the installer itself depends on. Missing pieces are installed in
  # step 1, but curl/pg_dump must be judged early: backups happen before any
  # migration and without pg_dump there is no backup.
  MISSING=""
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
    apt-get install -y --no-install-recommends curl ca-certificates gnupg postgresql nginx sudo >/dev/null 2>&1 \
      || fail "failed to install prerequisites"
  else
    fail "Only Debian/Ubuntu (apt) is supported by this installer right now"
  fi

  systemctl enable --now postgresql >/dev/null 2>&1 || true
  systemctl enable --now nginx >/dev/null 2>&1 || true

  # --- 0b. Workload isolation + runtimes --------------------------------------
  # Docker isolates containerized app workloads (websites stay native:
  # dedicated unix user + FPM pools). Java is provisioned here as a baseline
  # managed runtime so the first java app does not have to fetch a JVM; the
  # agent still installs other majors on demand. Both steps are best-effort:
  # a host without the docker package still works (the agent falls back to
  # systemd units).
  if [ "${EPICPANEL_SKIP_DOCKER:-0}" != "1" ]; then
    log "Installing Docker (containerized app isolation)…"
    if ! command -v docker >/dev/null; then
      apt-get install -y --no-install-recommends docker.io >/dev/null 2>&1 \
        || warn "docker.io install failed — containerized apps will use systemd units until Docker is available"
    fi
    systemctl enable --now docker >/dev/null 2>&1 || true
  fi
  if ! command -v java >/dev/null; then
    log "Installing a baseline JVM (Java 21)…"
    apt-get install -y --no-install-recommends openjdk-21-jre-headless >/dev/null 2>&1 \
      || warn "openjdk-21 install failed — the agent will fetch Temurin on first java install"
  fi

  # --- 1. Panel DB -----------------------------------------------------------
  log "Preparing the panel database…"
  mkdir -p "$EPIC_DIR"
  if [ ! -f "$DB_PASS_FILE" ]; then
    random_bytes 24 >"$DB_PASS_FILE"
    chmod 600 "$DB_PASS_FILE"
  fi
  DB_PASS="$(cat "$DB_PASS_FILE")"
  sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='$DB_USER'" | grep -q 1 \
    || sudo -u postgres psql -c "CREATE ROLE $DB_USER LOGIN PASSWORD '$DB_PASS';" >/dev/null
  sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'" | grep -q 1 \
    || sudo -u postgres createdb -O "$DB_USER" "$DB_NAME" >/dev/null
  sudo -u postgres psql -c "ALTER ROLE $DB_USER WITH PASSWORD '$DB_PASS';" >/dev/null

  # --- 1b. Backup before migrate ----------------------------------------------
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
    # shellcheck disable=SC1090
    . "$ENV_FILE"
  else
    mkdir -p "$ETC_DIR" && chmod 700 "$ETC_DIR"
    SECRET="$(random_bytes 64)"
    cat >"$ENV_FILE" <<EOF
EPICPANEL_DATABASE_URL=postgres://$DB_USER:$DB_PASS@127.0.0.1:5432/$DB_NAME?sslmode=disable
EPICPANEL_HTTP_ADDR=0.0.0.0:8080
EPICPANEL_SECRET_KEY=$SECRET
EPICPANEL_ENV=production
EOF
    chmod 600 "$ENV_FILE"
    # shellcheck disable=SC1090
    . "$ENV_FILE"
  fi

  # --- 2b. Public URL ---------------------------------------------------------
  # The setup link (and any future user-facing links) must show the PUBLIC
  # address, not the box's private IP. Detect once, store in the env file.
  if [ -z "${EPICPANEL_PUBLIC_URL:-}" ]; then
    PUB_IP="$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}')"
    EPICPANEL_PUBLIC_URL="http://${PUB_IP:-$(hostname -I | awk '{print $1}')}:$(printf '%s' "$EPICPANEL_HTTP_ADDR" | awk -F: '{print $2}')"
    grep -q '^EPICPANEL_PUBLIC_URL=' "$ENV_FILE" 2>/dev/null \
      || printf 'EPICPANEL_PUBLIC_URL=%s\n' "$EPICPANEL_PUBLIC_URL" >>"$ENV_FILE"
  fi

  # --- 3. Binaries -----------------------------------------------------------
  ARCH="$(uname -m)"; case "$ARCH" in x86_64) ARCH=amd64;; aarch64) ARCH=arm64;; esac
  RELEASE_BASE="${EPICPANEL_DOWNLOAD_BASE:-$RELEASES_BASE}"
  log "Downloading EpicPanel binaries ($ARCH)…"
  tmp="$(mktemp -d)"
  for f in epicpanel-api epicpanel-agent; do
    curl -fsSL -o "$tmp/$f" "$RELEASE_BASE/${f}-linux-${ARCH}" \
      || fail "download $f failed — set EPICPANEL_DOWNLOAD_BASE or install from source"
    # Keep the currently-running binary as the rollback target.
    [ -f "$BIN_DIR/$f" ] && cp -a "$BIN_DIR/$f" "$BIN_DIR/$f.prev"
    install -m 0755 "$tmp/$f" "$BIN_DIR/$f"
  done

  # --- 3a. Panel web UI --------------------------------------------------------
  # The API serves the built SPA from EPICPANEL_WEB_DIR (default
  # /opt/epicpanel/web) — one port, one process. Skip gracefully when the
  # bundle is missing (API-only mode; UI served by dev servers / nginx).
  if [ "${EPICPANEL_SKIP_WEB:-0}" != "1" ]; then
    log "Downloading the panel web UI…"
    if curl -fsSL -o "$tmp/web-dist.tar.gz" "$RELEASE_BASE/web-dist.tar.gz" 2>/dev/null; then
      mkdir -p "$EPIC_DIR/web"
      tar -xzf "$tmp/web-dist.tar.gz" -C "$EPIC_DIR/web"
      grep -q '^EPICPANEL_WEB_DIR=' "$ENV_FILE" 2>/dev/null \
        || printf 'EPICPANEL_WEB_DIR=%s/web\n' "$EPIC_DIR" >>"$ENV_FILE"
      log "Web UI installed -> $EPIC_DIR/web"
    else
      warn "web bundle not found in this release channel — installing API-only"
    fi
  fi
  rm -rf "$tmp"

  # --- 3b. One-command updater -----------------------------------------------
  # The installer URL is captured at install time so the box can self-update:
  #   sudo epicpanel-update
  INSTALL_URL="${EPICPANEL_INSTALL_URL:-$RAW_BASE/install.sh}"
  if [ -n "$INSTALL_URL" ]; then
    cat >"$UPDATE_HELPER" <<EOF
#!/usr/bin/env bash
# EpicPanel self-updater: re-runs the installer (which backs up the DB first).
set -Eeuo pipefail
[ "\$(id -u)" -eq 0 ] || { echo "run as root: sudo epicpanel-update"; exit 1; }
curl -fsSL "$INSTALL_URL" | bash -s -- update
EOF
    chmod 0755 "$UPDATE_HELPER"
  fi

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
EnvironmentFile=-$ETC_DIR/agent.env
Environment=EPICPANEL_CONTROL_PLANE_URL=http://127.0.0.1:8080
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

  # Keep the FPM socket dir across reboots (tmpfs).
  echo 'd /run/epicpanel/php-fpm 0755 root root -' >/etc/tmpfiles.d/epicpanel-fpm.conf
  systemd-tmpfiles --create /etc/tmpfiles.d/epicpanel-fpm.conf

  # Ensure base directories exist with world-traversal permissions for web servers (0755).
  mkdir -p /srv/epicpanel/websites /srv/epicpanel/releases
  chmod 0755 /srv /srv/epicpanel /srv/epicpanel/websites /srv/epicpanel/releases

  systemctl daemon-reload
  # enable --now alone does NOT restart an already-running service — upgrades
  # would keep the old binary + old env (web dir, public URL) in memory.
  systemctl enable epicpanel-api >/dev/null 2>&1 || true
  systemctl restart epicpanel-api

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
      mkdir -p "$ETC_DIR"
      printf 'EPICPANEL_CONTROL_PLANE_URL=http://127.0.0.1:8080\nEPICPANEL_AGENT_TOKEN=%s\n' "$REG" >"$ETC_DIR/agent.env"
      chmod 600 "$ETC_DIR/agent.env"
    fi
    systemctl enable epicpanel-agent >/dev/null 2>&1 || true
    systemctl restart epicpanel-agent || true
  else
    # Env may have changed (web dir, public URL) — pick up the new agent env too.
    systemctl restart epicpanel-agent || true
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

   Update the panel later:
       sudo epicpanel-update        (or re-run this script)

   Manage services:
       systemctl {status|restart} epicpanel-api epicpanel-agent

   Uninstall:
       bash install.sh uninstall            (keeps DB + backups)
       bash install.sh uninstall --purge    (also drops DB/config)

   ROLLBACK (release checklist / upgrade path):
       If this upgrade misbehaves:
         bash install.sh rollback
       Manual path:
         1. Stop services:      systemctl stop epicpanel-api epicpanel-agent
         2. Drop + recreate:    sudo -u postgres dropdb epicpanel
                                sudo -u postgres createdb -O epicpanel epicpanel
         3. Restore the dump taken before migration (path printed above, or):
                                ls -1t /var/backups/epicpanel/epicpanel-pre-migrate-*.sql.gz | head -1
                                gunzip -c <file> | sudo -u postgres psql epicpanel
         4. Reinstall the previous binaries (kept as
            /usr/local/bin/epicpanel-{api,agent}.prev) and: systemctl start epicpanel-api epicpanel-agent
       Website data lives on the node agent; this restores the panel DB only.
   ============================================================
EOF
}

# ---------------------------------------------------------------------------
# rollback — restore the previous binaries (*.prev)
# ---------------------------------------------------------------------------
do_rollback() {
  [ "$(id -u)" -eq 0 ] || fail "Run as root: sudo bash install.sh rollback"
  local restored=0
  for f in epicpanel-api epicpanel-agent; do
    if [ -f "$BIN_DIR/$f.prev" ]; then
      systemctl stop "$f" 2>/dev/null || true
      cp -a "$BIN_DIR/$f.prev" "$BIN_DIR/$f"
      log "rolled back $f"
      restored=1
    fi
  done
  [ "$restored" = 1 ] || fail "no .prev binaries found (nothing to roll back)"
  systemctl start epicpanel-api && sleep 3
  systemctl start epicpanel-agent || true
  systemctl is-active --quiet epicpanel-api && log "control plane healthy again" \
    || warn "check: journalctl -u epicpanel-api"
}

# ---------------------------------------------------------------------------
# uninstall — remove the panel; website data (/srv/epicpanel) is NEVER touched
# ---------------------------------------------------------------------------
do_uninstall() {
  [ "$(id -u)" -eq 0 ] || fail "Run as root: sudo bash install.sh uninstall"

  local PURGE=0 DRY=0
  for arg in "$@"; do
    case "$arg" in
      --purge)   PURGE=1 ;;
      --dry-run) DRY=1 ;;
      *) usage; exit 1 ;;
    esac
  done

  run() {
    if [ "$DRY" = 1 ]; then log "would run: $*"; else "$@"; fi
  }

  log "Stopping and disabling services…"
  run systemctl disable --now epicpanel-agent 2>/dev/null || true
  run systemctl disable --now epicpanel-api 2>/dev/null || true

  log "Removing binaries, unit files and helper…"
  run rm -f "$BIN_DIR/epicpanel-api" "$BIN_DIR/epicpanel-agent" \
            "$BIN_DIR/epicpanel-api.prev" "$BIN_DIR/epicpanel-agent.prev" "$UPDATE_HELPER"
  run rm -f /etc/systemd/system/epicpanel-api.service /etc/systemd/system/epicpanel-agent.service
  run rm -f /etc/tmpfiles.d/epicpanel-fpm.conf
  run systemctl daemon-reload

  if [ "$PURGE" = 1 ]; then
    log "Purge requested: removing /etc/epicpanel (agent token, panel secret, SSL state)…"
    warn "EPICPANEL_SECRET_KEY is deleted — stored secrets (backup creds, MFA seeds) become permanently undecryptable."
    if [ "$DRY" = 0 ]; then
      [ -f "$EPIC_DIR/secret.key" ] && cp -a "$EPIC_DIR/secret.key" "$EPIC_DIR/secret.key.removed-$(date +%s)" \
        && warn "kept a copy at $EPIC_DIR/secret.key.removed-* (delete manually when sure)"
    fi
    run rm -rf "$ETC_DIR"
    log "Purge: dropping the panel database and role…"
    run sudo -u postgres psql -c "DROP DATABASE IF EXISTS $DB_NAME WITH (FORCE);" || true
    run sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='$DB_USER'" | grep -q 1 \
      && run sudo -u postgres psql -c "DROP ROLE IF EXISTS $DB_USER;" || true
    log "Purge: removing $EPIC_DIR and pre-migrate backups…"
    run rm -rf "$EPIC_DIR" /var/backups/epicpanel
  else
    log "Keeping the panel DB, $ETC_DIR and /var/backups/epicpanel (re-install to reuse them)."
    log "Drop them later with: bash install.sh uninstall --purge"
  fi

  cat <<'EOF'

  Uninstall complete.

  NOT removed (on purpose — customer data):
    /srv/epicpanel          websites, runtime installs, backups on disk
  Remove manually ONLY if you are certain:
    rm -rf /srv/epicpanel
    (databases live inside postgres/mysql/mariadb data dirs — dump first!)

  nginx/postgres packages were left installed (system services may use them).
EOF
}

# ---------------------------------------------------------------------------
# agent — node-only install (attach this VPS to an existing panel)
# ---------------------------------------------------------------------------
do_agent() {
  [ "$(id -u)" -eq 0 ] || fail "Run as root: sudo bash install.sh agent --url … --token …"

  local URL="" TOKEN=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --url)   URL="$2"; shift 2 ;;
      --token) TOKEN="$2"; shift 2 ;;
      *) fail "unknown flag: $1 (usage: agent --url http://panel:8080 --token TOKEN)" ;;
    esac
  done
  [ -n "$URL" ] || fail "--url is required (your panel's address, e.g. http://PANEL_IP:8080)"
  [ -n "$TOKEN" ] || fail "--token is required (generate in the panel: Servers -> Connect Server)"
  command -v systemctl >/dev/null || fail "systemd is required (Ubuntu 20.04+/Debian 11+)"

  log "Installing the EpicPanel node agent…"

  # Node-only installs also need the workload isolation + runtime prerequisites
  # (same rationale as do_install step 0b).
  export DEBIAN_FRONTEND=noninteractive
  if command -v apt-get >/dev/null; then
    apt-get update -y >/dev/null 2>&1 || warn "apt-get update had warnings (continuing)"
    if [ "${EPICPANEL_SKIP_DOCKER:-0}" != "1" ] && ! command -v docker >/dev/null; then
      apt-get install -y --no-install-recommends docker.io ca-certificates curl >/dev/null 2>&1 \
        || warn "docker.io install failed — containerized apps will use systemd units until Docker is available"
      systemctl enable --now docker >/dev/null 2>&1 || true
    fi
    if ! command -v java >/dev/null; then
      apt-get install -y --no-install-recommends openjdk-21-jre-headless >/dev/null 2>&1 \
        || warn "openjdk-21 install failed — the agent will fetch Temurin on first java install"
    fi
  fi

  ARCH="$(uname -m)"; case "$ARCH" in x86_64) ARCH=amd64;; aarch64) ARCH=arm64;; esac
  RELEASE_BASE="${EPICPANEL_DOWNLOAD_BASE:-$RELEASES_BASE}"
  tmp="$(mktemp -d)"
  curl -fsSL -o "$tmp/epicpanel-agent" "$RELEASE_BASE/epicpanel-agent-linux-${ARCH}" \
    || fail "download epicpanel-agent failed — check EPICPANEL_DOWNLOAD_BASE"
  [ -f "$BIN_DIR/epicpanel-agent" ] && cp -a "$BIN_DIR/epicpanel-agent" "$BIN_DIR/epicpanel-agent.prev"
  install -m 0755 "$tmp/epicpanel-agent" "$BIN_DIR/epicpanel-agent"
  rm -rf "$tmp"

  log "Enrolling with the control plane at $URL …"
  "$BIN_DIR/epicpanel-agent" enroll --url "$URL" --token "$TOKEN" --no-run \
    || fail "enrollment failed — token expired/used? Generate a new one in the panel (Servers -> Connect Server)"

  cat >/etc/systemd/system/epicpanel-agent.service <<EOF
[Unit]
Description=EpicPanel Agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=$BIN_DIR/epicpanel-agent run
Environment=EPICPANEL_CONTROL_PLANE_URL=$URL
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
  # Ensure base directories exist with world-traversal permissions for web servers (0755).
  mkdir -p /srv/epicpanel/websites /srv/epicpanel/releases
  chmod 0755 /srv /srv/epicpanel /srv/epicpanel/websites /srv/epicpanel/releases

  systemctl daemon-reload
  systemctl enable --now epicpanel-agent

  sleep 3
  if systemctl is-active --quiet epicpanel-agent; then
    log "Node agent is running and streaming to $URL"
    log "Open the panel -> Servers: this node shows ONLINE with live metrics."
  else
    fail "agent did not start — check: journalctl -u epicpanel-agent"
  fi
}

# ---------------------------------------------------------------------------
# dispatch
# ---------------------------------------------------------------------------
CMD="${1:-install}"
case "$CMD" in
  install|update) shift 2>/dev/null || true; do_install "$@" ;;
  agent)          shift; do_agent "$@" ;;
  uninstall)      shift; do_uninstall "$@" ;;
  rollback)       do_rollback ;;
  -h|--help|help) usage ;;
  *) warn "unknown command: $CMD"; usage; exit 1 ;;
esac

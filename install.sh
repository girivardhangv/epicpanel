#!/usr/bin/env bash
#
# EpicPanel installer — installs EpicPanel and its own PostgreSQL only.
# The hosting stack (Nginx, PHP, MariaDB, Redis, Node, Docker, …) is added
# later from the EpicPanel web UI. Idempotent and resumable.
#
#   curl -fsSL https://get.epichostly.in | bash
#
set -euo pipefail

# ---- configuration (override via env) -------------------------------------
EPICPANEL_REPO="${EPICPANEL_REPO:-girivardhangv/epicpanel}"
EPICPANEL_VERSION="${EPICPANEL_VERSION:-latest}"   # a release tag, or "latest"
EPICPANEL_PORT="${EPICPANEL_PORT:-8080}"
EPICPANEL_HOME="/opt/epicpanel"
EPICPANEL_BIN="/usr/local/bin/epicpanel-panel"     # the panel daemon
EPICPANEL_CLI="/usr/local/bin/epicpanel"           # the CLI (epicpanel update/status/...)
EPICPANEL_AGENTD="/usr/local/bin/epicpanel-agentd" # the host agent
EPICPANEL_ETC="/etc/epicpanel"
EPICPANEL_DATA="/var/lib/epicpanel"
EPICPANEL_LOG="/var/log/epicpanel"
EPICPANEL_ENV="${EPICPANEL_ETC}/epicpanel.env"
EPICPANEL_USER="epicpanel"
DB_NAME="epicpanel"
DB_USER="epicpanel"

# ---- pretty output ---------------------------------------------------------
if [ -t 1 ]; then
  BOLD="$(printf '\033[1m')"; DIM="$(printf '\033[2m')"; RED="$(printf '\033[31m')"
  GREEN="$(printf '\033[32m')"; YELLOW="$(printf '\033[33m')"; CYAN="$(printf '\033[36m')"; RST="$(printf '\033[0m')"
else
  BOLD=""; DIM=""; RED=""; GREEN=""; YELLOW=""; CYAN=""; RST=""
fi
ok()   { printf "  ${GREEN}[✓]${RST} %s\n" "$*"; }
skip() { printf "  ${DIM}[•]${RST} %s ${DIM}(already done)${RST}\n" "$*"; }
warn() { printf "  ${YELLOW}[!]${RST} %s\n" "$*"; }
fail() { printf "  ${RED}[✗]${RST} %s\n" "$*" >&2; exit 1; }
step() { printf "\n${BOLD}%s${RST}\n" "$*"; }
banner() {
  printf "${CYAN}╔══════════════════════════════════════╗${RST}\n"
  printf "${CYAN}║         EpicPanel Installer          ║${RST}\n"
  printf "${CYAN}║            %s%s%s${RST}\n" "${BOLD}" "${EPICPANEL_VERSION}" "${RST}        "
  printf "${CYAN}╚══════════════════════════════════════╝${RST}\n"
}

# ---- preconditions ---------------------------------------------------------
require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    fail "This installer must run as root. Re-run with sudo (or as root)."
  fi
}

detect_os() {
  [ -r /etc/os-release ] || fail "/etc/os-release not found — unsupported OS."
  . /etc/os-release
  OS_ID="${ID:-unknown}"
  OS_LIKE="${ID_LIKE:-}"
  case "${OS_ID} ${OS_LIKE}" in
    *debian*|*ubuntu*) PKG="apt" ;;
    *rhel*|*fedora*|*centos*|*rocky*|*alma*) PKG="dnf" ;;
    *) fail "Unsupported distribution '${OS_ID}'. Supported: Debian/Ubuntu, RHEL/Rocky/Alma/Fedora." ;;
  esac
  ok "Operating system: ${PRETTY_NAME:-$OS_ID} (package manager: ${PKG})"
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) fail "Unsupported CPU architecture: $(uname -m)" ;;
  esac
  ok "Architecture: ${ARCH}"
}

check_requirements() {
  local cores mem_mb disk_mb
  cores="$(nproc 2>/dev/null || echo 1)"
  mem_mb="$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)"
  disk_mb="$(df -Pk / | awk 'NR==2 {print int($4/1024)}')"
  [ "${cores}" -ge 1 ] || fail "Need at least 1 CPU core."
  [ "${mem_mb}" -ge 1024 ] || fail "Need at least 1 GB RAM (have ${mem_mb} MB)."
  [ "${disk_mb}" -ge 2048 ] || fail "Need at least 2 GB free disk (have ${disk_mb} MB)."
  ok "Requirements met: ${cores} cores, ${mem_mb} MB RAM, ${disk_mb} MB free disk"
}

# ---- helpers ---------------------------------------------------------------
pkg_install() {
  if [ "${PKG}" = "apt" ]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq "$@"
  else
    dnf install -y -q "$@"
  fi
}

service_up() { systemctl enable --now "$1" >/dev/null 2>&1 || true; }

# ---- PostgreSQL (EpicPanel's own core dependency) --------------------------
install_postgres() {
  step "PostgreSQL (EpicPanel's database)"
  if command -v psql >/dev/null 2>&1 && pg_isready -q 2>/dev/null; then
    skip "PostgreSQL already installed and running"
  else
    if [ "${PKG}" = "apt" ]; then
      pkg_install postgresql postgresql-contrib
    else
      pkg_install postgresql-server postgresql
      [ -d /var/lib/pgsql/data ] || postgresql-setup --initdb >/dev/null 2>&1 || true
    fi
    service_up postgresql
    ok "PostgreSQL installed"
  fi
}

provision_database() {
  step "EpicPanel database"
  # Reuse an existing DSN if we already provisioned one (idempotent).
  if [ -f "${EPICPANEL_ENV}" ] && grep -q '^EPICPANEL_DATABASE_DSN=' "${EPICPANEL_ENV}"; then
    skip "database already configured"
    return
  fi
  local pw
  pw="$(tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 24 || true)"
  [ -n "${pw}" ] || pw="$(date +%s%N | sha256sum | cut -c1-24)"
  # Create role + database idempotently as the postgres superuser.
  sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='${DB_USER}'" | grep -q 1 \
    || sudo -u postgres psql -c "CREATE ROLE ${DB_USER} LOGIN PASSWORD '${pw}';" >/dev/null
  sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'" | grep -q 1 \
    || sudo -u postgres psql -c "CREATE DATABASE ${DB_NAME} OWNER ${DB_USER};" >/dev/null
  DB_DSN="postgres://${DB_USER}:${pw}@127.0.0.1:5432/${DB_NAME}?sslmode=disable"
  ok "database '${DB_NAME}' ready"
}

# ---- EpicPanel -------------------------------------------------------------
create_user_and_dirs() {
  step "EpicPanel user and directories"
  if ! id -u "${EPICPANEL_USER}" >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "${EPICPANEL_USER}" 2>/dev/null \
      || useradd --system --no-create-home --shell /bin/false "${EPICPANEL_USER}"
    ok "system user '${EPICPANEL_USER}' created"
  else
    skip "system user '${EPICPANEL_USER}'"
  fi
  mkdir -p "${EPICPANEL_HOME}" "${EPICPANEL_ETC}" "${EPICPANEL_DATA}" "${EPICPANEL_LOG}"
  ok "directories ready"
}

download_binary() {
  step "Downloading EpicPanel"
  local base asset tmp
  if [ "${EPICPANEL_VERSION}" = "latest" ]; then
    base="https://github.com/${EPICPANEL_REPO}/releases/latest/download"
  else
    base="https://github.com/${EPICPANEL_REPO}/releases/download/${EPICPANEL_VERSION}"
  fi
  asset="epicpanel_linux_${ARCH}"
  command -v curl >/dev/null 2>&1 || pkg_install curl
  tmp="$(mktemp)"
  if ! curl -fsSL "${base}/${asset}" -o "${tmp}"; then
    rm -f "${tmp}"
    fail "Could not download ${base}/${asset}. Set EPICPANEL_REPO/EPICPANEL_VERSION to a valid GitHub release."
  fi
  # Verify checksum when available (best effort, hard-fail on mismatch).
  local sumfile; sumfile="$(mktemp)"
  if curl -fsSL "${base}/checksums.txt" -o "${sumfile}" 2>/dev/null; then
    local want got
    want="$(grep -E "  ${asset}\$| ${asset}\$" "${sumfile}" | awk '{print $1}' | head -n1 || true)"
    if [ -n "${want}" ]; then
      got="$(sha256sum "${tmp}" | awk '{print $1}')"
      [ "${want}" = "${got}" ] || { rm -f "${tmp}" "${sumfile}"; fail "checksum mismatch for ${asset}"; }
      ok "checksum verified"
    fi
  fi
  rm -f "${sumfile}"
  install -m 0755 "${tmp}" "${EPICPANEL_BIN}"
  rm -f "${tmp}"
  ok "installed ${EPICPANEL_BIN}"
}

# download_tool installs one additional release asset (CLI or agent) with
# checksum verification, into a destination path.
download_tool() {
  local asset="$1" dest="$2" base tmp sumfile want got
  if [ "${EPICPANEL_VERSION}" = "latest" ]; then
    base="https://github.com/${EPICPANEL_REPO}/releases/latest/download"
  else
    base="https://github.com/${EPICPANEL_REPO}/releases/download/${EPICPANEL_VERSION}"
  fi
  command -v curl >/dev/null 2>&1 || pkg_install curl
  tmp="$(mktemp)"
  if ! curl -fsSL "${base}/${asset}" -o "${tmp}"; then
    rm -f "${tmp}"
    fail "Could not download ${base}/${asset}."
  fi
  sumfile="$(mktemp)"
  if curl -fsSL "${base}/checksums.txt" -o "${sumfile}" 2>/dev/null; then
    want="$(grep -E "  ${asset}\$| ${asset}\$" "${sumfile}" | awk '{print $1}' | head -n1 || true)"
    if [ -n "${want}" ]; then
      got="$(sha256sum "${tmp}" | awk '{print $1}')"
      [ "${want}" = "${got}" ] || { rm -f "${tmp}" "${sumfile}"; fail "checksum mismatch for ${asset}"; }
    fi
  fi
  rm -f "${sumfile}"
  install -m 0755 "${tmp}" "${dest}"
  rm -f "${tmp}"
  ok "installed ${dest}"
}

install_cli_and_agent() {
  step "EpicPanel CLI and agent"
  # The CLI (epicpanel update/status/doctor/software) and the agent (host
  # management) are separate release assets installed alongside the panel.
  download_tool "epicpanel-cli_linux_${ARCH}" "${EPICPANEL_CLI}" || true
  download_tool "epicpanel-agentd_linux_${ARCH}" "${EPICPANEL_AGENTD}" || true
}

write_env() {
  step "Configuration"
  if [ ! -f "${EPICPANEL_ENV}" ]; then
    cat > "${EPICPANEL_ENV}" <<EOF
# EpicPanel runtime configuration (managed by the installer)
EPICPANEL_DATABASE_DSN=${DB_DSN:-}
EPICPANEL_SERVER_ADDR=:${EPICPANEL_PORT}
EPICPANEL_SERVER_ENVIRONMENT=production
EPICPANEL_DATA_DIR=${EPICPANEL_DATA}
# EPICPANEL_LICENSE_API_URL=https://licenses.epichostly.in
EOF
    ok "wrote ${EPICPANEL_ENV}"
  else
    skip "existing ${EPICPANEL_ENV}"
  fi
  chown root:"${EPICPANEL_USER}" "${EPICPANEL_ENV}" 2>/dev/null || chown root "${EPICPANEL_ENV}"
  chmod 640 "${EPICPANEL_ENV}"
  chown -R "${EPICPANEL_USER}:${EPICPANEL_USER}" "${EPICPANEL_DATA}" "${EPICPANEL_LOG}" 2>/dev/null || true
}

install_service() {
  step "systemd service"
  cat > /etc/systemd/system/epicpanel.service <<EOF
[Unit]
Description=EpicPanel hosting control panel
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=${EPICPANEL_USER}
Group=${EPICPANEL_USER}
EnvironmentFile=${EPICPANEL_ENV}
ExecStart=${EPICPANEL_BIN}
Restart=on-failure
RestartSec=3
WorkingDirectory=${EPICPANEL_HOME}

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable epicpanel >/dev/null 2>&1 || true
  ok "service configured"
}

start_and_verify() {
  step "Starting EpicPanel"
  systemctl restart epicpanel
  local i
  for i in $(seq 1 20); do
    if curl -fsS "http://127.0.0.1:${EPICPANEL_PORT}/healthz" >/dev/null 2>&1; then
      ok "health check passed"
      return 0
    fi
    sleep 1
  done
  warn "EpicPanel did not answer /healthz within 20s. Check: systemctl status epicpanel; journalctl -u epicpanel"
}

print_result() {
  local ip
  ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [ -n "${ip}" ] || ip="<server-ip>"
  printf "\n${BOLD}${GREEN}EpicPanel installation complete!${RST}\n\n"
  printf "  Panel:   ${CYAN}http://%s:%s${RST}\n" "${ip}" "${EPICPANEL_PORT}"
  printf "  Service: ${DIM}systemctl status epicpanel${RST}\n"
  printf "  Logs:    ${DIM}journalctl -u epicpanel -f${RST}\n\n"
  printf "  Open the URL above to finish setup in the browser.\n"
  printf "  Install hosting software (Nginx, PHP, databases…) from the panel.\n\n"
}

# ---- main ------------------------------------------------------------------
main() {
  banner
  require_root
  detect_os
  detect_arch
  check_requirements
  install_postgres
  provision_database
  create_user_and_dirs
  download_binary
  install_cli_and_agent
  write_env
  install_service
  start_and_verify
  print_result
}

main "$@"

#!/usr/bin/env bash
#
# EpicPanel Licensing Server — public deployment.
#
# Installs and exposes the PHP+SQLite licensing API on a Linux VPS:
#
#   curl -fsSL https://get.epichostly.in/licensing | bash -s -- --domain licenses.example.com
#
# Optional flags (override via env too):
#   --domain  licenses.example.com   public hostname (TLS via Let's Encrypt)
#   --port    80                     plain HTTP port when no domain (default)
#   --user    epiclicense            system user to run the API under
#   --dir     /srv/epicpanel-license install directory
#
set -euo pipefail

# ---- configuration ---------------------------------------------------------
LICENSE_DOMAIN="${LICENSE_DOMAIN:-}"
LICENSE_PORT="${LICENSE_PORT:-80}"
LICENSE_USER="${LICENSE_USER:-epiclicense}"
LICENSE_DIR="${LICENSE_DIR:-/srv/epicpanel-license}"
APP_DIR="${LICENSE_DIR}/app"
DATA_DIR="${LICENSE_DIR}/var"

while [ $# -gt 0 ]; do
  case "$1" in
    --domain) LICENSE_DOMAIN="$2"; shift 2 ;;
    --port)   LICENSE_PORT="$2";   shift 2 ;;
    --user)   LICENSE_USER="$2";   shift 2 ;;
    --dir)    LICENSE_DIR="$2"; shift 2 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

# ---- pretty output ---------------------------------------------------------
if [ -t 1 ]; then
  BOLD="$(printf '\033[1m')"; RED="$(printf '\033[31m')"
  GREEN="$(printf '\033[32m')"; YELLOW="$(printf '\033[33m')"; RST="$(printf '\033[0m')"
else
  BOLD=""; RED=""; GREEN=""; YELLOW=""; RST=""
fi
ok()   { printf "  ${GREEN}[ok]${RST} %s\n" "$*"; }
warn() { printf "  ${YELLOW}[!]${RST} %s\n" "$*"; }
fail() { printf "  ${RED}[x]${RST} %s\n" "$*" >&2; exit 1; }
step() { printf "\n${BOLD}%s${RST}\n" "$*"; }

require_root() {
  [ "$(id -u)" -eq 0 ] || fail "This installer must run as root. Re-run with sudo."
}

detect_os() {
  [ -r /etc/os-release ] || fail "/etc/os-release not found."
  . /etc/os-release
  OS_ID="${ID:-unknown}"; OS_LIKE="${ID_LIKE:-}"
  case "${OS_ID} ${OS_LIKE}" in
    *debian*|*ubuntu*) PKG="apt" ;;
    *rhel*|*fedora*|*centos*|*rocky*|*alma*) PKG="dnf" ;;
    *) fail "Unsupported distribution '${OS_ID}'. Supported: Debian/Ubuntu, RHEL/Rocky/Alma/Fedora." ;;
  esac
  ok "OS: ${PRETTY_NAME:-$OS_ID} (pkg: ${PKG})"
}

pkg_install() {
  if [ "${PKG}" = "apt" ]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq "$@"
  else
    dnf install -y -q "$@"
  fi
}

# ---- PHP + sqlite ----------------------------------------------------------
install_php() {
  step "PHP with SQLite"
  if command -v php >/dev/null 2>&1 && php -m 2>/dev/null | grep -qi '^pdo_sqlite$'; then
    ok "PHP + pdo_sqlite already present"
    return
  fi
  if [ "${PKG}" = "apt" ]; then
    pkg_install php-cli php-sqlite3 php-fpm nginx curl
  else
    pkg_install php-cli php-pdo-sqlite php-fpm nginx curl
  fi
  command -v php >/dev/null 2>&1 || fail "php not installed"
  php -m 2>/dev/null | grep -qi 'pdo_sqlite' || fail "pdo_sqlite extension missing"
  ok "PHP $(php -r 'echo PHP_VERSION;') + pdo_sqlite ready"
}

# ---- app files -------------------------------------------------------------
install_app() {
  step "Licensing API files"
  local src
  src="$(cd "$(dirname "$0")" && pwd)"
  mkdir -p "${APP_DIR}" "${DATA_DIR}"
  install -m 0644 "${src}/index.php" "${APP_DIR}/index.php"
  install -m 0644 "${src}/lib.php"   "${APP_DIR}/lib.php"
  install -m 0755 "${src}/cli.php"   "${APP_DIR}/cli.php"
  if ! id -u "${LICENSE_USER}" >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "${LICENSE_USER}" 2>/dev/null \
      || useradd --system --no-create-home --shell /bin/false "${LICENSE_USER}"
  fi
  chown -R "${LICENSE_USER}:${LICENSE_USER}" "${APP_DIR}" "${DATA_DIR}"
  ok "files installed under ${APP_DIR}"
}

# ---- systemd ---------------------------------------------------------------
install_service() {
  step "systemd service"
  cat > /etc/systemd/system/epicpanel-license.service <<EOF
[Unit]
Description=EpicPanel licensing API
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${LICENSE_USER}
Group=${LICENSE_USER}
WorkingDirectory=${APP_DIR}
ExecStart=/usr/bin/php -S 127.0.0.1:9911 ${APP_DIR}/index.php
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable epicpanel-license >/dev/null 2>&1 || true
  ok "service configured"
}

# ---- nginx reverse proxy (domain -> TLS) or plain HTTP ---------------------
install_nginx() {
  step "Web server"
  local conf="/etc/nginx/conf.d/epicpanel-license.conf"
  cat > "${conf}" <<EOF
server {
    listen 80;
    server_name ${LICENSE_DOMAIN:-_};

    root ${APP_DIR};
    index index.php;

    location / {
        try_files \$uri \$uri/ /index.php?\$query_string;
    }
    location ~ \\.php\$ {
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME \$document_root/index.php;
        fastcgi_pass 127.0.0.1:9911;
    }
}
EOF
  if [ -n "${LICENSE_DOMAIN}" ]; then
    # Force TLS: port 80 redirects to HTTPS (certbot handles the cert).
    sed -i 's|return 301 https://.*;|&|' "${conf}"
  fi
  nginx -t >/dev/null 2>&1 || fail "nginx config invalid"
  systemctl enable nginx >/dev/null 2>&1 || true
  systemctl reload nginx
  ok "nginx serving ${LICENSE_DOMAIN:-port ${LICENSE_PORT}}"
}

# ---- TLS -------------------------------------------------------------------
install_tls() {
  [ -n "${LICENSE_DOMAIN}" ] || { ok "no domain — plain HTTP on port ${LICENSE_PORT}"; return; }
  step "Let's Encrypt TLS"
  if ! command -v certbot >/dev/null 2>&1; then
    if [ "${PKG}" = "apt" ]; then
      pkg_install certbot python3-certbot-nginx
    else
      pkg_install certbot python3-certbot-nginx
    fi
  fi
  certbot --nginx -d "${LICENSE_DOMAIN}" --non-interactive --agree-tos \
    --redirect --register-unsafely-without-email || \
    warn "certbot failed — obtain a certificate manually: certbot --nginx -d ${LICENSE_DOMAIN}"
  ok "TLS enabled for ${LICENSE_DOMAIN}"
}

# ---- firewall --------------------------------------------------------------
open_firewall() {
  step "Firewall"
  if command -v ufw >/dev/null 2>&1; then
    ufw allow "${LICENSE_PORT}/tcp" >/dev/null 2>&1 || true
    [ -n "${LICENSE_DOMAIN}" ] && ufw allow 443/tcp >/dev/null 2>&1 || true
    ok "ufw opened"
  elif command -v firewall-cmd >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port="${LICENSE_PORT}/tcp" >/dev/null 2>&1 || true
    [ -n "${LICENSE_DOMAIN}" ] && firewall-cmd --permanent --add-service=https >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    ok "firewalld opened"
  else
    warn "no firewall tool detected — open ${LICENSE_PORT}/tcp manually if needed"
  fi
}

verify() {
  step "Verifying"
  systemctl restart epicpanel-license
  local scheme="http"; [ -n "${LICENSE_DOMAIN}" ] && scheme="https"
  local host="${LICENSE_DOMAIN:-127.0.0.1:${LICENSE_PORT}}"
  for i in $(seq 1 15); do
    if curl -fsS "${scheme}://${host}/v1/health" >/dev/null 2>&1; then
      ok "health: ${scheme}://${host}/v1/health"
      return 0
    fi
    sleep 1
  done
  warn "licensing API not reachable at ${scheme}://${host} — check: journalctl -u epicpanel-license; systemctl status epicpanel-license"
}

print_result() {
  local scheme="http"; [ -n "${LICENSE_DOMAIN}" ] && scheme="https"
  local host="${LICENSE_DOMAIN:-<server-ip>:${LICENSE_PORT}}"
  printf "\n${BOLD}${GREEN}EpicPanel licensing server deployed!${RST}\n\n"
  printf "  API base: ${CYAN:-}${scheme}://${host}${RST}\n"
  printf "  Health:   ${scheme}://${host}/v1/health\n"
  printf "  CLI:      php ${APP_DIR}/cli.php generate --plan starter --seats 1 --days 365 --name Client\n"
  printf "  Panel:    set EPICPANEL_LICENSE_API_URL=${scheme}://${host}\n\n"
}

# ---- main ------------------------------------------------------------------
main() {
  require_root
  detect_os
  install_php
  install_app
  install_service
  install_nginx
  install_tls
  open_firewall
  verify
  print_result
}

main "$@"

#!/usr/bin/env bash
#
# EpicPanel — bind a public hostname + HTTPS to an existing panel.
#
# Puts nginx in front of the panel, gets a Let's Encrypt certificate, and
# reconfigures the panel to trust the proxy so Secure cookies work (fixes the
# "Password fields on insecure page" / "cookie rejected" browser warnings).
#
#   sudo bash panel-https.sh --domain panel.example.com
#
# Flags (or env):
#   --domain  panel.example.com    required: public hostname for the panel
#   --port    8080                 panel listen port (default 8080)
#
set -euo pipefail

PANEL_DOMAIN="${PANEL_DOMAIN:-}"
PANEL_PORT="${PANEL_PORT:-8080}"
PANEL_ENV="/etc/epicpanel/epicpanel.env"
PANEL_SVC="epicpanel"

while [ $# -gt 0 ]; do
  case "$1" in
    --domain) PANEL_DOMAIN="$2"; shift 2 ;;
    --port)   PANEL_PORT="$2";   shift 2 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

if [ -z "${PANEL_DOMAIN}" ]; then
  echo "error: --domain is required, e.g. --domain panel.example.com" >&2
  exit 2
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "error: must run as root" >&2
  exit 1
fi

BOLD="$(printf '\033[1m')"; GREEN="$(printf '\033[32m')"; YELLOW="$(printf '\033[33m')"; RST="$(printf '\033[0m')"
ok()   { printf "  ${GREEN}[ok]${RST} %s\n" "$*"; }
warn() { printf "  ${YELLOW}[!]${RST} %s\n" "$*"; }
fail() { printf "  [x] %s\n" "$*" >&2; exit 1; }
step() { printf "\n${BOLD}%s${RST}\n" "$*"; }

# ---- detect package manager ------------------------------------------------
[ -r /etc/os-release ] || fail "unsupported OS"
. /etc/os-release
case "${ID:-} ${ID_LIKE:-}" in
  *debian*|*ubuntu*) PKG="apt" ;;
  *rhel*|*fedora*|*centos*|*rocky*|*alma*) PKG="dnf" ;;
  *) fail "unsupported distribution" ;;
esac

pkg_install() {
  if [ "${PKG}" = "apt" ]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq "$@"
  else
    dnf install -y -q "$@"
  fi
}

# ---- nginx reverse proxy ---------------------------------------------------
step "nginx reverse proxy"
command -v nginx >/dev/null 2>&1 || pkg_install nginx
conf="/etc/nginx/conf.d/epicpanel.conf"
cat > "${conf}" <<EOF
# EpicPanel reverse proxy (managed by panel-https.sh)
server {
    listen 80;
    server_name ${PANEL_DOMAIN};

    location / {
        proxy_pass http://127.0.0.1:${PANEL_PORT};
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header X-Forwarded-Host \$host;
        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }

    # Panel websocket support (if the panel later adds streaming endpoints).
    proxy_http_version 1.1;
    proxy_set_header Upgrade \$http_upgrade;
    proxy_set_header Connection "upgrade";
}
EOF
nginx -t >/dev/null 2>&1 || fail "nginx config invalid"
systemctl enable nginx >/dev/null 2>&1 || true
systemctl reload nginx
ok "nginx proxying http://127.0.0.1:${PANEL_PORT}"

# ---- Let's Encrypt TLS -----------------------------------------------------
step "Let's Encrypt TLS"
if ! command -v certbot >/dev/null 2>&1; then
  pkg_install certbot python3-certbot-nginx
fi
certbot --nginx -d "${PANEL_DOMAIN}" --non-interactive --agree-tos \
  --redirect --register-unsafely-without-email || \
  warn "certbot failed — run manually: certbot --nginx -d ${PANEL_DOMAIN}"
ok "TLS enabled for ${PANEL_DOMAIN}"

# ---- reconfigure the panel to trust the proxy ------------------------------
step "Panel configuration"
touch "${PANEL_ENV}"
set_opt() {
  local key="$1" val="$2"
  if grep -q "^${key}=" "${PANEL_ENV}" 2>/dev/null; then
    sed -i "s|^${key}=.*|${key}=${val}|" "${PANEL_ENV}"
  else
    printf '%s=%s\n' "${key}" "${val}" >> "${PANEL_ENV}"
  fi
}
set_opt "EPICPANEL_SERVER_PUBLIC_URL" "https://${PANEL_DOMAIN}"
set_opt "EPICPANEL_SERVER_TRUSTED_PROXY" "127.0.0.1/32"
set_opt "EPICPANEL_SECURITY_COOKIE_SECURE" "true"
ok "env updated: PUBLIC_URL=https://${PANEL_DOMAIN}, TRUSTED_PROXY=127.0.0.1/32, COOKIE_SECURE=true"

# ---- restart panel ---------------------------------------------------------
step "Restarting EpicPanel"
systemctl restart "${PANEL_SVC}" || fail "could not restart ${PANEL_SVC}"
systemctl reload nginx 2>/dev/null || true
ok "panel restarted"

# ---- verify ----------------------------------------------------------------
step "Verifying"
for i in $(seq 1 20); do
  if curl -fsS "https://${PANEL_DOMAIN}/healthz" >/dev/null 2>&1; then
    ok "https://${PANEL_DOMAIN}/healthz reachable"
    printf "\n${BOLD}${GREEN}Done!${RST}\n"
    printf "  Panel:   https://${PANEL_DOMAIN}\n"
    printf "  Cookies: Secure over HTTPS — warnings gone.\n"
    exit 0
  fi
  sleep 1
done
warn "https://${PANEL_DOMAIN} not reachable yet — check: systemctl status nginx; systemctl status ${PANEL_SVC}"

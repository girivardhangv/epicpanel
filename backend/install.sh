#!/usr/bin/env bash
# =============================================================================
# EpicPanel — Production Installer
# =============================================================================
# Installs ONLY the control panel (Go binary + frontend + PostgreSQL).
# Does NOT install hosting software (nginx, PHP, Node, Python, Go, MySQL).
# Hosting software is installed by the EpicPanel Agent on each managed server.
#
# Usage:
#   sudo bash install.sh [--hostname panel.example.com] [--port 8080] [--skip-build]
#
# Requirements:
#   - Linux (any distro with systemd)
#   - root access
#   - Go 1.22+ (or --skip-build to download a pre-built binary)
#   - PostgreSQL 14+ (installed automatically if missing)
# =============================================================================

set -euo pipefail

# --- Defaults ---
PANEL_HOSTNAME=""
PANEL_PORT="8080"
PANEL_USER="epicpanel"
PANEL_GROUP="epicpanel"
PANEL_HOME="/opt/epicpanel"
PANEL_DB="epicpanel"
PANEL_DB_USER="epicpanel"
PANEL_DB_PASS=""
INSTALL_DIR="/usr/local/bin"
SKIP_BUILD=false
REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# --- Colors ---
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'

info()  { echo -e "${BLUE}[INFO]${NC} $1"; }
ok()    { echo -e "${GREEN}[OK]${NC} $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
err()   { echo -e "${RED}[ERROR]${NC} $1"; exit 1; }

# --- Parse args ---
while [[ $# -gt 0 ]]; do
  case $1 in
    --hostname)   PANEL_HOSTNAME="$2"; shift 2 ;;
    --port)       PANEL_PORT="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=true; shift ;;
    *)            warn "Unknown option: $1"; shift ;;
  esac
done

# --- Check root ---
[[ $EUID -ne 0 ]] && err "This script must be run as root (sudo bash install.sh)"

# --- Detect distro ---
detect_distro() {
  if [[ -f /etc/os-release ]]; then
    . /etc/os-release
    echo "$ID"
  else
    echo "unknown"
  fi
}
DISTRO=$(detect_distro)
info "Detected distro: $DISTRO"

# --- Ask hostname if not provided ---
if [[ -z "$PANEL_HOSTNAME" ]]; then
  read -rp "Panel hostname (e.g. panel.example.com): " PANEL_HOSTNAME
  [[ -z "$PANEL_HOSTNAME" ]] && PANEL_HOSTNAME=$(hostname -f 2>/dev/null || echo "localhost")
fi
info "Panel hostname: $PANEL_HOSTNAME"

# --- Generate DB password ---
PANEL_DB_PASS=$(openssl rand -hex 16 2>/dev/null || head -c 16 /dev/urandom | xxd -p | head -1)

# =============================================================================
# 1. System packages (panel dependencies only — no hosting software)
# =============================================================================
info "Installing panel dependencies..."
case "$DISTRO" in
  ubuntu|debian|mint|pop)
    apt-get update -y 2>/dev/null || true
    apt-get install -y --no-install-recommends \
      postgresql postgresql-contrib openssl curl 2>/dev/null || true
    ;;
  fedora|rhel|rocky|almalinux|centos)
    dnf install -y postgresql-server postgresql-contrib openssl curl 2>/dev/null || true
    postgresql-setup --initdb 2>/dev/null || true
    ;;
  arch|manjaro)
    pacman -Sy --noconfirm --needed postgresql openssl curl 2>/dev/null || true
    ;;
  opensuse*|suse)
    zypper --non-interactive install postgresql-server postgresql-contrib openssl curl 2>/dev/null || true
    ;;
  alpine)
    apk add --no-cache postgresql postgresql-contrib openssl curl 2>/dev/null || true
    ;;
  *)
    warn "Unknown distro '$DISTRO' — install PostgreSQL manually and re-run"
    ;;
esac

# =============================================================================
# 2. PostgreSQL setup
# =============================================================================
info "Configuring PostgreSQL..."
if command -v systemctl &>/dev/null; then
  systemctl enable --now postgresql 2>/dev/null || systemctl enable --now postgresql-16 2>/dev/null || true
  sleep 2
fi

# Create DB user and database
sudo -u postgres psql -c "SELECT 1" &>/dev/null || err "PostgreSQL is not running"
sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='$PANEL_DB_USER'" | grep -q 1 || \
  sudo -u postgres psql -c "CREATE ROLE $PANEL_DB_USER LOGIN PASSWORD '$PANEL_DB_PASS';"
sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='$PANEL_DB'" | grep -q 1 || \
  sudo -u postgres psql -c "CREATE DATABASE $PANEL_DB OWNER $PANEL_DB_USER;"

export EPICPANEL_SECRET_KEY=$(openssl rand -hex 32)
export EPICPANEL_DATABASE_URL="postgres://$PANEL_DB_USER:$PANEL_DB_PASS@localhost:5432/$PANEL_DB?sslmode=disable"
ok "PostgreSQL configured"

# =============================================================================
# 3. Build or download the panel binary
# =============================================================================
info "Building EpicPanel..."
if [[ "$SKIP_BUILD" == "false" ]] && command -v go &>/dev/null; then
  cd "$REPO_DIR/backend"
  go build -o "$INSTALL_DIR/epicpanel-api" ./cmd/api
  go build -o "$INSTALL_DIR/epicpanel-agent" ./cmd/agent
  go build -o "$INSTALL_DIR/epicpanel-shell" ./cmd/agent-shell
  ok "Built from source"
elif [[ -f "$REPO_DIR/backend/epicpanel-api" ]]; then
  cp "$REPO_DIR/backend/epicpanel-api" "$INSTALL_DIR/epicpanel-api"
  cp "$REPO_DIR/backend/epicpanel-agent" "$INSTALL_DIR/epicpanel-agent"
  cp "$REPO_DIR/backend/epicpanel-shell" "$INSTALL_DIR/epicpanel-shell"
  ok "Copied pre-built binaries"
else
  err "Go not found and no pre-built binaries. Install Go 1.22+ or use --skip-build with pre-built binaries."
fi
chmod +x "$INSTALL_DIR/epicpanel-api" "$INSTALL_DIR/epicpanel-agent" "$INSTALL_DIR/epicpanel-shell"

# =============================================================================
# 4. Frontend build
# =============================================================================
info "Building frontend..."
FRONTEND_DIR="$PANEL_HOME/frontend"
if [[ -d "$REPO_DIR/frontend" ]] && command -v npm &>/dev/null; then
  cd "$REPO_DIR/frontend"
  npm install --production=false 2>/dev/null
  VITE_API_URL="" npm run build 2>/dev/null
  mkdir -p "$FRONTEND_DIR"
  cp -r dist/* "$FRONTEND_DIR/"
  ok "Frontend built"
elif [[ -d "$FRONTEND_DIR" ]]; then
  ok "Frontend already present"
else
  warn "Frontend not built (npm not found). The API will serve a JSON-only interface."
fi

# =============================================================================
# 5. Service user + directories
# =============================================================================
id -u "$PANEL_USER" &>/dev/null || useradd --system --no-create-home --shell /usr/sbin/nologin "$PANEL_USER"
mkdir -p "$PANEL_HOME" /etc/epicpanel /run/epicpanel
chown -R "$PANEL_USER:$PANEL_GROUP" "$PANEL_HOME" /run/epicpanel

# =============================================================================
# 6. Environment file
# =============================================================================
cat > /etc/epicpanel/panel.env <<ENVEOF
EPICPANEL_HTTP_ADDR=:$PANEL_PORT
EPICPANEL_DATABASE_URL=$EPICPANEL_DATABASE_URL
EPICPANEL_SECRET_KEY=$EPICPANEL_SECRET_KEY
EPICPANEL_ENV=production
EPICPANEL_PANEL_HOSTNAME=$PANEL_HOSTNAME
EPICPANEL_CORS_ORIGINS=https://$PANEL_HOSTNAME,http://$PANEL_HOSTNAME
ENVEOF
chmod 600 /etc/epicpanel/panel.env
chown "$PANEL_USER:$PANEL_GROUP" /etc/epicpanel/panel.env
ok "Environment configured"

# =============================================================================
# 7. Systemd services
# =============================================================================
info "Creating systemd services..."
cat > /etc/systemd/system/epicpanel-api.service <<UNIT
[Unit]
Description=EpicPanel Control Plane API
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=$PANEL_USER
Group=$PANEL_GROUP
EnvironmentFile=/etc/epicpanel/panel.env
ExecStart=$INSTALL_DIR/epicpanel-api
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT

cat > /etc/systemd/system/epicpanel-agent.service <<UNIT
[Unit]
Description=EpicPanel Server Agent (local hosting)
After=network-online.target epicpanel-api.service

[Service]
Type=simple
EnvironmentFile=/etc/epicpanel/panel.env
ExecStart=$INSTALL_DIR/epicpanel-agent run
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
systemctl enable --now epicpanel-api
sleep 3
systemctl enable --now epicpanel-agent

# =============================================================================
# 8. Enroll local agent
# =============================================================================
info "Enrolling local agent..."
sleep 2

# Create a temporary admin to get a registration token (only if no users exist)
USER_COUNT=$(sudo -u postgres psql -d "$PANEL_DB" -tAc "SELECT count(*) FROM users" 2>/dev/null || echo "0")
if [[ "$USER_COUNT" == "0" ]]; then
  info "No users exist yet — complete setup via the web interface at http://$PANEL_HOSTNAME:$PANEL_PORT"
  info "The setup wizard will create the admin account and configure the hostname."
else
  info "Users exist — enroll the local agent manually or via the API"
fi

# =============================================================================
# 9. Frontend nginx (optional — panel serves API; static frontend can be
#    served by nginx or any static file server)
# =============================================================================
if command -v nginx &>/dev/null && [[ -d "$FRONTEND_DIR" ]]; then
  cat > /etc/nginx/sites-available/epicpanel-ui <<NGINX
server {
    listen 80;
    server_name $PANEL_HOSTNAME;
    root $FRONTEND_DIR;
    index index.html;

    location / {
        try_files \$uri \$uri/ /index.html;
    }

    location /v1/ {
        proxy_pass http://127.0.0.1:$PANEL_PORT;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
NGINX
  ln -sf /etc/nginx/sites-available/epicpanel-ui /etc/nginx/sites-enabled/epicpanel-ui 2>/dev/null || true
  nginx -t 2>/dev/null && systemctl reload nginx 2>/dev/null || true
  ok "Frontend nginx configured"
fi

# =============================================================================
# Done
# =============================================================================
echo ""
echo -e "${GREEN}==========================================${NC}"
echo -e "${GREEN}  EpicPanel installed successfully!       ${NC}"
echo -e "${GREEN}==========================================${NC}"
echo ""
echo "  Panel URL:      http://$PANEL_HOSTNAME:$PANEL_PORT"
echo "  Frontend:       http://$PANEL_HOSTNAME (via nginx)"
echo "  Setup wizard:   http://$PANEL_HOSTNAME:$PANEL_PORT/setup (first boot)"
echo ""
echo "  Config:         /etc/epicpanel/panel.env"
echo "  Services:       systemctl status epicpanel-api epicpanel-agent"
echo ""
echo "  IMPORTANT: The panel binary is the ONLY software installed."
echo "  Hosting software (nginx, PHP, MySQL, etc.) is installed"
echo "  by the EpicPanel Agent on each managed server via the"
echo "  Software page in the admin interface."
echo ""
echo -e "${YELLOW}  Secrets are in /etc/epicpanel/panel.env — back it up!${NC}"
echo ""

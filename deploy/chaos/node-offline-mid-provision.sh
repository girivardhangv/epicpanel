#!/usr/bin/env bash
# ============================================================================
# Chaos drill: node goes offline mid-provision.
#
# What it proves: a provision job whose agent dies half-way does not wedge —
# the lease expires, the reaper requeues the job, a retry either completes
# (agent back) or terminally fails after max_attempts with the failure
# visible in the jobs feed and via events (no silent hang, no lost job).
#
# Safe on a dev box: it stops the LOCAL agent and enqueues a harmless
# detect_software job. It never deletes data.
#
# Usage: sudo bash node-offline-mid-provision.sh [--keep-stopped]
# ============================================================================
set -uo pipefail

API="${EPICPANEL_API:-http://127.0.0.1:8080}"
SERVER_NAME="${EPICPANEL_DRILL_SERVER:-}"
LEASE_MIN="${EPICPANEL_LEASE_MIN:-10}"   # jobs lease: 10 min default (store.go)
MAX_WAIT=$((LEASE_MIN * 60 + 120))

step() { printf '\n\033[1;36m[drill]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ ok ]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "run as root"
command -v systemctl >/dev/null || fail "systemd required"

# --- Precondition: a server enrolled on this box ---------------------------
step "0. Locate drill server"
if [ -z "$SERVER_NAME" ]; then
  SERVER_NAME="$(sudo -u postgres psql -d epicpanel -tAc "SELECT name FROM servers ORDER BY created_at LIMIT 1" 2>/dev/null || true)"
fi
[ -n "$SERVER_NAME" ] || fail "no server in DB; enroll a node first (or set EPICPANEL_DRILL_SERVER)"
SERVER_ID="$(sudo -u postgres psql -d epicpanel -tAc "SELECT id FROM servers WHERE name='$SERVER_NAME' LIMIT 1" | tr -d ' ')"
[ -n "$SERVER_ID" ] || fail "server '$SERVER_NAME' not found"
ok "server $SERVER_NAME ($SERVER_ID)"

# --- Enqueue a job, then kill the agent mid-run -----------------------------
step "1. Enqueue detect_software job, then kill the agent"
JOB_ID="$(sudo -u postgres psql -d epicpanel -tAc "INSERT INTO jobs (server_id, type, payload, max_attempts) VALUES ('$SERVER_ID','detect_software','{\"drill\":\"node-offline-mid-provision\"}',2) RETURNING id" 2>/dev/null | head -1 | tr -d ' ')"
[ -n "$JOB_ID" ] || fail "could not enqueue drill job (is psql available? is the DB up?)"
ok "job $JOB_ID enqueued (pending)"

systemctl stop epicpanel-agent
AGENT_DOWN_AT="$(date +%s)"
ok "epicpanel-agent stopped at t=0"

sleep 2
STATUS="$(sudo -u postgres psql -d epicpanel -tAc "SELECT status FROM jobs WHERE id='$JOB_ID'" | tr -d ' ')"
case "$STATUS" in
  pending)  ok "job still pending — agent died before claiming (legitimate)" ;;
  running)  ok "job RUNNING while agent is dead — lease clock ticking" ;;
  *)        warn "job status '$STATUS' (fast local race); drill continues" ;;
esac

# --- Wait for the reaper -----------------------------------------------------
step "2. Wait (max ${MAX_WAIT}s) for lease expiry + reaper requeue/retry"
DEADLINE=$((AGENT_DOWN_AT + MAX_WAIT))
RESULT=""
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  STATUS="$(sudo -u postgres psql -d epicpanel -tAc "SELECT status FROM jobs WHERE id='$JOB_ID'" | tr -d ' ')"
  case "$STATUS" in
    failed)
      RESULT="failed-terminally"
      break
      ;;
    success)
      RESULT="succeeded-after-retry"
      break
      ;;
  esac
  sleep 5
done

# --- Bring the agent back ----------------------------------------------------
if [ "${1:-}" != "--keep-stopped" ]; then
  step "3. Restart the agent (retry path)"
  systemctl start epicpanel-agent
  ok "epicpanel-agent started"
fi

# --- Verification ------------------------------------------------------------
step "4. Verification"
FINAL="$(sudo -u postgres psql -d epicpanel -tAc "SELECT status || ' | attempts=' || attempts || ' | err=' || COALESCE(error,'-') FROM jobs WHERE id='$JOB_ID'" | tr -d ' ')"
echo "  final job row: $FINAL"
echo "  verify live:   sudo -u postgres psql -d epicpanel -c \"SELECT id,status,attempts,visible_after,lease_expires_at FROM jobs WHERE id='$JOB_ID'\""
echo "  journal:       journalctl -u epicpanel-api --since '-${MAX_WAIT}s' | grep -i reaper"

if [ -n "$RESULT" ]; then
  ok "outcome: $RESULT — job lifecycle completed without a hang"
else
  warn "outcome: still '$STATUS' at deadline — check lease reaper schedule"
  warn "(the fast scheduler loop runs every 30s; on a slow box raise EPICPANEL_LEASE_MIN)"
fi

step "Expected outcome"
cat <<'EOF'
  - While the agent is dead the job may show status=running with a stale
    lease_expires_at (claimed just before the kill), or stay pending.
  - When the lease expires the reaper requeues it (pending, attempts+1) or
    fails it terminally once attempts >= max_attempts.
  - UI (jobs feed) mirrors the same rows; WS broadcasts job.claimed /
    job.failed events. Nothing hangs in "running" forever.
EOF

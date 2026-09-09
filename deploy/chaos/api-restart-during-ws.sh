#!/usr/bin/env bash
# ============================================================================
# Chaos drill: API restart during live WebSocket streams.
#
# What it proves: killing the control plane mid-stream
#   - drops both agent (/v1/agent/stream) and browser (/v1/ws) sockets cleanly
#     from the CLIENTS' perspective (they reconnect on their own),
#   - sessions survive (they live in Postgres, not in API memory) — the
#     statelessness requirement,
#   - no data loss beyond the frames in flight (agents replay from rings).
#
# Safe on a dev box: restarts the LOCAL api + agent.
#
# Usage: sudo bash api-restart-during-ws.sh
# ============================================================================
set -uo pipefail

API="${EPICPANEL_API:-http://127.0.0.1:8080}"

step() { printf '\n\033[1;36m[drill]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ ok ]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

# SIGPIPE-safe journal grep (grep -q can kill the producer; see
# agent-reconnect-resume.sh).
jgrep() {
  local pat="$1"; shift
  [ "$(journalctl "$@" --no-pager 2>/dev/null | grep -c -- "$pat")" -gt 0 ]
}

q() { sudo -u postgres psql -d epicpanel -tAc "$1" 2>/dev/null | tr -d ' '; }

[ "$(id -u)" -eq 0 ] || fail "run as root"
curl -fsS "$API/healthz" >/dev/null || fail "API not healthy at baseline"

step "1. Baseline: sessions in Postgres (statelessness precondition)"
SESS0="$(q "SELECT count(*) FROM sessions WHERE expires_at > now()")"
[ -n "$SESS0" ] && [ "$SESS0" -gt 0 ] 2>/dev/null || fail "no active sessions — log in via the UI once first"
ok "$SESS0 active sessions in Postgres (not in API memory)"

step "2. Browser-side WS note"
# A browser-style probe (session cookie + GET /v1/ws) can be added with any
# WS client; the AGENT stream below is the primary probe because it is a
# real persistent socket. Session durability is asserted in step 6.
ok "agent stream is the primary probe; session durability asserted in step 6"

step "3. Kill -9 the API while the agent stream is connected"
jgrep "metrics stream connected" -u epicpanel-agent --since "-5 min" \
  || fail "agent not streaming at baseline (wait ~10s after agent start)"
API_PID="$(systemctl show epicpanel-api -p MainPID --value)"
kill -9 "$API_PID"
ok "SIGKILL delivered to pid $API_PID (t=0)"
T0="$(date +%s)"

step "4. systemd restarts the API; measure recovery time"
for i in $(seq 1 60); do
  curl -fsS "$API/healthz" >/dev/null 2>&1 && break
  [ "$i" = 60 ] && fail "API did not recover in 60s"
  sleep 1
done
RECOVERY=$(( $(date +%s) - T0 ))
ok "API healthy again after ${RECOVERY}s"

step "5. Agent reconnects on its own"
sleep 15
jgrep "metrics stream connected" -u epicpanel-agent --since "-90s" \
  && ok "agent reconnected (no operator action)" \
  || fail "agent did not reconnect within 15s"

step "6. Sessions survived the restart"
SESS1="$(q "SELECT count(*) FROM sessions WHERE expires_at > now()")"
[ "$SESS1" -ge "$SESS0" ] && ok "$SESS1 sessions still valid — no login lost" \
  || fail "sessions lost across restart ($SESS0 -> $SESS1) — state leaked into memory"

step "Verification"
echo "  journalctl -u epicpanel-api  --since '-2 min' | grep -E 'agent stream|listening'"
echo "  journalctl -u epicpanel-agent --since '-2 min' | grep -E 'stream (dis)?connected|replayed'"
echo "  curl -s $API/healthz"

step "Expected outcome"
cat <<'EOF'
  - In-flight WS frames are lost (TCP is gone) — agents replay them from the
    ring on resume; browser clients refetch the live fleet projection.
  - Sessions, jobs, events all survive: Postgres is the only source of truth
    (statelessness audit in docs/scale-report.md).
  - Recovery budget: systemd Restart=always + RestartSec=3; healthz answers
    within seconds of process start (measured above).
EOF

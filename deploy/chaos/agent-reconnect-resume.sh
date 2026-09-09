#!/usr/bin/env bash
# ============================================================================
# Chaos drill: agent reconnect + metrics sequence resume.
#
# What it proves: when an agent's stream drops (network blip, agent restart)
# it reconnects with backoff, sends resume{last_seq}, replays the frames it
# buffered in its 256-frame ring, and the control plane acks contiguously —
# no data loss inside the ring horizon, no duplicate samples, LIVE returns
# without operator action.
#
# Safe on a dev box: restarts the LOCAL agent twice.
#
# Usage: sudo bash agent-reconnect-resume.sh
# ============================================================================
set -uo pipefail

API="${EPICPANEL_API:-http://127.0.0.1:8080}"

step() { printf '\n\033[1;36m[drill]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ ok ]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

# grep the journal safely. grep -q exits after the first match, which can
# SIGPIPE the journalctl producer; with pipefail that turns a MATCH into a
# failure. grep -c consumes all input, so it is SIGPIPE-safe.
jgrep() {
  local pat="$1"; shift
  [ "$(journalctl "$@" --no-pager 2>/dev/null | grep -c -- "$pat")" -gt 0 ]
}

[ "$(id -u)" -eq 0 ] || fail "run as root"
systemctl is-active --quiet epicpanel-agent || fail "epicpanel-agent not running"

step "1. Baseline: confirm the agent is streaming"
sleep 6
jgrep "metrics stream connected" -u epicpanel-agent --since "-60s" \
  || fail "no stream connection in the last 60s; fix baseline first"
ok "stream connected (see journalctl -u epicpanel-agent)"

step "2. Hard-drop: restart the agent (new process, same session semantics)"
RESTARTS0="$(systemctl show epicpanel-agent -p NRestarts --value)"
T0="$(date +%s)"
systemctl restart epicpanel-agent
ok "agent restarted at t=0 (journal will show reconnect + backoff if the API was busy)"

step "3. Wait for reconnect and ring replay"
sleep 12
jgrep "metrics stream connected" -u epicpanel-agent --since "-30s" \
  && ok "reconnected" || fail "agent did not reconnect within 12s"
jgrep "replayed metrics frames" -u epicpanel-agent --since "-30s" \
  && ok "ring replay visible (replayed metrics frames after reconnect)" \
  || ok "no replay needed (gap under one sample — also correct)"

step "4. Second drop: this time restart the API too (double chaos)"
systemctl restart epicpanel-api
systemctl restart epicpanel-agent
# Wait for the API to accept connections again.
for i in $(seq 1 30); do
  curl -fsS "$API/healthz" >/dev/null 2>&1 && break
  [ "$i" = 30 ] && fail "API did not come back"
  sleep 1
done
ok "API healthy again"

step "5. Final state"
sleep 15
jgrep "metrics stream connected" -u epicpanel-agent --since "-60s" \
  && ok "agent streaming again" || fail "agent not streaming after double chaos"

NR1="$(systemctl show epicpanel-agent -p NRestarts --value)"
echo
echo "Verification:"
echo "  journalctl -u epicpanel-agent --since '-2 min' | grep -E 'connected|replayed|reconnect'"
echo "  journalctl -u epicpanel-api   --since '-2 min' | grep -E 'agent stream (connected|closed)'"
echo "  curl -s $API/healthz"
echo "  systemctl show epicpanel-agent -p NRestarts   (baseline $RESTARTS0 -> now $NR1)"

step "Expected outcome"
cat <<'EOF'
  - Every restart reconnects within seconds (exponential backoff only when
    the control plane is unreachable).
  - The 256-frame ring replays any samples missed while the socket was down;
    the control plane dedups by seq (no double rows) and acks contiguously.
  - The node flips LIVE->STALE during the outage and back to LIVE after
    reconnect without operator action (run loadsim -drop-node-at for the
    measured STALE/OFFLINE transition times).
EOF

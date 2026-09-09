#!/usr/bin/env bash
# ============================================================================
# Chaos drill: Postgres failover / restart under the control plane.
#
# What it proves:
#   - the API degrades visibly (healthz 503) but does not crash-loop while
#     the DB is unreachable;
#   - the API recovers WITHOUT restart once Postgres is back (pool
#     self-heals); sessions/jobs/events are intact (durable in Postgres);
#   - on a real HA pair, promote + DNS/DSN switch follows the same steps.
#
# Safe on a dev box: it restarts the LOCAL postgres cluster briefly.
# On production HA: replace step 2 with your failover command and skip
# the local pg restart (documented inline).
#
# Usage: sudo bash db-failover.sh
# ============================================================================
set -uo pipefail

API="${EPICPANEL_API:-http://127.0.0.1:8080}"
PG_CLUSTER="$(pg_lsclusters --no-header 2>/dev/null | awk 'NR==1{print $1"/"$2}')"

step() { printf '\n\033[1;36m[drill]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ ok ]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || fail "run as root"
curl -fsS "$API/healthz" >/dev/null || fail "API not healthy at baseline"
command -v pg_lsclusters >/dev/null || fail "pg_lsclusters not found (is postgres installed via apt?)"

step "1. Baseline"
q() { sudo -u postgres psql -d epicpanel -tAc "$1" 2>/dev/null | tr -d ' '; }
JOBS0="$(q "SELECT count(*) FROM jobs")"
SESS0="$(q "SELECT count(*) FROM sessions WHERE expires_at > now()")"
[ -n "$JOBS0" ] || fail "cannot query epicpanel DB via psql as postgres user"
ok "jobs=$JOBS0 sessions=$SESS0 cluster=${PG_CLUSTER:-unknown}"

step "2. Stop Postgres (dev drill). PRODUCTION HA: promote the replica instead."
systemctl stop postgresql.service
T0="$(date +%s)"
ok "postgres stopped at t=0"

step "3. Observe API degradation"
sleep 2
CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$API/healthz" || true)"
case "$CODE" in
  503|000) ok "healthz -> $CODE (degraded visibly, not lying)" ;;
  200)     warn "healthz still 200 — check DB reachability assumptions" ;;
esac
if systemctl is-active --quiet epicpanel-api; then
  ok "epicpanel-api process still UP (no crash-loop while DB is gone)"
else
  warn "epicpanel-api died while DB was down (Restart=always masks it; acceptable but note it)"
fi

step "4. Restore Postgres (dev drill). PRODUCTION HA: this is the promote step."
systemctl start postgresql.service
for i in $(seq 1 30); do
  q "SELECT 1" | grep -q 1 && break
  [ "$i" = 30 ] && fail "postgres did not come back (check: journalctl -u postgresql)"
  sleep 1
done
ok "postgres back after $(( $(date +%s) - T0 ))s"

step "5. API self-heals WITHOUT restart"
sleep 5
CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$API/healthz" || true)"
[ "$CODE" = 200 ] && ok "healthz -> 200 without restarting the API (pool self-heal)" \
  || { warn "healthz -> $CODE; restarting the API (note: check pool retry settings)"; systemctl restart epicpanel-api; sleep 5; }

step "6. Durability spot-check"
JOBS1="$(q "SELECT count(*) FROM jobs")"
SESS1="$(q "SELECT count(*) FROM sessions WHERE expires_at > now()")"
[ "$SESS1" -ge "$SESS0" ] && ok "sessions intact ($SESS0 -> $SESS1)" || fail "sessions lost"
[ "$JOBS1" -ge "$JOBS0" ] && ok "jobs intact ($JOBS0 -> $JOBS1)" || fail "jobs lost"

step "Verification"
echo "  journalctl -u epicpanel-api --since '-3 min' | grep -iE 'pool|postgres|degraded'"
echo "  curl -s $API/healthz ; curl -s $API/metrics | grep jobs_pending"

step "Expected outcome"
cat <<'EOF'
  - While the DB is down: healthz reports 503 (degraded), request handlers
    that need the DB return 5xx, the live metrics ingest keeps draining
    (in-memory live store) and the history writer requeues nothing it
    already flushed.
  - After failover/restart: no operator action needed; the pgx pool
    re-establishes connections on the next query.
  - On a real HA pair the same drill runs with: pg_ctl promote on the
    replica + DSN switch (or a VIP move); steps 3-6 are identical.
EOF

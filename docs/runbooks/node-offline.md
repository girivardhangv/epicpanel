# Runbook: Node Offline

Alert names: `node.offline`, `node.stale` (registry sweep), WS
`server_state{connected:false}` broadcast, freshness state OFFLINE.

## Symptoms

- Alert feed: `node.offline` / node freshness OFFLINE (`/v1/admin/alerts`,
  admin Observability → Nodes).
- `/v1/ws` clients receive `server_state` with `connected:false`.
- Node row shows `connected=false`, last sample age growing.
- Jobs targeting the node stall in `pending` or hold `running` with an
  aging `lease_expires_at`.

## Diagnosis

```bash
ssh <node>
systemctl status epicpanel-agent
journalctl -u epicpanel-agent --since '-30 min' | tail -50
curl -fsS http://127.0.0.1:8080/healthz         # control plane reachable?
journalctl -u epicpanel-api --since '-30 min' | grep "agent stream"
```

- Agent dead (unit failed, OOM-kill, host rebooted): `systemctl is-active`
  fails or restart counter climbing.
- Agent alive but disconnected: network path, control-plane restart loop,
  or token revoked (`server_agent_tokens`).
- Frequent reconnect cycling: `metrics stream connected` /
  `agent stream closed` pairs within seconds in the API journal.

## Remediation

1. Agent stopped/crashed: `systemctl restart epicpanel-agent`; if it
   crash-loops, capture `journalctl -u epicpanel-agent -n 200` before
   touching anything (panics include the frame state).
2. Enrollment broken: mint a fresh registration token in the panel
   (Servers → node → registration token) and re-enroll:
   `epicpanel-agent enroll --url https://panel:8080 --token agtreg_…`.
3. Network: verify the control plane URL/port from the node
   (`curl http://<panel>:8080/healthz`).
4. Host down: fix the host; the queue self-heals — jobs' leases expire,
   the reaper requeues them, attempts continue up to `max_attempts`.

## Verification

```bash
journalctl -u epicpanel-agent --since '-2 min' | grep "metrics stream connected"
curl -s "$API/v1/organizations/$ORG/servers/metrics" | jq '.metrics[] | {server_id, node_state}'
# expect node_state: LIVE, connected: true
psql -d epicpanel -c "SELECT id,status,attempts FROM jobs WHERE server_id='<id>' ORDER BY created_at DESC LIMIT 5"
```

Drill: `deploy/chaos/agent-reconnect-resume.sh` (ring resume proof),
`deploy/chaos/node-offline-mid-provision.sh` (job lease/reaper proof).

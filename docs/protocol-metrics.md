# EpicPanel Metrics Stream Protocol — v1

> Implementation: `backend/internal/agentproto` (shared wire types) ·
> agent sender: `internal/agent/stream.go` · control-plane ingest:
> `internal/servers/stream.go` + `internal/metrics/live.go` ·
> browser fan-out: `internal/events/hub.go` (`BroadcastMetrics`).

## Topology

```
Node Agent (collector, 5s default)
   │  WebSocket GET /v1/agent/stream  (Authorization: Bearer agt_…)
   ▼
Control Plane  — ingest → LiveStore (in-memory) ─┬─ WS /v1/ws → React (push, no polling)
                                                 └─ async Writer → server_metrics (raw 24h)
                                                                 → server_metrics_rollup_5m (30d)
```

The live dashboard NEVER reads the historical database (master doc line 434).
The historical writer NEVER runs inside a request handler or the ingest path.

## Frames (JSON, agent → control plane)

Envelope: `{"type":"hello|metrics|heartbeat|resume|pong","session_id":"…","seq":N,"ts":"RFC3339","data":{…}}`

| Type | When | Data |
|------|------|------|
| `hello` | first frame after connect | `{protocol, agent_version, hostname, os, started_at}` |
| `resume` | reconnect of an existing session | `{last_seq, protocol}` |
| `metrics` | every sample interval | `Sample` (below) |
| `heartbeat` | liveness when no sample is sent | `{collect_ms, degraded}` |
| `pong` | reply to `ping` | — |

Control plane → agent:

| Type | Data |
|------|------|
| `welcome` | `{resume_seq, protocol}` |
| `ack` | `{seq}` — highest contiguous ingested seq |
| `ping` | — (agent replies `pong`; also refreshes read deadline) |

## Sequencing, reconnect, gaps

- `session_id` is generated once per agent process; `seq` increments per
  metrics frame **within** the session.
- The agent buffers the last 256 frames (ring). On reconnect it sends
  `resume{last_seq}`; the control plane answers `welcome{resume_seq}`; the
  agent replays frames with `seq > resume_seq`.
- Ingest dedup: frames with `seq <= last_seq` for the same session are
  dropped; jumps > 1 bump the server's `gaps` counter (surfaced in the API).
- A new `session_id` resets sequence accounting (agent restart).

## Freshness contract (verbatim from the master doc)

```
CPU  82%  LIVE   updated 240ms ago
CPU  12%  STALE  last update 18.4s ago
```

Thresholds (shared constant, `agentproto`):
- **LIVE** — sample age ≤ 15s
- **STALE** — ≤ 120s
- **OFFLINE** — older, or no data ever

The UI recomputes age client-side every second from `collected_at`, so a
pushed frame degrades LIVE→STALE→OFFLINE without new traffic.

## Node connection registry

Derived per node from the ingest connection + last frame age
(`ServerLive.NodeState`):

| State | Condition |
|-------|-----------|
| ONLINE | stream connected, last frame ≤ 30s |
| STALE | last frame ≤ 120s (connection transitional/down) |
| OFFLINE | last frame > 120s or never seen |

Exposed as `node_state` on every metrics frame/snapshot; `server_state`
WS messages announce connect/disconnect transitions.

## Self-throttling (degrade visibly, never silently)

If a collection pass takes longer than the interval, the agent doubles the
interval (cap 30s), sets `degraded: true` + `degraded_reason`, logs a
warning, and recovers by halving the interval when healthy. The UI renders
an amber "Degraded collection" chip. Drops in the control-plane history
queue are counted and logged — the live path never blocks.

## Collection lists (master doc, verbatim mapping)

| Envelope | Fields |
|----------|--------|
| Node (Linux host) | CPU, RAM, Swap, Load average, Disk usage, Disk I/O, Network RX/TX, Process count, Filesystem inode usage, TCP connections, Service status |
| Sites (`sites[]`) | CPU usage, Memory usage (+limit), Disk usage, Bandwidth, Processes, IO |
| Containers (`containers[]`) | CPU, Memory, Memory limit, Network RX/TX, Block I/O, PIDs, Uptime |
| Apps (`apps[]`, kind `minecraft`) | CPU, RAM, Network, Disk, Process status, **Players, TPS, MSPT** (populated Phase 7) |
| Apps (`apps[]`, kind `discord`) | CPU, RAM, Network, Process status, uptime, **restart count** (populated Phase 8) |

CPU% / network / disk-IO are always **two-sample deltas** over the window
between collection passes — never since-boot averages (the Phase 1 root
cause). The agent's first sample establishes baselines (rates 0) and the
UI shows LIVE from the first frame.

## Persistence & retention

- Raw rows: `server_metrics` extended with swap, `network_rx_bps/tx_bps`,
  `read_bps/write_bps`, `tcp_established/total`, `processes`,
  `inodes_total/used`, `degraded` (migration 0022).
- Writer: 8s batches via pgx Batch (single round trip), bounded queue
  (2000), non-blocking drop+count on overflow.
- Retention: raw rows pruned after 24h; `server_metrics_rollup_5m`
  (5-minute buckets, built hourly) kept 30 days.
- History API: `?range=24h` reads raw; `7d`/`30d` read the rollup.

## Phase 7 / 8 contract (no protocol change needed)

Minecraft/Discord workloads appear as `apps[]` entries with
`kind: "minecraft" | "discord"`. The agent currently infers the kind from
the process command line and reports zero `players/tps/mspt`; Phases 7/8
populate them (RCON/query) without touching this protocol.

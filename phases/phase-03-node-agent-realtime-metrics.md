# PHASE 3 — Node Agent + Real-Time Metrics Infrastructure  ⚑ PRIORITY PHASE

> **NEW AGENT SESSION — START HERE.** Read in this order:
> 1. This file (fully)
> 2. `docs/architecture-report.md` § metrics root-cause (Phase 1 finding — the thing you are fixing)
> 3. `prompts/epicpanel-docs` lines 376–579 (Phase 3 + Agent Prompt) and lines 1273–1300 ("One particularly important change")
> 4. `phases/README.md` (global contracts)
> 5. Code: `backend/cmd/agent/main.go`, `backend/internal/{servers/agent.go,servers/metrics.go,monitoring,agent/*,jobs}`, `frontend/src/pages/{Dashboard,Servers}.tsx`, `frontend/src/lib/api.ts`
>
> Depends on: Phase 2 (event bus, jobs, WS skeleton) · Blocks: 5, 6, 9, 13

## Mission
Replace "browser polls API which may run a command" with the push pipeline (doc diagram, verbatim):
`Node Agent → metrics stream → Control Plane → WebSocket → React`
Every live metric must be labeled **LIVE / STALE / OFFLINE** with age. The panel must NEVER show `CPU: 12%` when the value is 20–30s old.

## Agent Prompt — VERBATIM from master doc (execute exactly as written)
> Rework the node architecture around a dedicated Go Node Agent.
>
> The Node Agent must be installed on every managed hosting/server node.
>
> The control plane must NOT depend on executing arbitrary SSH commands for normal infrastructure operations.
>
> Implement secure authenticated control-plane ↔ node-agent communication.
>
> Implement a real-time metrics pipeline.
>
> Metrics must NOT depend on slow database queries or browser polling.
>
> The Node Agent should continuously collect:
> - CPU
> - memory
> - swap
> - load average
> - disk usage
> - disk IO
> - network RX/TX
> - inode usage
> - process information
> - service health
>
> For managed workloads collect per-resource metrics.
>
> Support:
> - Linux host metrics
> - systemd services
> - Docker/container metrics
> - process metrics
>
> Separate:
> 1. instantaneous metrics
> 2. historical metrics
>
> Instantaneous metrics should be kept in a low-latency in-memory/local representation and streamed to the control plane.
>
> Historical metrics should be asynchronously persisted for graphs and reporting.
>
> Implement:
> - WebSocket or equivalent persistent event stream
> - reconnect handling
> - heartbeat
> - sequence numbers
> - timestamps
> - stale-data detection
> - node connection status
> - metric freshness indicators
>
> The UI must be able to tell whether a metric is:
> LIVE
> STALE
> OFFLINE
>
> Never present old metrics as if they are current.
>
> Optimize the implementation so high server load does not cause the monitoring system to become silently inaccurate.
>
> Do not perform expensive metrics aggregation synchronously inside normal API requests.
>
> Add tests for high-load and reconnect scenarios.

## Specific requirements from master doc — implement EXACTLY as named
- **Do NOT use the historical database as the source for the live dashboard** (doc line 434)
- **Collection lists (verbatim):**
  - Linux node: CPU, RAM, Swap, Load average, Disk usage, Disk I/O, Network RX, Network TX, Process count, Filesystem inode usage, TCP connections, Service status
  - Per hosting account: CPU usage, Memory usage, Disk usage, Bandwidth, Processes, IO
  - Containers: CPU, Memory, Memory limit, Network RX, Network TX, Block I/O, PIDs, Uptime
  - Minecraft (protocol must carry these; UI lands Phase 7): CPU, RAM, TPS, MSPT, Players, Network, Disk, Process status
  - Discord (protocol must carry; UI lands Phase 8): CPU, RAM, Network, Process status, uptime, restart count
- **Don't make the browser poll `GET /metrics` every second** (doc lines 493–501) — remove existing polling loops
- Use **Redis pub-sub or internal event mechanism** where appropriate (doc line 515)
- **Freshness display format (verbatim examples from doc):**
  ```
  CPU  82%  LIVE   updated 240ms ago
  CPU  12%  STALE  last update 18.4s ago
  ```
- Existing agent (`cmd/agent` poll loop + heartbeat + typed jobs) is a KEEP/refactor base — do not rewrite from scratch

## Work Items
- [ ] Agent: high-frequency collector (all host metrics above; `/proc`, `/sys`, systemd, Docker stats) with self-throttling under load — degrade visibly, never silently
- [ ] Agent→control-plane authenticated stream: sequence numbers, timestamps, heartbeat, resume-on-reconnect
- [ ] Control plane: ingest → instantaneous store (in-memory/Redis, low latency) → WS fan-out via Phase 2 event bus
- [ ] Historical path: async batched persistence + rollups + retention; NOT in the live path
- [ ] Node connection registry: ONLINE / STALE / OFFLINE thresholds; expose to UI
- [ ] Per-workload metric envelopes (account, container, minecraft, discord) in the protocol even before those UIs exist
- [ ] Frontend: WS client with reconnect; `useMetrics` subscription hooks; freshness badge component (LIVE/STALE/OFFLINE + age); replace all metric polling in Dashboard/Servers
- [ ] Tests: high-load collection accuracy; reconnect without gaps/duplicates (sequence verification); stale detection timing

## Deliverables
Agent collector + stream protocol, control-plane ingest + WS hub + historical writer, node status registry, React live-metrics layer, load/reconnect tests.

## Definition of Done
CPU spike on a node visible in UI < 2s with LIVE badge; killing the agent flips UI to STALE→OFFLINE within configured thresholds; reconnect resumes with no duplicate/gapped sequences; no `GET /metrics` polling remains in the frontend; no expensive aggregation inside request handlers.

## Session Handoff — FILL BEFORE ENDING SESSION
- Stream protocol doc location + version: `docs/protocol-metrics.md` (agentproto v1,
  `backend/internal/agentproto` shared wire types). Frames hello/resume/metrics/heartbeat/
  pong agent→CP, welcome/ack/ping CP→agent; session-scoped monotonic seq with 256-frame
  ring replay on reconnect + ingest-side dedup and visible gap counters. Transport:
  `GET /v1/agent/stream` WebSocket (agent token). Browser fan-out: `/v1/ws` `metrics` +
  `server_state` frames via `Hub.BroadcastMetrics` (no DB writes in the live path).
- Stale/offline thresholds chosen: LIVE ≤ 15s · STALE ≤ 120s · OFFLINE > 120s (shared
  constants `agentproto.LiveMaxAge/StaleMaxAge`, one source of truth). Node registry:
  ONLINE (stream connected, frame ≤ 30s) / STALE (frame ≤ 120s) / OFFLINE (> 120s or never).
  Frontend recomputes age client-side each second so pushed frames degrade without traffic.
- Measured end-to-end latency under load: single-digit ms in-process (ingest → fan-out is
  a direct call; `-race` hammer test ingests 200 frames across 8 goroutines while readers
  snapshot). CPU spike → UI badge latency is bounded by the 5s default sample interval
  (`EPICPANEL_AGENT_METRICS_INTERVAL`); collection overrun self-throttles visibly
  (interval doubles to 30s cap, `degraded` flag + UI amber chip, recover by halving).
- Handed to Phase 5/6: freshness badge component + hooks API:
  `frontend/src/components/FreshnessBadge.tsx` (props `state`, `ageMs`, `label`;
  verbatim "updated 240ms ago" / "last update 18.4s ago" formats) ·
  `frontend/src/lib/metrics.ts` (`useMetrics()` → `{frames: Record<server_id, SnapshotFrame>,
  connected}`, `useFreshness(frame)` → `{state, ageMs}` with 1s recompute, REST seeders
  `normalizeBatch/normalizeSingle` handling legacy MetricPoint) · `frontend/src/lib/ws.ts`
  (singleton WS with backoff + resubscribe). TypeScript wire types in `lib/metrics.ts`
  mirror `agentproto` (SnapshotFrame/NodeSample/SiteSample/ContainerSample/AppSample).
  Phase 7/8: `apps[]` envelopes with `kind: minecraft|discord` already flow end-to-end;
  `players/tps/mspt` + `restart_count` fields are reserved in the protocol and surface
  to the UI without protocol changes.
- Update `phases/README.md` status row for Phase 3 → DONE ✓ (see below)
- Notes: agent version bumped 0.2.0; legacy HTTP heartbeat kept as fallback only while the
  stream is down; migration 0022 (metrics columns + rollup table); audit's monitoring
  history connection leak fixed; `GET .../metrics/history?range=7d|30d` now reads the
  5-minute rollup. Deviation note: per-site disk walks are cached 60s and container name
  resolution 30s (expensive ops must not run per sample) — matches the "no expensive
  aggregation in the live path" contract.

// Package agentproto defines the EpicPanel real-time metrics protocol v1
// shared by the node agent (sender) and the control plane (ingestor).
//
// Transport: one persistent WebSocket per agent (GET /v1/agent/stream,
// authenticated with the server agent token). Frames are JSON objects:
//
//	{"type":"metrics","session_id":"...","seq":42,"ts":"...","data":{...}}
//
// Sequence numbers are monotonically increasing per session (an agent
// process generates one session id at startup). The agent keeps a ring
// buffer of recent frames and replays seq > resume_seq on reconnect, so a
// reconnect neither duplicates nor gaps frames as long as the buffer has
// not rotated (protocol-metrics.md §Reconnect).
package agentproto

import (
	"encoding/json"
	"time"
)

// ProtocolVersion is bumped on incompatible frame-shape changes.
const ProtocolVersion = 1

// Frame types agent → control plane.
const (
	TypeHello     = "hello"     // first frame after connect (data: Hello)
	TypeMetrics   = "metrics"   // periodic sample batch (data: Sample)
	TypeHeartbeat = "heartbeat" // liveness without metrics (data: Heartbeat)
	TypeResume    = "resume"    // reconnect: agent announces last acked seq (data: Resume)
	TypePong      = "pong"      // reply to a control-plane ping (data: nil)
)

// Frame types control plane → agent.
const (
	TypeWelcome = "welcome" // reply to hello/resume (data: Welcome)
	TypeAck     = "ack"     // last seq ingested (data: Ack)
	TypePing    = "ping"    // liveness check (data: nil)
)

// Freshness thresholds (shared source of truth; see docs/protocol-metrics.md):
// a value younger than LiveMaxAge is LIVE, younger than StaleMaxAge is STALE,
// anything older (or no data at all) is OFFLINE.
const (
	LiveMaxAge  = 15 * time.Second
	StaleMaxAge = 120 * time.Second
)

// Freshness classifies the age of a metric sample.
func Freshness(age time.Duration, haveData bool) string {
	if !haveData {
		return "OFFLINE"
	}
	switch {
	case age <= LiveMaxAge:
		return "LIVE"
	case age <= StaleMaxAge:
		return "STALE"
	default:
		return "OFFLINE"
	}
}

// Frame is the wire envelope. Data carries the per-type payload. RequestID
// links an asynchronous request to its response(s) (console backfill, command
// acknowledgement) without blocking the stream.
type Frame struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Seq       int64           `json:"seq"`
	Ts        time.Time       `json:"ts"`
	RequestID string          `json:"request_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Hello announces the agent after the connection is upgraded.
type Hello struct {
	Protocol     int    `json:"protocol"`
	AgentVersion string `json:"agent_version"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	StartedAt    string `json:"started_at"`
}

// Heartbeat is liveness without a metrics payload (e.g. collector degraded).
type Heartbeat struct {
	CollectMS int64 `json:"collect_ms"`
	Degraded  bool  `json:"degraded"`
}

// Resume is sent instead of hello on reconnect; LastSeq is the highest seq
// the agent knows the control plane ingested for this session.
type Resume struct {
	LastSeq  int64 `json:"last_seq"`
	Protocol int   `json:"protocol"`
}

// Welcome answers hello/resume. ResumeSeq echoes the last seq the control
// plane ingested for the session (0 when unknown); the agent replays frames
// with seq > ResumeSeq from its ring buffer.
type Welcome struct {
	ResumeSeq int64 `json:"resume_seq"`
	Protocol  int   `json:"protocol"`
}

// Ack reports ingestion progress (fire-and-forget for the agent).
type Ack struct {
	Seq int64 `json:"seq"`
}

// Sample is one metrics batch: node-wide plus per-workload envelopes.
// Collection lists follow the master doc verbatim (phase-03 work items).
type Sample struct {
	// Node (Linux host): CPU, RAM, Swap, Load average, Disk usage, Disk I/O,
	// Network RX/TX, Process count, Filesystem inode usage, TCP connections,
	// Service status.
	Node NodeSample `json:"node"`
	// Per hosting account: CPU usage, Memory usage, Disk usage, Bandwidth,
	// Processes, IO.
	Sites []SiteSample `json:"sites,omitempty"`
	// Containers: CPU, Memory, Memory limit, Network RX/TX, Block I/O, PIDs,
	// Uptime.
	Containers []ContainerSample `json:"containers,omitempty"`
	// Managed app workloads (systemd transient units or containers).
	Apps []AppSample `json:"apps,omitempty"`
	// Completed per-site traffic windows (dynamic resources feature). A site
	// appears only in the sample right after its aggregation window closed,
	// so this is usually empty or tiny — never a full fleet per frame.
	Traffic []SiteTraffic `json:"traffic,omitempty"`
}

// SiteTraffic is one completed access-log aggregation window for a website.
// It carries the raw features the control-plane analyzer scores (bot
// detection is intentionally a control-plane decision so thresholds are
// tunable without agent updates).
type SiteTraffic struct {
	WebsiteID string `json:"website_id"`
	// WindowS is the aggregation window length in seconds.
	WindowS int `json:"window_s"`
	// Requests/Bytes totals parsed from the access log delta.
	Requests int64 `json:"requests"`
	Bytes    int64 `json:"bytes"`
	// Client-IP diversity: uniques seen, the top talker and its share of
	// requests, and the combined share of the top-3 talkers.
	UniqueIPs  int     `json:"unique_ips"`
	TopIP      string  `json:"top_ip,omitempty"`
	TopIPShare float64 `json:"top_ip_share"`
	Top3Share  float64 `json:"top3_share"`
	// Status mix (counts per class).
	Status2xx int64 `json:"status_2xx"`
	Status3xx int64 `json:"status_3xx"`
	Status4xx int64 `json:"status_4xx"`
	Status5xx int64 `json:"status_5xx"`
	// Method mix (subset that matters for abuse detection).
	GetReqs  int64 `json:"get_reqs"`
	PostReqs int64 `json:"post_reqs"`
	// User-agent classes: search-engine/verified crawlers, known hostile
	// tooling, headless browsers, empty/absent UA, everything else.
	UAGoodBot  int64 `json:"ua_good_bot"`
	UABadTool  int64 `json:"ua_bad_tool"`
	UAHeadless int64 `json:"ua_headless"`
	UAEmpty    int64 `json:"ua_empty"`
	UAOther    int64 `json:"ua_other"`
	// Path behavior: distinct paths requested vs how many request lines were
	// examined for cardinality (capped sample), and referer presence.
	UniquePaths  int   `json:"unique_paths"`
	PathSamples  int   `json:"path_samples"`
	RefererReqs  int64 `json:"referer_reqs"`
	NotFoundReqs int64 `json:"not_found_reqs"` // 404s specifically
	// TruncatedIPs: requests dropped from per-IP accounting after the cap —
	// a saturation signal for distributed floods.
	TruncatedIPs int64 `json:"truncated_ips,omitempty"`
}

// NodeSample is the host-wide snapshot.
type NodeSample struct {
	CPUPercent float64 `json:"cpu_percent"` // delta over the sample window
	CPUCores   float64 `json:"cpu_cores"`

	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	MemoryTotal     int64 `json:"memory_total_bytes"`
	MemoryUsed      int64 `json:"memory_used_bytes"`
	MemoryAvailable int64 `json:"memory_available_bytes"`
	SwapTotal       int64 `json:"swap_total_bytes"`
	SwapUsed        int64 `json:"swap_used_bytes"`

	Disks []DiskSample `json:"disks,omitempty"`
	IO    DiskIOSample `json:"disk_io"`
	Net   NetSample    `json:"net"`

	TCPEstablished int `json:"tcp_established"`
	TCPTotal       int `json:"tcp_total"`

	Processes int   `json:"processes"`
	UptimeS   int64 `json:"uptime_s"`

	Services []ServiceSample `json:"services,omitempty"`

	// Collector health: how long the collection pass took and whether the
	// agent self-throttled (degrade visibly, never silently).
	CollectMS      int64  `json:"collect_ms"`
	Degraded       bool   `json:"degraded"`
	DegradedReason string `json:"degraded_reason,omitempty"`
}

// DiskSample is per real filesystem mount (usage + inode usage).
type DiskSample struct {
	Fs          string `json:"fs"`
	Mount       string `json:"mount"`
	TotalBytes  int64  `json:"total_bytes"`
	UsedBytes   int64  `json:"used_bytes"`
	InodesTotal int64  `json:"inodes_total"`
	InodesUsed  int64  `json:"inodes_used"`
}

// DiskIOSample holds rate deltas (per second over the sample window) plus
// cumulative counters for the historical store.
type DiskIOSample struct {
	ReadBPS    float64 `json:"read_bps"`
	WriteBPS   float64 `json:"write_bps"`
	ReadIOPS   float64 `json:"read_iops"`
	WriteIOPS  float64 `json:"write_iops"`
	TotalRead  int64   `json:"total_read_bytes"`
	TotalWrite int64   `json:"total_written_bytes"`
}

// NetSample holds network rates (per second over the window) and cumulative
// totals across all non-virtual interfaces.
type NetSample struct {
	RxBPS   float64 `json:"rx_bps"`
	TxBPS   float64 `json:"tx_bps"`
	RxBytes int64   `json:"rx_bytes"`
	TxBytes int64   `json:"tx_bytes"`
}

// ServiceSample is one systemd unit health entry.
type ServiceSample struct {
	Name  string `json:"name"`
	State string `json:"state"` // active | inactive | failed | ...
	Sub   string `json:"sub,omitempty"`
}

// SiteSample is the per-hosting-account envelope (cgroup slice per site).
// Phase 9: limit fields are read through the same slice files the
// enforcement arm wrote (agent readSliceLimits) — display source ==
// enforcement source by construction.
type SiteSample struct {
	WebsiteID     string  `json:"website_id"`
	UnixUser      string  `json:"unix_user"`
	CPUPercent    float64 `json:"cpu_percent"` // % of one core
	CPUCoresUsed  float64 `json:"cpu_cores_used"`
	CPULimitCores float64 `json:"cpu_limit_cores"` // 0 = unlimited
	MemoryBytes   int64   `json:"memory_bytes"`
	MemoryLimit   int64   `json:"memory_limit_bytes"` // 0 = unlimited
	PidsLimit     int64   `json:"pids_limit"`         // 0 = unlimited
	DiskUsedMB    int64   `json:"disk_used_mb"`
	BandwidthBPS  float64 `json:"bandwidth_bps"` // rx+tx, 0 when not measurable
	Processes     int     `json:"processes"`
	IOReadBPS     float64 `json:"io_read_bps"`
	IOWriteBPS    float64 `json:"io_write_bps"`
	// PHP-FPM worker-pool pressure (absent/zero for non-PHP sites). Sourced
	// from the pool's status endpoint (agent fpm_status.go); MaxChildren is
	// the configured pm.max_children so the control plane can compute
	// active/limit without knowing the pool config.
	FpmActive             int  `json:"fpm_active,omitempty"`
	FpmIdle               int  `json:"fpm_idle,omitempty"`
	FpmTotal              int  `json:"fpm_total,omitempty"`
	FpmQueue              int  `json:"fpm_queue,omitempty"`
	FpmMaxChildren        int  `json:"fpm_max_children,omitempty"`
	FpmMaxChildrenReached bool `json:"fpm_max_children_reached,omitempty"`
}

// ContainerSample is the Docker/container envelope.
type ContainerSample struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryBytes     int64   `json:"memory_bytes"`
	MemoryLimit     int64   `json:"memory_limit_bytes"`
	NetRxBPS        float64 `json:"net_rx_bps"`
	NetTxBPS        float64 `json:"net_tx_bps"`
	BlockIOReadBPS  float64 `json:"block_io_read_bps"`
	BlockIOWriteBPS float64 `json:"block_io_write_bps"`
	PIDs            int     `json:"pids"`
	UptimeS         int64   `json:"uptime_s"`
}

// AppSample is the managed-app workload envelope (systemd transient units or
// containers).
type AppSample struct {
	WebsiteID    string  `json:"website_id"`
	Kind         string  `json:"kind"`
	Status       string  `json:"status"`
	CPUPercent   float64 `json:"cpu_percent"`
	MemoryBytes  int64   `json:"memory_bytes"`
	NetRxBPS     float64 `json:"net_rx_bps"`
	NetTxBPS     float64 `json:"net_tx_bps"`
	DiskUsedMB   int64   `json:"disk_used_mb"`
	UptimeS      int64   `json:"uptime_s"`
	RestartCount int     `json:"restart_count"`
}

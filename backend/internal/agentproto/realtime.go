package agentproto

// Real-time console / lifecycle frames layered on the SAME persistent agent
// WebSocket as the metrics protocol. The metrics frames stay the liveness +
// resource channel; these frames make the console a STREAM (never a poll) and
// make server state an explicit state machine reported as events.
//
// ServerID on every payload is the WORKLOAD id (minecraft instance / bot /
// app), NOT the node id: one agent stream carries many workloads for one node.

// Frame types agent → control plane.
const (
	TypeConsoleOutput  = "console.output"  // batched console chunk (data: ConsoleOutput)
	TypeConsoleHistory = "console.history" // backfill answer (data: ConsoleHistory)
	TypeServerState    = "server.state"    // state transition (data: ServerStateEvent)
	TypeServerCrashed  = "server.crashed"  // unexpected exit (data: ServerStateEvent)
	TypeInstallOutput  = "installation.output"
	TypeSyncStart      = "sync.start"    // reconnect snapshot begins
	TypeSyncComplete   = "sync.complete" // reconnect snapshot ends (data: SyncSnapshot)
)

// Frame types control plane → agent.
const (
	TypeServerCommand  = "server.command"  // validated console command (data: ServerCommand)
	TypeConsoleRequest = "console.request" // backfill request (data: ConsoleRequest)
)

// Console stream tags.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// ConsoleOutput is one BATCHED chunk of process output. Data holds one or
// more complete lines joined by '\n'. FirstSeq..Seq is the per-workload
// monotonic sequence range covered by this chunk: the panel assigns each
// split line FirstSeq+i, so replay + live never gap or duplicate even when
// output is batched on the wire.
type ConsoleOutput struct {
	ServerID string `json:"server_id"`
	FirstSeq int64  `json:"first_seq"`
	Seq      int64  `json:"seq"`
	Stream   string `json:"stream"`
	Data     string `json:"data"`
	Ts       string `json:"ts,omitempty"`
}

// ConsoleHistory answers a ConsoleRequest: the lines with seq > FromSeq,
// oldest first, so the caller can assign FromSeq+1..ToSeq.
type ConsoleHistory struct {
	ServerID string   `json:"server_id"`
	FromSeq  int64    `json:"from_seq"`
	ToSeq    int64    `json:"to_seq"`
	Lines    []string `json:"lines"`
}

// ServerStateEvent announces a state-machine transition.
type ServerStateEvent struct {
	ServerID string `json:"server_id"`
	State    string `json:"state"`
	Desired  string `json:"desired,omitempty"`
	Reason   string `json:"reason,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Signal   string `json:"signal,omitempty"`
	At       string `json:"at,omitempty"`
}

// ServerCommand is a validated console command for one workload. The agent
// chooses the transport (stdin when it owns the process, RCON otherwise).
type ServerCommand struct {
	ServerID  string `json:"server_id"`
	Command   string `json:"command"`
	RequestID string `json:"request_id,omitempty"`
}

// ConsoleRequest asks the agent to backfill a workload's console history.
type ConsoleRequest struct {
	ServerID string `json:"server_id"`
	AfterSeq int64  `json:"after_seq"`
	Lines    int    `json:"lines,omitempty"`
}

// SyncEntry is one workload in the reconnect snapshot.
type SyncEntry struct {
	ServerID   string `json:"server_id"`
	State      string `json:"state"`
	Desired    string `json:"desired,omitempty"`
	Unit       string `json:"unit,omitempty"`
	ConsoleSeq int64  `json:"console_seq"`
}

// SyncSnapshot is the full node view sent as sync.start … sync.complete so a
// reconnecting panel never renders stale state (spec §36).
type SyncSnapshot struct {
	AgentVersion string      `json:"agent_version,omitempty"`
	At           string      `json:"at"`
	Servers      []SyncEntry `json:"servers"`
}

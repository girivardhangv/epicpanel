// Package minecraft implements Minecraft hosting as a FIRST-CLASS workload
// type (Phase 7) — never a web-hosting account with extra fields.
//
// Minecraft Instance owns: a container/workload (systemd transient unit via
// the agent's ContainerDriver; a Docker driver slots into the same
// interface), the Java runtime selection, the server JAR (downloaded from
// the provider at runtime — versions are never hardcoded), the world, the
// config, ports, and container-level CPU/RAM/disk/process limits.
//
// The node agent is AUTHORITATIVE for runtime state: the control plane
// records intent (desired_state) and reconciles against agent truth (live
// AppSample envelopes + finished jobs). An accepted API request is never
// mistaken for a successful operation.
package minecraft

import (
	"fmt"
	"strings"
	"time"
)

// DefaultBuildTimeout bounds provider install steps (jar download, installer
// run) on the agent.
const DefaultBuildTimeout = 15 * time.Minute

// JobLeaseDefault matches the wave contract (agent lease default 10 min);
// installs can run longer, so install jobs may override per enqueue.
const JobLeaseDefault = 10 * time.Minute

// Job types enqueued for Minecraft instances (job_type enum values added by
// migration 0028).
const (
	JobMCInstall      = "mc_install"
	JobMCStart        = "mc_start"
	JobMCStop         = "mc_stop"
	JobMCRestart      = "mc_restart"
	JobMCKill         = "mc_kill"
	JobMCStatus       = "mc_status"
	JobMCFiles        = "mc_files"
	JobMCUpload       = "mc_upload"
	JobMCLogs         = "mc_logs"
	JobMCCommand      = "mc_command"
	JobMCProperties   = "mc_properties"
	JobMCBackupWorld  = "mc_backup_world"
	JobMCRestoreWorld = "mc_restore_world"
	JobMCMetrics      = "mc_metrics"
	JobMCDelete       = "mc_delete"
)

// ---------------------------------------------------------------------------
// Wire payloads (control plane → agent). The RCON password travels ONLY as
// ciphertext; the agent decrypts once to write server.properties (0600) and
// to speak RCON. It never appears in logs, results or API responses.
// ---------------------------------------------------------------------------

// MCInstallPayload is the mc_install job body.
type MCInstallPayload struct {
	InstanceID  string            `json:"instance_id"`
	Provider    string            `json:"provider"`
	Version     string            `json:"version"`
	JavaMajor   int               `json:"java_major"`
	Port        int               `json:"port"`
	RCONPort    int               `json:"rcon_port"`
	RCONPassEnc string            `json:"rcon_pass_enc"`
	XmxMB       int64             `json:"xmx_mb"`
	EULA        bool              `json:"eula_accepted"`
	Properties  map[string]string `json:"properties,omitempty"`
}

// MCLifecyclePayload is the start/restart body (stop/kill need only the id).
type MCLifecyclePayload struct {
	InstanceID    string            `json:"instance_id"`
	Provider      string            `json:"provider"`
	Version       string            `json:"version"`
	JavaMajor     int               `json:"java_major"`
	Port          int               `json:"port"`
	RCONPort      int               `json:"rcon_port"`
	RCONPassEnc   string            `json:"rcon_pass_enc"`
	XmxMB         int64             `json:"xmx_mb"`
	Properties    map[string]string `json:"properties,omitempty"`
	ExtraArgs     []string          `json:"extra_args,omitempty"`
	RestartPolicy string            `json:"restart_policy"`
	Limits        MCLimitsPayload   `json:"limits,omitempty"`
}

// MCLimitsPayload mirrors the Phase 9 engine numbers for the unit (CPU % of
// one core, RAM MB, disk MB accounted, PIDs count) — the SAME numbers the
// usage bars display (anti-drift rule).
type MCLimitsPayload struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemoryMB   int64   `json:"memory_mb"`
	DiskMB     int64   `json:"disk_mb"`
	PidsMax    int64   `json:"pids_max"`
}

// MCIDPayload is the stop/kill/status/logs/delete body.
type MCIDPayload struct {
	InstanceID string `json:"instance_id"`
}

// MCCommandPayload is the mc_command body. The command was validated
// against the console allowlist BEFORE enqueue; the agent validates again.
type MCCommandPayload struct {
	InstanceID string `json:"instance_id"`
	Command    string `json:"command"`
}

// MCFilesPayload lists a directory inside the instance tree.
type MCFilesPayload struct {
	InstanceID string `json:"instance_id"`
	Path       string `json:"path"`
}

// MCUploadPayload carries one file (base64) into the instance tree.
type MCUploadPayload struct {
	InstanceID string `json:"instance_id"`
	Path       string `json:"path"`
	B64        string `json:"content_b64"`
}

// MCLogsPayload is the console snapshot body.
type MCLogsPayload struct {
	InstanceID string `json:"instance_id"`
	Lines      int    `json:"lines"`
	AfterSeq   int64  `json:"after_seq"`
}

// MCPropertiesPayload writes/reads server.properties agent-side. Protected
// keys (ports/rcon) are enforced on both ends.
type MCPropertiesPayload struct {
	InstanceID string            `json:"instance_id"`
	Action     string            `json:"action"` // get | set
	Properties map[string]string `json:"properties,omitempty"`
}

// MCBackupPayload snapshots the world (save-off → copy → save-on seam).
type MCBackupPayload struct {
	InstanceID string `json:"instance_id"`
	BackupName string `json:"backup_name"`
}

// MCRestorePayload restores a world snapshot (stop → swap → start seam).
type MCRestorePayload struct {
	InstanceID    string          `json:"instance_id"`
	BackupName    string          `json:"backup_name"`
	Provider      string          `json:"provider"`
	Version       string          `json:"version"`
	JavaMajor     int             `json:"java_major"`
	Port          int             `json:"port"`
	RCONPort      int             `json:"rcon_port"`
	RCONPassEnc   string          `json:"rcon_pass_enc"`
	XmxMB         int64           `json:"xmx_mb"`
	RestartPolicy string          `json:"restart_policy"`
	Limits        MCLimitsPayload `json:"limits,omitempty"`
}

// MCMetricsPayload polls players/TPS/MSPT on demand (honesty flags included).
type MCMetricsPayload struct {
	InstanceID  string `json:"instance_id"`
	RCONPort    int    `json:"rcon_port"`
	RCONPassEnc string `json:"rcon_pass_enc"`
}

// ---------------------------------------------------------------------------
// Wire outcomes (agent → control plane). All free text is scrubbed of the
// RCON password agent-side; the control plane scrubs again at the API edge.
// ---------------------------------------------------------------------------

// MCStatusOutcome is the agent truth for start/stop/restart/kill/status.
type MCStatusOutcome struct {
	InstanceID string `json:"instance_id"`
	Unit       string `json:"unit"`
	UnitState  string `json:"unit_state"`
	UptimeS    int64  `json:"uptime_s"`
	Restarts   int    `json:"restarts"`
	Port       int    `json:"port"`
	Log        string `json:"log,omitempty"`
}

// MCOutcome is the generic install/upload/delete/properties result.
type MCOutcome struct {
	InstanceID string `json:"instance_id"`
	OK         bool   `json:"ok"`
	Log        string `json:"log"`
}

// MCLogsOutcome is the console snapshot payload.
type MCLogsOutcome struct {
	InstanceID string        `json:"instance_id"`
	Lines      []ConsoleLine `json:"lines"`
	NextSeq    int64         `json:"next_seq"`
}

// MCCommandOutcome carries the RCON response lines for one console command.
type MCCommandOutcome struct {
	InstanceID string   `json:"instance_id"`
	Command    string   `json:"command"`
	Response   []string `json:"response"`
	OK         bool     `json:"ok"`
}

// MCMetricsOutcome is the on-demand metrics snapshot with honesty flags: a
// server that does not expose TPS/MSPT over RCON reports the gap instead of
// pretending.
type MCMetricsOutcome struct {
	InstanceID string  `json:"instance_id"`
	Players    int     `json:"players"`
	MaxPlayers int     `json:"max_players"`
	TPS        float64 `json:"tps"`
	MSPT       float64 `json:"mspt"`
	TPSKnown   bool    `json:"tps_known"`
	MSPTKnown  bool    `json:"mspt_known"`
	At         string  `json:"at"`
	Detail     string  `json:"detail,omitempty"`
}

// MCBackupOutcome registers one completed world backup.
type MCBackupOutcome struct {
	InstanceID string `json:"instance_id"`
	BackupName string `json:"backup_name"`
	Path       string `json:"path"`
	SizeBytes  int64  `json:"size_bytes"`
	SHA256     string `json:"sha256"`
}

// MCPropertiesOutcome is the parsed server.properties (protected keys and
// the RCON password excluded).
type MCPropertiesOutcome struct {
	InstanceID string            `json:"instance_id"`
	Properties map[string]string `json:"properties"`
}

// FileEntry is one listed file.
type FileEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// MCFilesOutcome is a directory listing.
type MCFilesOutcome struct {
	InstanceID string      `json:"instance_id"`
	Path       string      `json:"path"`
	Entries    []FileEntry `json:"entries"`
}

// ConsoleLine is one console ring line (seq is the console cursor).
type ConsoleLine struct {
	Seq  int64  `json:"seq"`
	Ts   string `json:"ts"`
	Text string `json:"text"`
}

// ---------------------------------------------------------------------------
// Lifecycle: verbatim state machine installing → stopped → starting →
// running → stopping → crashed, plus failed/deleting/deleted.
// ---------------------------------------------------------------------------

// Status is the verbatim lifecycle.
type Status string

const (
	StatusInstalling Status = "installing"
	StatusStopped    Status = "stopped"
	StatusStarting   Status = "starting"
	StatusRunning    Status = "running"
	StatusStopping   Status = "stopping"
	StatusCrashed    Status = "crashed"
	StatusFailed     Status = "failed"
	StatusDeleting   Status = "deleting"
	StatusDeleted    Status = "deleted"
)

// DesiredState is what the customer asked for; the agent reconciles.
const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
)

// transitions is the complete legal-edge set (guarded by CanTransition).
var transitions = map[Status][]Status{
	StatusInstalling: {StatusStopped, StatusStarting, StatusRunning, StatusFailed, StatusDeleting},
	StatusStopped:    {StatusStarting, StatusDeleting, StatusFailed},
	StatusStarting:   {StatusRunning, StatusCrashed, StatusFailed, StatusStopped, StatusDeleting},
	StatusRunning:    {StatusStopping, StatusCrashed, StatusStarting, StatusDeleting, StatusFailed},
	StatusStopping:   {StatusStopped, StatusCrashed, StatusFailed, StatusDeleting},
	StatusCrashed:    {StatusStarting, StatusStopped, StatusDeleting, StatusFailed},
	StatusFailed:     {StatusInstalling, StatusStopped, StatusDeleting, StatusDeleted},
	StatusDeleting:   {StatusDeleted},
	StatusDeleted:    {},
}

// CanTransition reports whether from→to is a legal edge.
func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// TransitionError carries the illegal edge for audit + API mapping.
type TransitionError struct{ From, To Status }

func (e *TransitionError) Error() string {
	return "illegal minecraft state transition " + string(e.From) + " -> " + string(e.To)
}

// AgentStatus maps a systemd unit state (agent truth) to an instance Status.
func AgentStatus(unitState string, desired string) Status {
	switch unitState {
	case "active":
		if desired == DesiredRunning {
			return StatusRunning
		}
		return StatusStopping // running but not wanted: converge by stopping
	case "activating":
		return StatusStarting
	case "deactivating":
		return StatusStopping
	case "failed":
		return StatusCrashed
	case "inactive", "":
		return StatusStopped
	default:
		return StatusStopped
	}
}

// LifecycleGuard is the pure transition policy used by handlers and tests.
type LifecycleGuard struct{}

// CanStart reports whether a customer start may be enqueued from s.
func (LifecycleGuard) CanStart(s Status) bool {
	return s == StatusStopped || s == StatusCrashed || s == StatusFailed || s == StatusInstalling
}

// CanStop reports whether a customer stop may be enqueued from s. stopped is
// allowed: MarkStopping is a no-op there (idempotent double-stop).
func (LifecycleGuard) CanStop(s Status) bool {
	return s == StatusRunning || s == StatusStarting || s == StatusStopping || s == StatusCrashed || s == StatusStopped
}

// CanDelete reports whether deletion may begin from s.
func (LifecycleGuard) CanDelete(s Status) bool {
	return s != StatusDeleting && s != StatusDeleted
}

// ---------------------------------------------------------------------------
// Validation + limits + events
// ---------------------------------------------------------------------------

// ValidInstanceName enforces the instance name shape (mirrors the DB CHECK:
// min 2 chars, alnum edges).
func ValidInstanceName(name string) bool {
	if len(name) < 2 || len(name) > 63 {
		return false
	}
	for i, c := range name {
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !alnum && c != '-' && c != '_' {
			return false
		}
		if (i == 0 || i == len(name)-1) && !alnum {
			return false
		}
	}
	return true
}

// MinXmxMB is the smallest JVM heap a Minecraft server can boot with.
const MinXmxMB = 512

// XmxForPlan derives the JVM heap from the plan RAM: the JVM needs overhead
// beyond -Xmx (metaspace, direct buffers, OS page cache), so the heap gets
// 3/4 of the plan RAM, floored at MinXmxMB.
func XmxForPlan(ramMB int64) int64 {
	if ramMB <= 0 {
		return MinXmxMB
	}
	xmx := ramMB * 3 / 4
	if xmx < MinXmxMB {
		xmx = MinXmxMB
	}
	if xmx < MinXmxMB {
		return MinXmxMB
	}
	return xmx
}

// EpisodeWindow bounds how long a crash-recovery episode counts restarts
// before the counter resets (a healthy run longer than this starts fresh).
const EpisodeWindow = 15 * time.Minute

// ProtectedPropertyKeys are server.properties keys the control plane and the
// agent never let customers overwrite: ports/RCON are allocated and managed
// by the panel; server-ip binds the workload.
var ProtectedPropertyKeys = map[string]bool{
	"server-port":         true,
	"server-ip":           true,
	"enable-rcon":         true,
	"rcon.port":           true,
	"rcon.password":       true,
	"query.port":          true,
	"level-seed":          false, // changeable pre-start is harmless; keep allowed
	"level-type":          false,
	"max-players":         false,
	"online-mode":         false,
	"motd":                false,
	"view-distance":       false,
	"simulation-distance": false,
	"difficulty":          false,
	"gamemode":            false,
	"pvp":                 false,
	"white-list":          false,
	"spawn-protection":    false,
}

// IsProtectedProperty reports whether a key is panel-managed.
func IsProtectedProperty(key string) bool {
	return ProtectedPropertyKeys[key] || strings.HasPrefix(key, "rcon.")
}

// ValidateProperty checks one server.properties key/value pair (safe file
// line: no newline injection, bounded size, protected keys rejected).
func ValidateProperty(key, value string) error {
	if key == "" || len(key) > 64 {
		return fmt.Errorf("invalid property key")
	}
	if IsProtectedProperty(key) {
		return fmt.Errorf("property %q is managed by the panel and cannot be changed", key)
	}
	for _, c := range key + value {
		if c == '\n' || c == '\r' || c == '\x00' {
			return fmt.Errorf("property values cannot contain line breaks")
		}
	}
	if len(value) > 512 {
		return fmt.Errorf("property value too large (max 512 bytes)")
	}
	return nil
}

// SanitizeProperties validates a whole map (used by create + PUT properties).
func SanitizeProperties(props map[string]string) (map[string]string, error) {
	if len(props) > 128 {
		return nil, fmt.Errorf("too many properties (max 128)")
	}
	out := make(map[string]string, len(props))
	for k, v := range props {
		if err := ValidateProperty(k, v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// Event types published on the bus for instance lifecycle changes.
const (
	EventMCCreated       = "minecraft.created"
	EventMCStarting      = "minecraft.starting"
	EventMCStarted       = "minecraft.started"
	EventMCStopping      = "minecraft.stopping"
	EventMCStopped       = "minecraft.stopped"
	EventMCCrashed       = "minecraft.crashed"
	EventMCDeleted       = "minecraft.deleted"
	EventMCScheduleFired = "minecraft.schedule_fired"
	EventMCBackupDone    = "minecraft.backup_completed"
)

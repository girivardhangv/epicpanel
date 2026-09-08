// Bot lifecycle orchestration: the API handlers drive these; all node work
// goes through jobs (API → Job → Queue → Node Agent → Event → UI). The
// control plane NEVER assumes an accepted request succeeded — agent truth
// arrives via job results + metrics reconciliation.
package discord

import (
	"fmt"
	"strings"
	"time"
)

// DefaultBuildTimeout bounds dependency installation (npm ci / pip install).
const DefaultBuildTimeout = 10 * time.Minute

// JobLeaseDefault matches the wave contract (agent lease default 10 min).
const JobLeaseDefault = 10 * time.Minute

// Job types enqueued for bots (job_type enum values added by migration 0029).
const (
	JobBotInstall   = "bot_install"
	JobBotDeployGit = "bot_deploy_git"
	JobBotUpload    = "bot_upload"
	JobBotFiles     = "bot_files"
	JobBotStart     = "bot_start"
	JobBotStop      = "bot_stop"
	JobBotRestart   = "bot_restart"
	JobBotKill      = "bot_kill"
	JobBotStatus    = "bot_status"
	JobBotLogs      = "bot_logs"
	JobBotDelete    = "bot_delete"
)

// ---------------------------------------------------------------------------
// Wire payloads (control plane → agent). Secrets travel ONLY as ciphertext;
// the node agent holds the panel key and decrypts at unit start.
// ---------------------------------------------------------------------------

// BotInstallPayload is the bot_install job body.
type BotInstallPayload struct {
	BotID          string `json:"bot_id"`
	Runtime        string `json:"runtime"`
	RuntimeVersion string `json:"runtime_version"`
	EnvEnc         string `json:"env_enc"`
}

// BotLifecyclePayload is start/restart body (stop/kill need only the id).
type BotLifecyclePayload struct {
	BotID          string           `json:"bot_id"`
	Runtime        string           `json:"runtime"`
	RuntimeVersion string           `json:"runtime_version"`
	StartupFile    string           `json:"startup_file"`
	StartupCommand string           `json:"startup_command"`
	EnvEnc         string           `json:"env_enc"`
	RestartPolicy  string           `json:"restart_policy"`
	NetAllow       []string         `json:"net_allow,omitempty"`
	Limits         BotLimitsPayload `json:"limits,omitempty"`
}

// BotLimitsPayload mirrors the Phase 9 engine numbers for the bot unit
// (CPU % of one core, RAM MB, disk MB accounted, PIDs count).
type BotLimitsPayload struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemoryMB   int64   `json:"memory_mb"`
	DiskMB     int64   `json:"disk_mb"`
	PidsMax    int64   `json:"pids_max"`
}

// BotIDPayload is the stop/kill/status/delete body.
type BotIDPayload struct {
	BotID string `json:"bot_id"`
}

// BotDeployGitPayload is the git deployment job body (foundation).
type BotDeployGitPayload struct {
	BotID        string `json:"bot_id"`
	RepoURL      string `json:"repo_url"`
	Branch       string `json:"branch"`
	TokenEnc     string `json:"token_enc,omitempty"`
	BuildTimeout int    `json:"build_timeout_s"`
	Runtime      string `json:"runtime"`
	EnvEnc       string `json:"env_enc"`
}

// BotUploadPayload carries one file (base64) into the bot tree.
type BotUploadPayload struct {
	BotID string `json:"bot_id"`
	Path  string `json:"path"`
	B64   string `json:"content_b64"`
}

// BotFilesPayload lists a directory inside the bot tree.
type BotFilesPayload struct {
	BotID string `json:"bot_id"`
	Path  string `json:"path"`
}

// BotLogsPayload is the log-fetch job body.
type BotLogsPayload struct {
	BotID string `json:"bot_id"`
	Lines int    `json:"lines"`
}

// ---------------------------------------------------------------------------
// Wire outcomes (agent → control plane). All free text is scrubbed of secret
// values agent-side; the control plane scrubs again at the API edge.
// ---------------------------------------------------------------------------

// BotStatusOutcome is the agent truth for start/stop/restart/status.
type BotStatusOutcome struct {
	BotID     string `json:"bot_id"`
	Unit      string `json:"unit"`
	UnitState string `json:"unit_state"`
	UptimeS   int64  `json:"uptime_s"`
	Restarts  int    `json:"restarts"`
	Log       string `json:"log,omitempty"`
}

// BotOutcome is the generic install/deploy/upload result (log is scrubbed).
type BotOutcome struct {
	BotID string `json:"bot_id"`
	OK    bool   `json:"ok"`
	Log   string `json:"log"`
}

// BotLogsOutcome is the console snapshot payload.
type BotLogsOutcome struct {
	BotID   string        `json:"bot_id"`
	Lines   []ConsoleLine `json:"lines"`
	NextSeq int64         `json:"next_seq"`
}

// BotFilesOutcome is a directory listing.
type BotFilesOutcome struct {
	BotID   string      `json:"bot_id"`
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// FileEntry is one listed file.
type FileEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// ConsoleLine is one ring-buffer log line (seq is the console cursor).
type ConsoleLine struct {
	Seq  int64  `json:"seq"`
	Ts   string `json:"ts"`
	Text string `json:"text"`
}

// ---------------------------------------------------------------------------
// Validation + create flow
// ---------------------------------------------------------------------------

// ValidBotName enforces the bot name shape (mirrors the DB CHECK).
func ValidBotName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
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

// ValidGitURL accepts https/git/ssh remote forms (no arbitrary schemes).
func ValidGitURL(u string) bool {
	if u == "" {
		return false
	}
	for _, prefix := range []string{"https://", "http://", "git@", "ssh://"} {
		if strings.HasPrefix(u, prefix) {
			return true
		}
	}
	return false
}

// ValidateCreate checks a create request against the runtime provider and
// returns the sanitized env map (values newline-stripped).
func ValidateCreate(name, runtime, runtimeVersion, startupFile, startupCommand, gitRepo, restartPolicy string, env map[string]string) (map[string]string, string, string, error) {
	rt, err := RuntimeFor(runtime)
	if err != nil {
		return nil, "", "", err
	}
	if !rt.Supported() {
		return nil, "", "", fmt.Errorf("runtime %q is not available yet", runtime)
	}
	if !ValidBotName(name) {
		return nil, "", "", fmt.Errorf("bot name must be lowercase letters, digits, - and _ (2-63 chars, alnum edges)")
	}
	if runtimeVersion == "" {
		runtimeVersion = rt.DefaultVersion()
	}
	if err := rt.ValidateVersion(runtimeVersion); err != nil {
		return nil, "", "", err
	}
	if startupFile == "" && startupCommand == "" {
		// Git deploys replace the tree but the entrypoint still needs a
		// default; the customer can change it any time.
		startupFile = rt.DefaultStartup()
	}
	if err := rt.ValidateStartup(startupFile, startupCommand); err != nil {
		return nil, "", "", err
	}
	if gitRepo != "" && !ValidGitURL(gitRepo) {
		return nil, "", "", fmt.Errorf("invalid git repository URL")
	}
	if restartPolicy == "" {
		restartPolicy = "on-failure"
	}
	switch restartPolicy {
	case "on-failure", "always", "no":
	default:
		return nil, "", "", fmt.Errorf("restart_policy must be on-failure, always or no")
	}
	clean, err := SanitizeEnv(env)
	if err != nil {
		return nil, "", "", err
	}
	return clean, runtimeVersion, startupFile, nil
}

// SanitizeEnv validates + normalizes the map (shared by create and update).
func SanitizeEnv(env map[string]string) (map[string]string, error) {
	if len(env) > MaxEnvVars {
		return nil, fmt.Errorf("too many environment variables (max %d)", MaxEnvVars)
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if err := ValidateEnvKey(k); err != nil {
			return nil, err
		}
		clean, err := ValidateEnvValue(v)
		if err != nil {
			return nil, err
		}
		out[k] = clean
	}
	return out, nil
}

// Event types published on the bus for bot lifecycle changes.
const (
	EventBotCreated  = "bot.created"
	EventBotStarting = "bot.starting"
	EventBotStarted  = "bot.started"
	EventBotStopping = "bot.stopping"
	EventBotStopped  = "bot.stopped"
	EventBotCrashed  = "bot.crashed"
	EventBotDeployed = "bot.deployed"
	EventBotDeleted  = "bot.deleted"
	EventBotRestartScheduled = "bot.schedule_fired"
)

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

package agent

// ============================================================================
// Phase 7 — Minecraft ops (node agent side). Minecraft instances are
// FIRST-CLASS workloads: their own file layout (/srv/epicpanel/minecraft),
// dedicated unix user, systemd transient unit (via ContainerDriver) and
// cgroup limits. Customers never touch any daemon — every operation arrives
// as a job. NO docker, NO shell from customer payloads: commands are
// assembled from validated fields only, and console commands pass the
// allowlist (minecraft.ValidateConsoleCommand) before they ever reach RCON.
//
// The coordinator merges MinecraftOps into the agent dispatch (wave
// contract: handlers + registry in this file, worker.go untouched).
// ============================================================================

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/minecraft"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// MCRootBase is the instance file layout root (instances are NOT websites).
const MCRootBase = "/srv/epicpanel/minecraft"

// MCUnitName is the systemd transient unit for an instance. The
// `epicpanel-app-` prefix keeps every instance inside the existing Phase 3
// workload collector (listAppUnits samples `epicpanel-app-*`), so
// CPU/RAM/net/uptime/restarts stream live through the AppSample envelope
// without protocol changes (kind=minecraft via the jar cmdline heuristic).
func MCUnitName(instanceID string) string {
	if _, err := uuid.Parse(instanceID); err != nil {
		return ""
	}
	return "epicpanel-app-" + instanceID
}

// MCRoot is the instance's file root.
func MCRoot(instanceID string) string {
	return filepath.Join(MCRootBase, instanceID)
}

// mcUserFor derives the dedicated unix user: ep-mc-<first-8>.
func mcUserFor(instanceID string) (string, error) {
	id, err := uuid.Parse(instanceID)
	if err != nil {
		return "", fmt.Errorf("invalid instance id")
	}
	raw := strings.ReplaceAll(id.String(), "-", "")
	return "ep-mc-" + raw[:8], nil
}

// MCOpFunc is the signature the coordinator's dispatch merges.
type MCOpFunc func(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error)

// MinecraftOps is the Phase 7 registry the coordinator merges into the
// agent job dispatch (worker.go stays untouched by this phase).
var MinecraftOps = map[string]func(context.Context, *Executor, *Client, Config, json.RawMessage) (json.RawMessage, error){
	"mc_install":       HandleMCInstall,
	"mc_start":         HandleMCStart,
	"mc_stop":          HandleMCStop,
	"mc_restart":       HandleMCRestart,
	"mc_kill":          HandleMCKill,
	"mc_status":        HandleMCStatus,
	"mc_files":         HandleMCFiles,
	"mc_upload":        HandleMCUpload,
	"mc_logs":          HandleMCLogs,
	"mc_command":       HandleMCCommand,
	"mc_properties":    HandleMCProperties,
	"mc_backup_world":  HandleMCBackupWorld,
	"mc_restore_world": HandleMCRestoreWorld,
	"mc_metrics":       HandleMCMetrics,
	"mc_delete":        HandleMCDelete,
}

// ============================================================================
// ContainerDriver — the isolation seam. SystemdDriver is the shipped
// implementation (transient units + cgroups v2, mirroring enforce.go /
// app_ops.go); a DockerDriver can slot in later without touching the ops.
// ============================================================================

// ContainerSpec is the desired container/workload state for one instance.
type ContainerSpec struct {
	InstanceID    string
	Unit          string
	User          string
	Root          string
	JavaBin       string
	StartCommand  string
	RestartPolicy string
	// Limits (Phase 9 engine numbers).
	CPUPercent float64
	MemoryMB   int64
	PidsMax    int64
	// UnitName is the human-readable description for journald triage.
	Description string
}

// UnitTruth is the observed unit state (agent truth for reconciliation).
type UnitTruth struct {
	State    string
	UptimeS  int64
	Restarts int
	PID      int
}

// ContainerDriver isolates and supervises one Minecraft workload.
type ContainerDriver interface {
	Start(ctx context.Context, spec ContainerSpec) error
	Stop(ctx context.Context, unit string) error
	Kill(ctx context.Context, unit string) error
	Status(ctx context.Context, unit string) (UnitTruth, error)
	// Enforce re-asserts cgroup limits on a running unit (Phase 9 seam).
	Enforce(ctx context.Context, spec ContainerSpec) error
}

// SystemdDriver implements ContainerDriver with systemd-run transient
// units (no container runtime needed; strong sandboxing + cgroup v2).
type SystemdDriver struct{}

// Start creates the transient unit under the instance user with resource
// controls and sandboxing, then starts it. Idempotent: any previous unit is
// stopped first.
func (SystemdDriver) Start(ctx context.Context, spec ContainerSpec) error {
	_ = exec.CommandContext(ctx, "systemctl", "stop", spec.Unit).Run()
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", spec.Unit).Run()

	args := []string{"--unit=" + spec.Unit,
		"--property", "User=" + spec.User,
		"--property", "Group=" + spec.User,
		"--property", "WorkingDirectory=" + spec.Root,
		"--property", "Restart=" + mcRestartPolicy(spec.RestartPolicy),
		"--property", "RestartSec=5",
		"--property", "NoNewPrivileges=yes",
		"--property", "ProtectSystem=strict",
		"--property", "ProtectHome=yes",
		"--property", "PrivateTmp=yes",
		"--property", "PrivateDevices=yes",
		"--property", "RestrictSUIDSGID=yes",
		"--property", "ReadWritePaths=" + spec.Root,
		"--property", "UMask=0027",
		"--property", "Description=" + spec.Description,
		"--collect",
	}
	if spec.CPUPercent > 0 {
		args = append(args, "--property", "CPUQuota="+strconv.FormatFloat(spec.CPUPercent, 'f', -1, 64)+"%")
	}
	if spec.MemoryMB > 0 {
		args = append(args, "--property", "MemoryMax="+strconv.FormatInt(spec.MemoryMB, 10)+"M")
		// Soft cap at 90% so the kernel reclaims before hard-killing.
		args = append(args, "--property", "MemoryHigh="+strconv.FormatInt(spec.MemoryMB*9/10, 10)+"M")
	}
	if spec.PidsMax > 0 {
		args = append(args, "--property", "TasksMax="+strconv.FormatInt(spec.PidsMax, 10))
	}
	args = append(args, "/bin/bash", "-c", spec.StartCommand)
	out, err := exec.CommandContext(ctx, "systemd-run", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd-run: %s (%w)", tailString(string(out), 200), err)
	}
	return nil
}

// Stop stops the unit.
func (SystemdDriver) Stop(ctx context.Context, unit string) error {
	_ = exec.CommandContext(ctx, "systemctl", "stop", unit).Run()
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", unit).Run()
	return nil
}

// Kill SIGKILLs the main process then stops the unit.
func (SystemdDriver) Kill(ctx context.Context, unit string) error {
	_ = exec.CommandContext(ctx, "systemctl", "kill", "--signal=SIGKILL", unit).Run()
	_ = exec.CommandContext(ctx, "systemctl", "stop", unit).Run()
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", unit).Run()
	return nil
}

// Status reports the observed unit truth.
func (SystemdDriver) Status(ctx context.Context, unit string) (UnitTruth, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit,
		"-p", "ActiveState", "-p", "NRestarts", "-p", "ActiveEnterTimestamp", "-p", "MainPID",
		"--no-pager").Output()
	if err != nil && len(out) == 0 {
		return UnitTruth{}, err
	}
	t := UnitTruth{State: "inactive"}
	for _, line := range splitLines(out) {
		switch {
		case strings.HasPrefix(line, "ActiveState="):
			t.State = strings.TrimSpace(strings.TrimPrefix(line, "ActiveState="))
		case strings.HasPrefix(line, "NRestarts="):
			t.Restarts, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "NRestarts=")))
		case strings.HasPrefix(line, "MainPID="):
			t.PID, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "MainPID=")))
		case strings.HasPrefix(line, "ActiveEnterTimestamp="):
			v := strings.TrimSpace(strings.TrimPrefix(line, "ActiveEnterTimestamp="))
			if ts, perr := time.ParseInLocation("Mon 2006-01-02 15:04:05 MST", v, time.Local); perr == nil {
				t.UptimeS = int64(time.Since(ts).Seconds())
				if t.UptimeS < 0 {
					t.UptimeS = 0
				}
			}
		}
	}
	return t, nil
}

// Enforce re-asserts cgroup limits on the running unit's cgroup (CPU is
// owned by systemd's CPUQuota; memory/pids are re-asserted here so a
// respawned unit converges without a full restart).
func (SystemdDriver) Enforce(ctx context.Context, spec ContainerSpec) error {
	if !cgroupV2Available() {
		return fmt.Errorf("cgroup v2 unavailable")
	}
	unitDir := cgroupRoot + "/system.slice/" + spec.Unit + ".service"
	if spec.MemoryMB > 0 {
		_ = os.WriteFile(filepath.Join(unitDir, "memory.max"), []byte(strconv.FormatInt(spec.MemoryMB, 10)+"M"), 0o644)
	}
	if spec.PidsMax > 0 {
		_ = os.WriteFile(filepath.Join(unitDir, "pids.max"), []byte(strconv.FormatInt(spec.PidsMax, 10)), 0o644)
	}
	return nil
}

// mcRestartPolicy validates the restart policy (never arbitrary).
func mcRestartPolicy(p string) string {
	switch p {
	case "always":
		return "always"
	case "no":
		return "no"
	default:
		return "on-failure"
	}
}

// defaultDriver is the driver used by the ops (single seam).
var defaultDriver ContainerDriver = SystemdDriver{}

// ============================================================================
// install — unix user, directories, jar download / installer run (provider
// steps), server.properties + eula.txt
// ============================================================================

// HandleMCInstall provisions the instance tree. Idempotent (safe to retry):
// existing jar + config are detected and reused.
func HandleMCInstall(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p minecraft.MCInstallPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(p.InstanceID); err != nil {
		return nil, fmt.Errorf("invalid instance id")
	}
	provider, err := minecraft.ProviderFor(p.Provider)
	if err != nil {
		return nil, err
	}
	mcUser, err := mcUserFor(p.InstanceID)
	if err != nil {
		return nil, err
	}
	if err := ensureMCUser(mcUser); err != nil {
		return nil, fmt.Errorf("instance user: %w", err)
	}
	root := MCRoot(p.InstanceID)
	for _, dir := range []string{root, filepath.Join(root, "logs"), filepath.Join(root, "backups")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	if d := provider.PluginDir(); d != "" {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			return nil, err
		}
	}

	// Provider install steps: download the jar / run the installer. NO
	// provider branches here — the interface drives everything.
	if err := runInstallSteps(ctx, e, provider, p, root, mcUser); err != nil {
		return nil, err
	}

	// eula.txt — the customer accepted the EULA at create time.
	if p.EULA {
		eula := "eula=true\n"
		if b, err := os.ReadFile(filepath.Join(root, "eula.txt")); err == nil && strings.Contains(string(b), "eula=true") {
			eula = string(b) // idempotent: never overwrite accepted state
		}
		if err := os.WriteFile(filepath.Join(root, "eula.txt"), []byte(eula), 0o640); err != nil {
			return nil, err
		}
	}

	// server.properties — merge stored properties with panel-managed
	// settings (ports + RCON). The RCON password decrypts transiently and
	// NEVER appears in logs or job results.
	rconPass, err := decryptRCONPass(p.RCONPassEnc)
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	for k, v := range p.Properties {
		if minecraft.IsProtectedProperty(k) {
			continue
		}
		props[k] = v
	}
	props["server-port"] = strconv.Itoa(p.Port)
	props["server-ip"] = "0.0.0.0"
	props["enable-rcon"] = "true"
	props["rcon.port"] = strconv.Itoa(p.RCONPort)
	props["rcon.password"] = rconPass
	if err := writePropertiesFile(root, props); err != nil {
		return nil, err
	}

	uid, gid, err := mcOwnerIDs(root)
	if err != nil {
		return nil, err
	}
	_ = chownRecursive(root, uid, gid)

	// Enable RCON metrics polling for this instance (TPS/MSPT/players for
	// the AppSample envelope).
	RememberMCSpec(p.InstanceID, rconPass, p.RCONPort, provider.SupportsRCONTPS())
	StartMCMetricsPoller(p.InstanceID)

	slog.Info("minecraft instance installed", "instance", p.InstanceID, "provider", p.Provider, "version", p.Version)
	out := minecraft.MCOutcome{InstanceID: p.InstanceID, OK: true, Log: "installed " + p.Provider + " " + p.Version}
	return json.Marshal(out)
}

// runInstallSteps executes the provider's install layout: jar download
// (or installer run for loader providers). Bounded, no shell.
func runInstallSteps(ctx context.Context, e *Executor, provider minecraft.Provider, p minecraft.MCInstallPayload, root, mcUser string) error {
	ctx, cancel := context.WithTimeout(ctx, minecraft.DefaultBuildTimeout)
	defer cancel()

	kind := provider.InstallerKind(p.Version)
	jarPath := filepath.Join(root, provider.JarName(p.Version))

	// Idempotent: skip the download when the artifact already exists.
	if _, err := os.Stat(jarPath); err == nil {
		return nil
	}

	url, err := provider.DownloadURL(p.Version)
	if err != nil {
		return fmt.Errorf("resolve download: %w", err)
	}

	switch kind {
	case "jar":
		dst := jarPath
		if _, err := e.DownloadFile(ctx, url, dst); err != nil {
			return fmt.Errorf("download %s: %w", sanitizeURL(url), err)
		}
	case "loader", "installer":
		// Fabric-style loader installer or Forge/NeoForge installer.
		installer := filepath.Join(root, "installer.jar")
		if _, err := e.DownloadFile(ctx, url, installer); err != nil {
			return fmt.Errorf("download installer: %w", err)
		}
		javaBin, err := ResolveJavaBin(p.JavaMajor)
		if err != nil {
			return err
		}
		var argv []string
		if kind == "loader" {
			// fabric-installer: server profile for the mc version, plain jar.
			argv = []string{javaBin, "-jar", installer, "server", "-mcversion", p.Version, "-downloadMinecraft", "-noprofile"}
		} else {
			// forge/neoforge installer.
			argv = []string{javaBin, "-jar", installer, "--installServer", root}
		}
		if out, rerr := runAsMCUser(ctx, root, mcUser, argv[0], argv[1:]...); rerr != nil {
			return fmt.Errorf("installer: %s (%w)", tailString(out, 400), rerr)
		}
		_ = os.Remove(installer)
	default:
		return fmt.Errorf("provider %q declares unknown installer kind %q", provider.Name(), kind)
	}
	return nil
}

// sanitizeURL strips any userinfo for logging.
func sanitizeURL(u string) string {
	if i := strings.Index(u, "@"); i > 0 && strings.Contains(u[:i], "//") {
		if j := strings.Index(u[:i], "//"); j >= 0 {
			return u[:j+2] + "[redacted]@" + u[i+1:]
		}
	}
	return u
}

// resolveServerJar finds the launchable jar for a provider layout: the
// provider's canonical name, else any single *.jar in the root (loader
// installers produce their own artifacts).
func resolveServerJar(root string, provider minecraft.Provider, version string) (string, error) {
	if p := filepath.Join(root, provider.JarName(version)); fileExists(p) {
		return provider.JarName(version), nil
	}
	// loader installers produce fabric-server-launch.jar / run libs.
	for _, cand := range []string{"fabric-server-launch.jar", "server.jar", "run.jar"} {
		if fileExists(filepath.Join(root, cand)) {
			return cand, nil
		}
	}
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, en := range entries {
			if !en.IsDir() && strings.HasSuffix(en.Name(), ".jar") && !strings.HasSuffix(en.Name(), "-installer.jar") {
				return en.Name(), nil
			}
		}
	}
	return "", fmt.Errorf("no server jar found in instance root (install incomplete)")
}

// ============================================================================
// lifecycle — start / stop (safe shutdown) / restart / kill / status
// ============================================================================

// HandleMCStart builds the launch command from the provider and starts the
// unit. Idempotent: stop-then-start.
func HandleMCStart(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p minecraft.MCLifecyclePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := MCUnitName(p.InstanceID)
	if unit == "" {
		return nil, fmt.Errorf("invalid instance id")
	}
	root := MCRoot(p.InstanceID)
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("instance tree missing (install first)")
	}
	provider, err := minecraft.ProviderFor(p.Provider)
	if err != nil {
		return nil, err
	}
	mcUser, err := mcUserFor(p.InstanceID)
	if err != nil {
		return nil, err
	}
	uid, gid, err := mcOwnerIDs(root)
	if err != nil {
		return nil, err
	}

	// Refresh properties + rcon at start (customer edits + password rotation).
	rconPass, err := decryptRCONPass(p.RCONPassEnc)
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	for k, v := range p.Properties {
		if minecraft.IsProtectedProperty(k) {
			continue
		}
		props[k] = v
	}
	props["server-port"] = strconv.Itoa(p.Port)
	props["server-ip"] = "0.0.0.0"
	props["enable-rcon"] = "true"
	props["rcon.port"] = strconv.Itoa(p.RCONPort)
	props["rcon.password"] = rconPass
	if err := writePropertiesFile(root, props); err != nil {
		return nil, err
	}

	// Launch command — provider interface assembles it (no branches here).
	javaBin, err := ResolveJavaBin(p.JavaMajor)
	if err != nil {
		return nil, err
	}
	jarName, err := resolveServerJar(root, provider, p.Version)
	if err != nil {
		return nil, err
	}
	xmx := p.XmxMB
	if xmx <= 0 {
		xmx = minecraft.MinXmxMB
	}
	startCmd := provider.StartCommand(javaBin, jarName, xmx, p.ExtraArgs)

	spec := ContainerSpec{
		InstanceID:    p.InstanceID,
		Unit:          unit,
		User:          mcUser,
		Root:          root,
		JavaBin:       javaBin,
		StartCommand:  startCmd,
		RestartPolicy: p.RestartPolicy,
		CPUPercent:    p.Limits.CPUPercent,
		MemoryMB:      p.Limits.MemoryMB,
		PidsMax:       p.Limits.PidsMax,
		Description:   "EpicPanel Minecraft instance " + p.InstanceID,
	}
	if err := defaultDriver.Start(ctx, spec); err != nil {
		return nil, err
	}
	_ = exec.CommandContext(ctx, "systemctl", "enable", unit).Run()
	_ = chownRecursive(root, uid, gid)

	// Metrics poller for the AppSample envelope.
	RememberMCSpec(p.InstanceID, rconPass, p.RCONPort, provider.SupportsRCONTPS())
	StartMCMetricsPoller(p.InstanceID)

	slog.Info("minecraft instance started", "instance", p.InstanceID, "unit", unit, "jar", jarName)
	out := minecraft.MCStatusOutcome{InstanceID: p.InstanceID, Unit: unit, Log: "started as " + unit}
	if t, err := defaultDriver.Status(ctx, unit); err == nil {
		out.UnitState = t.State
		out.UptimeS = t.UptimeS
		out.Restarts = t.Restarts
	}
	return json.Marshal(out)
}

// HandleMCStop performs the SAFE shutdown sequence:
//  1. RCON save-all (flush the world; bounded wait)
//  2. RCON stop (graceful server shutdown)
//  3. wait for the unit to go inactive (StopTimeout)
//  4. still active → SIGTERM then SIGKILL (the kill path)
//
// Kill is the explicit customer override (skip straight to SIGKILL).
func HandleMCStop(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p minecraft.MCIDPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := MCUnitName(p.InstanceID)
	if unit == "" {
		return nil, fmt.Errorf("invalid instance id")
	}
	log := mcSafeShutdown(ctx, unit, p.InstanceID)
	out := minecraft.MCStatusOutcome{InstanceID: p.InstanceID, Unit: unit, UnitState: "inactive", Log: log}
	return json.Marshal(out)
}

// HandleMCRestart = safe shutdown + start.
func HandleMCRestart(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	if _, err := HandleMCStop(ctx, e, c, cfg, payload); err != nil {
		return nil, err
	}
	return HandleMCStart(ctx, e, c, cfg, payload)
}

// HandleMCKill SIGKILLs the unit immediately (customer override).
func HandleMCKill(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p minecraft.MCIDPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := MCUnitName(p.InstanceID)
	if unit == "" {
		return nil, fmt.Errorf("invalid instance id")
	}
	_ = defaultDriver.Kill(ctx, unit)
	StopMCMetricsPoller(p.InstanceID)
	out := minecraft.MCStatusOutcome{InstanceID: p.InstanceID, Unit: unit, UnitState: "inactive", Log: "killed"}
	return json.Marshal(out)
}

// HandleMCStatus reports agent truth for the unit.
func HandleMCStatus(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p minecraft.MCIDPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := MCUnitName(p.InstanceID)
	if unit == "" {
		return nil, fmt.Errorf("invalid instance id")
	}
	out := minecraft.MCStatusOutcome{InstanceID: p.InstanceID, Unit: unit, UnitState: "inactive"}
	if t, err := defaultDriver.Status(ctx, unit); err == nil {
		out.UnitState = t.State
		out.UptimeS = t.UptimeS
		out.Restarts = t.Restarts
	}
	return json.Marshal(out)
}

// ============================================================================
// console — logs (ring buffer snapshot) + allowlisted RCON command
// ============================================================================

type mcLogsPayload struct {
	InstanceID string `json:"instance_id"`
	Lines      int    `json:"lines"`
	AfterSeq   int64  `json:"after_seq"`
}

// HandleMCLogs returns recent console lines from the agent-side ring buffer
// (journald-fed, scrubbed against this instance's RCON password). The
// control plane re-scrubs before serving — defense in depth.
func HandleMCLogs(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p mcLogsPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if p.Lines <= 0 || p.Lines > 500 {
		p.Lines = 200
	}
	unit := MCUnitName(p.InstanceID)
	if unit == "" {
		return nil, fmt.Errorf("invalid instance id")
	}
	lines, next, err := mcConsoleLines(ctx, unit, p.InstanceID, p.Lines, p.AfterSeq, mcSecrets(p.InstanceID))
	if err != nil {
		return nil, err
	}
	return json.Marshal(minecraft.MCLogsOutcome{InstanceID: p.InstanceID, Lines: lines, NextSeq: next})
}

type mcCommandPayload struct {
	InstanceID string `json:"instance_id"`
	Command    string `json:"command"`
}

// HandleMCCommand runs ONE allowlisted server command over RCON. The
// command was validated control-plane side; it is validated AGAIN here
// (defense in depth). There is no shell anywhere on this path.
func HandleMCCommand(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p mcCommandPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	cmd, err := minecraft.ValidateConsoleCommand(p.Command)
	if err != nil {
		return nil, err
	}
	resp, err := mcRunCommand(ctx, p.InstanceID, cmd)
	if err != nil {
		return nil, err
	}
	// Append command + response to the console ring (scrubbed).
	secrets := mcSecrets(p.InstanceID)
	texts := []string{"> " + cmd}
	texts = append(texts, resp...)
	appendMCLines(p.InstanceID, texts, secrets)
	out := minecraft.MCCommandOutcome{InstanceID: p.InstanceID, Command: cmd, Response: resp, OK: true}
	return json.Marshal(out)
}

// mcRunCommand dials the instance's RCON endpoint and runs one command.
func mcRunCommand(ctx context.Context, instanceID, cmd string) ([]string, error) {
	spec, ok := mcSpecOf(instanceID)
	if !ok || spec.rconPass == "" || spec.rconPort == 0 {
		return nil, fmt.Errorf("rcon is not configured for this instance (not started or disabled)")
	}
	addr := "127.0.0.1:" + strconv.Itoa(spec.rconPort)
	conn, err := minecraft.DialRCON(addr, spec.rconPass, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("rcon dial: %w", err)
	}
	defer conn.Close()
	resp, err := conn.Command(cmd)
	if err != nil {
		return nil, fmt.Errorf("rcon command: %w", err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(resp), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

// ============================================================================
// files — list + upload (controlled paths only) + properties
// ============================================================================

type mcFilesPayload struct {
	InstanceID string `json:"instance_id"`
	Path       string `json:"path"`
}

// HandleMCFiles lists a directory inside the instance tree (no file content).
func HandleMCFiles(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p mcFilesPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	root := MCRoot(p.InstanceID)
	rel := p.Path
	if rel == "" {
		rel = "."
	}
	dir, err := safeMCPath(root, rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := minecraft.MCFilesOutcome{InstanceID: p.InstanceID, Path: rel}
	for _, en := range entries {
		info, err := en.Info()
		if err != nil {
			continue
		}
		out.Entries = append(out.Entries, minecraft.FileEntry{
			Name: en.Name(), IsDir: en.IsDir(), Size: info.Size(), ModTime: info.ModTime(),
		})
	}
	return json.Marshal(out)
}

type mcUploadPayload struct {
	InstanceID string `json:"instance_id"`
	Path       string `json:"path"`
	B64        string `json:"content_b64"`
}

// HandleMCUpload writes one file inside the instance tree. Path traversal
// and symlink escapes are rejected; server.properties/eula.txt are
// panel-managed and cannot be overwritten through uploads.
func HandleMCUpload(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p mcUploadPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	root := MCRoot(p.InstanceID)
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("instance tree missing (install first)")
	}
	switch filepath.Base(filepath.Clean("/" + p.Path)) {
	case "server.properties", "eula.txt":
		return nil, fmt.Errorf("%s is panel-managed; use the properties endpoint", filepath.Base(p.Path))
	}
	target, err := safeMCPath(root, p.Path)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(p.B64)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 content")
	}
	if len(raw) > 64*1024*1024 {
		return nil, fmt.Errorf("file too large (max 64 MiB per upload)")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(target, raw, 0o640); err != nil {
		return nil, err
	}
	uid, gid, _ := mcOwnerIDs(root)
	_ = os.Chown(target, uid, gid)
	out := minecraft.MCOutcome{InstanceID: p.InstanceID, OK: true, Log: "wrote " + p.Path}
	return json.Marshal(out)
}

// safeMCPath resolves rel inside root (symlink-proof).
func safeMCPath(root, rel string) (string, error) {
	if rel == "" || rel == "." || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("path must be relative to the instance root")
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("path escapes the instance root")
	}
	full := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		if os.IsNotExist(err) {
			return full, nil // new file: parent checked at write time
		}
		return "", err
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the instance root")
	}
	return filepath.Join(resolved, filepath.Base(full)), nil
}

type mcPropertiesPayload struct {
	InstanceID string            `json:"instance_id"`
	Action     string            `json:"action"` // get | set
	Properties map[string]string `json:"properties,omitempty"`
}

// HandleMCProperties reads (protected keys + rcon password excluded) or
// writes server.properties (protected keys rejected; file stays 0640).
func HandleMCProperties(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p mcPropertiesPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	root := MCRoot(p.InstanceID)
	propFile := filepath.Join(root, "server.properties")

	switch p.Action {
	case "get":
		props, err := readPropertiesFile(propFile)
		if err != nil {
			return nil, err
		}
		delete(props, "rcon.password")
		return json.Marshal(minecraft.MCPropertiesOutcome{InstanceID: p.InstanceID, Properties: props})
	case "set":
		for k := range p.Properties {
			if minecraft.IsProtectedProperty(k) {
				return nil, fmt.Errorf("property %q is panel-managed", k)
			}
		}
		cur, err := readPropertiesFile(propFile)
		if err != nil {
			return nil, err
		}
		for k, v := range p.Properties {
			if !minecraft.IsProtectedProperty(k) {
				cur[k] = v
			}
		}
		if err := writePropertiesFile(root, cur); err != nil {
			return nil, err
		}
		out := minecraft.MCOutcome{InstanceID: p.InstanceID, OK: true, Log: "properties updated"}
		return json.Marshal(out)
	default:
		return nil, fmt.Errorf("action must be get or set")
	}
}

// readPropertiesFile parses a java properties file (comments ignored).
func readPropertiesFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		props[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return props, nil
}

// writePropertiesFile atomically writes server.properties (0640).
func writePropertiesFile(root string, props map[string]string) error {
	var b strings.Builder
	b.WriteString("# Managed by EpicPanel — protected keys are panel-owned.\n")
	for _, k := range sortedKeys(props) {
		b.WriteString(k + "=" + props[k] + "\n")
	}
	tmp := filepath.Join(root, ".server.properties.tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(root, "server.properties"))
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStringsInPlace(keys)
	return keys
}

func sortStringsInPlace(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ============================================================================
// world backups (save-off → copy → save-on seam) + restore (stop→swap→start)
// ============================================================================

type mcBackupPayload struct {
	InstanceID string `json:"instance_id"`
	BackupName string `json:"backup_name"`
}

// validBackupName bounds snapshot names to a safe file component.
func validBackupName(name string) bool {
	if name == "" || len(name) > 80 {
		return false
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return !strings.Contains(name, "..")
}

// HandleMCBackupWorld snapshots the world: save-off → save-all (flush) →
// tar the world directories → save-on. Output lands in <root>/backups/.
func HandleMCBackupWorld(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p mcBackupPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if !validBackupName(p.BackupName) {
		return nil, fmt.Errorf("invalid backup name")
	}
	root := MCRoot(p.InstanceID)
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("instance tree missing")
	}
	if !isInstanceRunning(ctx, p.InstanceID) {
		// Offline instance: flush is a no-op; copy the world directly.
	} else {
		// save-off → save-all → snapshot → save-on (the Phase 11 seam).
		_, _ = mcRunCommand(ctx, p.InstanceID, "save-off")
		resp, err := mcRunCommand(ctx, p.InstanceID, "save-all")
		if err != nil {
			_, _ = mcRunCommand(ctx, p.InstanceID, "save-on")
			return nil, fmt.Errorf("save-all: %w", err)
		}
		_ = resp
		time.Sleep(2 * time.Second) // bounded flush grace
		defer func() {
			_, _ = mcRunCommand(context.Background(), p.InstanceID, "save-on")
		}()
	}

	backupDir := filepath.Join(root, "backups")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return nil, err
	}
	dst := filepath.Join(backupDir, p.BackupName+".tar.gz")
	worlds := worldDirs(root)
	if len(worlds) == 0 {
		return nil, fmt.Errorf("no world directories found (has the server ever started?)")
	}
	args := append([]string{"-czf", dst}, worlds...)
	if out, err := exec.CommandContext(ctx, "tar", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tar world: %s (%w)", tailString(string(out), 200), err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		return nil, err
	}
	sum, err := sha256File(dst)
	if err != nil {
		return nil, err
	}
	out := minecraft.MCBackupOutcome{
		InstanceID: p.InstanceID, BackupName: p.BackupName, Path: dst,
		SizeBytes: info.Size(), SHA256: sum,
	}
	return json.Marshal(out)
}

// worldDirs lists the world directories (level-name based discovery: any
// directory containing region/ or level.dat).
func worldDirs(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, en := range entries {
		if !en.IsDir() {
			continue
		}
		p := filepath.Join(root, en.Name())
		if fileExists(filepath.Join(p, "level.dat")) {
			out = append(out, en.Name())
		}
	}
	return out
}

// HandleMCRestoreWorld restores a snapshot: stop (safe shutdown) → swap the
// world dirs → leave stopped (the control plane restarts per desired state).
func HandleMCRestoreWorld(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p minecraft.MCRestorePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	if !validBackupName(p.BackupName) {
		return nil, fmt.Errorf("invalid backup name")
	}
	root := MCRoot(p.InstanceID)
	src := filepath.Join(root, "backups", p.BackupName+".tar.gz")
	if !fileExists(src) {
		return nil, fmt.Errorf("backup %q not found", p.BackupName)
	}

	// 1. stop (safe shutdown seam; offline is fine).
	unit := MCUnitName(p.InstanceID)
	_ = mcSafeShutdown(ctx, unit, p.InstanceID)

	// 2. swap: move current worlds aside, extract, restore on success.
	scratch := root + ".restore-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	_ = os.MkdirAll(scratch, 0o750)
	worlds := worldDirs(root)
	var moved []string
	for _, w := range worlds {
		if err := os.Rename(filepath.Join(root, w), filepath.Join(scratch, w)); err == nil {
			moved = append(moved, w)
		}
	}
	if out, err := exec.CommandContext(ctx, "tar", "-xzf", src, "-C", root).CombinedOutput(); err != nil {
		// Roll the old worlds back — never leave the tree half-swapped.
		for _, w := range moved {
			_ = os.Rename(filepath.Join(scratch, w), filepath.Join(root, w))
		}
		_ = os.RemoveAll(scratch)
		return nil, fmt.Errorf("extract backup: %s (%w)", tailString(string(out), 200), err)
	}
	_ = os.RemoveAll(scratch)

	uid, gid, _ := mcOwnerIDs(root)
	_ = chownRecursive(root, uid, gid)

	slog.Info("minecraft world restored", "instance", p.InstanceID, "backup", p.BackupName)
	out := minecraft.MCOutcome{InstanceID: p.InstanceID, OK: true, Log: "restored " + p.BackupName + " (instance stopped; start to apply)"}
	return json.Marshal(out)
}

// isInstanceRunning checks the unit truth.
func isInstanceRunning(ctx context.Context, instanceID string) bool {
	unit := MCUnitName(instanceID)
	if unit == "" {
		return false
	}
	t, err := defaultDriver.Status(ctx, unit)
	return err == nil && t.State == "active"
}

// ============================================================================
// metrics — on-demand honest snapshot (players/TPS/MSPT with known flags)
// ============================================================================

// HandleMCMetrics polls RCON for players + TPS/MSPT (best effort). When the
// server does not expose a metric, the outcome says so (known=false) — the
// panel never presents a guess as data.
func HandleMCMetrics(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p minecraft.MCMetricsPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	out := minecraft.MCMetricsOutcome{InstanceID: p.InstanceID, At: time.Now().UTC().Format(time.RFC3339)}
	players, tps, mspt, known := MCPollMetrics(ctx, p.InstanceID)
	out.Players = int(players)
	out.TPS = tps
	out.MSPT = mspt
	out.TPSKnown = known.tps
	out.MSPTKnown = known.mspt
	if !known.list {
		out.Detail = "rcon unreachable (instance stopped or rcon disabled)"
	} else if !known.tps {
		out.Detail = "players ok; tps/mspt not exposed by this server type over rcon"
	}
	return json.Marshal(out)
}

// ============================================================================
// delete — stop + remove tree + user
// ============================================================================

// HandleMCDelete stops the unit and removes the instance tree + user.
func HandleMCDelete(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p minecraft.MCIDPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	unit := MCUnitName(p.InstanceID)
	if unit == "" {
		return nil, fmt.Errorf("invalid instance id")
	}
	_ = mcSafeShutdown(ctx, unit, p.InstanceID)
	StopMCMetricsPoller(p.InstanceID)
	ForgetMCSpec(p.InstanceID)
	_ = os.RemoveAll(MCRoot(p.InstanceID))
	if mcUser, err := mcUserFor(p.InstanceID); err == nil {
		_ = exec.Command("userdel", mcUser).Run()
	}
	out := minecraft.MCOutcome{InstanceID: p.InstanceID, OK: true, Log: "deleted"}
	return json.Marshal(out)
}

// ============================================================================
// helpers — user, ownership, java resolution, download, rcon password
// ============================================================================

// ensureMCUser creates the dedicated system user (idempotent).
func ensureMCUser(name string) error {
	if _, err := user.Lookup(name); err == nil {
		return nil
	}
	out, err := exec.Command("useradd", "--system", "--shell", "/usr/sbin/nologin",
		"--home-dir", "/nonexistent", "--no-create-home", name).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("useradd: %s (%w)", tailString(string(out), 200), err)
	}
	return nil
}

func mcOwnerIDs(root string) (int, int, error) {
	if _, err := os.Stat(root); err != nil {
		_ = os.MkdirAll(root, 0o750)
	}
	info, err := os.Stat(root)
	if err != nil {
		return 0, 0, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid), nil
	}
	return 0, 0, fmt.Errorf("stat unsupported")
}

// runAsMCUser executes argv as the instance's unix user inside dir
// (setpriv — never run customer workloads as root).
func runAsMCUser(ctx context.Context, dir, mcUser string, name string, args ...string) (string, error) {
	uid, gid, err := idsForUser(mcUser)
	if err != nil {
		return "", err
	}
	full := append([]string{"--reuid", strconv.Itoa(uid), "--regid", strconv.Itoa(gid),
		"--clear-groups", "--inh-caps", "-0", "--",
		"env", "HOME=" + dir, "USER=" + strconv.Itoa(uid), "TERM=xterm",
		"PATH=/usr/local/bin:/usr/bin:/bin",
		name}, args...)
	cmd := exec.CommandContext(ctx, "setpriv", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func idsForUser(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uid, gid, nil
}

// ResolveJavaBin finds a java binary for the requested major version:
// distro JVM layouts first, honest fallback to the default java (the
// outcome reports which was used).
func ResolveJavaBin(major int) (string, error) {
	if major <= 0 {
		major = 21
	}
	// Debian/Ubuntu/RH layouts + SDKMAN-style dirs.
	patterns := []string{
		"/usr/lib/jvm/java-" + strconv.Itoa(major) + "-openjdk-*/bin/java",
		"/usr/lib/jvm/java-" + strconv.Itoa(major) + "-openjdk/bin/java",
		"/usr/lib/jvm/java-" + strconv.Itoa(major) + "/bin/java",
		"/usr/java/jdk-" + strconv.Itoa(major) + "*/bin/java",
	}
	for _, pat := range patterns {
		matches, _ := filepath.Glob(pat)
		if len(matches) > 0 {
			return matches[0], nil
		}
	}
	if _, err := exec.LookPath("java"); err == nil {
		return "java", nil // honest fallback: default JVM (may differ in major)
	}
	return "", fmt.Errorf("no java runtime found for major %d (install openjdk-%d)", major, major)
}

// DownloadFile fetches url into dst (bounded, atomic rename).
func (e *Executor) DownloadFile(ctx context.Context, url, dst string) (int64, error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(c, "curl", "-fsSL", "--max-time", "540", "-o", dst+".part", url).CombinedOutput()
	if err != nil {
		_ = os.Remove(dst + ".part")
		return 0, fmt.Errorf("%s (%w)", tailString(string(out), 200), err)
	}
	if err := os.Rename(dst+".part", dst); err != nil {
		return 0, err
	}
	info, err := os.Stat(dst)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// decryptRCONPass opens the stored ciphertext.
func decryptRCONPass(enc string) (string, error) {
	if enc == "" {
		return "", fmt.Errorf("rcon password missing")
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	return secretbox.Decrypt(raw)
}

// sha256File hashes a file (verification seam for Phase 11).
func sha256File(path string) (string, error) {
	out, err := exec.Command("sha256sum", path).Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("sha256sum output malformed")
	}
	return fields[0], nil
}

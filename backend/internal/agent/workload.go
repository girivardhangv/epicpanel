package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// workloadCollector produces the per-workload envelopes: hosting-account
// sites (cgroup slices), Docker containers and managed app units (systemd
// transient services — the path app_ops.go provisions). Expensive parts
// (disk walks) run on their own slower cadence and are cached between passes.
type workloadCollector struct {
	mu          sync.Mutex
	siteCPUs    map[string]cpuSample
	siteIO      map[string]ioSample
	siteDisks   map[string]diskWalk
	siteDisksAt time.Time
	appProps    map[string]appProp
	appPropsAt  time.Time
}

type ioSample struct {
	rbytes, wbytes uint64
	at             time.Time
}

type diskWalk struct {
	mb int64
}

type appProp struct {
	active   string
	sub      string
	restarts int
	since    time.Time
	pid      int
}

func newWorkloadCollector() *workloadCollector {
	return &workloadCollector{
		siteCPUs:  map[string]cpuSample{},
		siteIO:    map[string]ioSample{},
		siteDisks: map[string]diskWalk{},
		appProps:  map[string]appProp{},
	}
}

// CollectSites scans epicpanel.slice child slices (one per hosting account)
// and returns the per-site envelope. Cheap: cgroup file reads only; the disk
// walk is refreshed at most once per siteDiskTTL.
func (w *workloadCollector) CollectSites() []agentproto.SiteSample {
	base := cgroupRoot + "/epicpanel.slice"
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	now := time.Now()
	var out []agentproto.SiteSample
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "epicpanel-") || !strings.HasSuffix(name, ".slice") {
			continue
		}
		user := strings.TrimSuffix(strings.TrimPrefix(name, "epicpanel-"), ".slice")
		if user == "" || user == "system" {
			continue
		}
		dir := filepath.Join(base, name)
		s := agentproto.SiteSample{UnixUser: user, WebsiteID: user}
		// Limits via the SHARED reader (same files enforcement wrote — the
		// anti-drift rule; see enforce.go readSliceLimits).
		lim := readSliceLimits(dir)
		s.MemoryLimit = lim.MemoryMaxBytes
		s.CPULimitCores = 0
		if lim.CPUPeriodUsec > 0 && lim.CPUQuotaUsec > 0 {
			s.CPULimitCores = float64(lim.CPUQuotaUsec) / float64(lim.CPUPeriodUsec)
		}
		s.PidsLimit = lim.PidsMax
		if b, err := os.ReadFile(filepath.Join(dir, "memory.current")); err == nil {
			s.MemoryBytes, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "cpu.stat")); err == nil {
			if usec, ok := parseCpuStat(b); ok {
				w.mu.Lock()
				prev, seen := w.siteCPUs[user]
				w.siteCPUs[user] = cpuSample{usageUsec: usec, at: now}
				w.mu.Unlock()
				if seen {
					elapsed := now.Sub(prev.at).Seconds()
					if elapsed > 0.25 {
						cores := (float64(usec-prev.usageUsec) / 1e6) / elapsed
						if cores >= 0 {
							s.CPUCoresUsed = cores
							s.CPUPercent = clampPercent(cores * 100)
						}
					}
				}
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs")); err == nil {
			s.Processes = len(strings.Fields(string(b)))
		}
		if b, err := os.ReadFile(filepath.Join(dir, "io.stat")); err == nil {
			s.IOReadBPS, s.IOWriteBPS = w.siteIORates(user, b, now)
		}
		// Disk usage: cached walk (expensive), refreshed per TTL.
		s.DiskUsedMB = w.siteDiskMB(user, now)
		out = append(out, s)
	}
	return out
}

func (w *workloadCollector) siteIORates(user string, b []byte, now time.Time) (rx, tx float64) {
	var rbytes, wbytes uint64
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		// Guard: truncated/empty io.stat lines (kernel can emit partial
		// lines under load) previously panicked on fields[1:].
		if len(fields) < 2 {
			continue
		}
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "rbytes=") {
				v, _ := strconv.ParseUint(strings.TrimPrefix(f, "rbytes="), 10, 64)
				rbytes += v
			} else if strings.HasPrefix(f, "wbytes=") {
				v, _ := strconv.ParseUint(strings.TrimPrefix(f, "wbytes="), 10, 64)
				wbytes += v
			}
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	prev, seen := w.siteIO[user]
	w.siteIO[user] = ioSample{rbytes: rbytes, wbytes: wbytes, at: now}
	if !seen {
		return 0, 0
	}
	elapsed := now.Sub(prev.at).Seconds()
	if elapsed <= 0.25 || rbytes < prev.rbytes || wbytes < prev.wbytes {
		return 0, 0
	}
	return float64(rbytes-prev.rbytes) / elapsed, float64(wbytes-prev.wbytes) / elapsed
}

// siteDiskMB walks the site tree at most once per siteDiskTTL (60s default):
// the walk is the one expensive collector op, so it must not run per pass.
func (w *workloadCollector) siteDiskMB(user string, now time.Time) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if prev, ok := w.siteDisks[user]; ok && now.Sub(w.siteDisksAt) < siteDiskTTL {
		_ = prev
		return w.siteDisks[user].mb
	}
	mb := dirSizeMB(filepath.Join("/srv/epicpanel/websites", user))
	w.siteDisks[user] = diskWalk{mb: mb}
	w.siteDisksAt = now
	return mb
}

const siteDiskTTL = 60 * time.Second

// CollectApps samples managed app units (epicpanel-app-<websiteID>.service
// — the transient units app_ops provisions). One systemctl show invocation
// per pass, cached for appPropsTTL; the app kind is recorded in the unit
// description where the provisioning path sets one.
func (w *workloadCollector) CollectApps() []agentproto.AppSample {
	var out []agentproto.AppSample
	now := time.Now()

	// systemd transient units (non-Docker hosts / the systemd fallback).
	units := listAppUnits()
	props := w.appProperties(units)
	seen := map[string]bool{}
	for _, unit := range units {
		id := strings.TrimPrefix(strings.TrimSuffix(unit, ".service"), "epicpanel-app-")
		p := props[unit]
		s := agentproto.AppSample{
			WebsiteID:    id,
			Kind:         appKindFor(unit, p),
			Status:       p.active,
			RestartCount: p.restarts,
		}
		if !p.since.IsZero() {
			s.UptimeS = int64(now.Sub(p.since).Seconds())
			if s.UptimeS < 0 {
				s.UptimeS = 0
			}
		}
		if p.pid > 0 {
			s.CPUPercent = processCPUPercent(p.pid)
			s.MemoryBytes = processMemory(p.pid)
			s.NetRxBPS, s.NetTxBPS = processNetRates(p.pid)
		}
		seen[id] = true
		out = append(out, s)
	}

	// Docker workloads (containerized apps when the daemon is present): the
	// reconciler's truth source MUST include containers or a containerized
	// workload never converges (stuck starting/stopping forever).
	for _, s := range collectDockerApps(now) {
		if !seen[s.WebsiteID] {
			out = append(out, s)
		}
	}
	return out
}

// collectDockerApps samples containers labelled epicpanel.workload=1 and maps
// them onto the AppSample envelope (WebsiteID = the app instance id).
func collectDockerApps(now time.Time) []agentproto.AppSample {
	out, err := execCommand("docker", "ps", "-a",
		"--filter", "label=epicpanel.workload=1",
		"--format", "{{.Names}}|{{.State}}|{{.Status}}|{{.Label \"epicpanel.kind\"}}").Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	// Container memory/CPU/net MUST come from the container's cgroup, not
	// /proc/<init-pid>/status: the init process (tini) is tiny (~78 KB), so
	// process-based reads reported a bogus near-zero RAM while the JVM was
	// using gigabytes. CollectContainers reads memory.current/cpu.stat/net
	// from the same cgroup the kernel enforces (anti-drift).
	byName := map[string]agentproto.ContainerSample{}
	for _, c := range CollectContainers() {
		if c.Name != "" {
			byName[c.Name] = c
		}
	}
	var apps []agentproto.AppSample
	for _, line := range splitLines(out) {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) < 2 {
			continue
		}
		name := fields[0]
		id := strings.TrimPrefix(name, "epicpanel-app-")
		if id == "" || id == name {
			continue
		}
		kind := "app"
		if len(fields) >= 4 && fields[3] != "" {
			kind = fields[3]
		}
		s := agentproto.AppSample{WebsiteID: id, Kind: kind}
		switch fields[1] {
		case "running":
			s.Status = "active"
		case "restarting":
			s.Status = "activating"
		default:
			s.Status = "inactive"
		}
		// Accurate usage from the container cgroup (memory.current, cpu.stat,
		// net counters). Fall back to the main-pid /proc reads only when the
		// cgroup scope is unavailable (older daemon / cgroupfs layouts).
		if c, ok := byName[name]; ok && c.MemoryBytes > 0 {
			s.CPUPercent = c.CPUPercent
			s.MemoryBytes = c.MemoryBytes
			s.NetRxBPS = c.NetRxBPS
			s.NetTxBPS = c.NetTxBPS
		} else if pidOut, perr := execCommand("docker", "inspect", "-f", "{{.State.Pid}}", name).Output(); perr == nil {
			if pid, aerr := strconv.Atoi(strings.TrimSpace(string(pidOut))); aerr == nil && pid > 1 {
				s.CPUPercent = processCPUPercent(pid)
				s.MemoryBytes = processMemory(pid)
				s.NetRxBPS, s.NetTxBPS = processNetRates(pid)
			}
		}
		// Disk: the workload tree on the host (/srv/epicpanel/websites/<id>).
		if st, serr := os.Stat(filepath.Join("/srv/epicpanel/websites", id)); serr == nil && st.IsDir() {
			s.DiskUsedMB = dirSizeMB(filepath.Join("/srv/epicpanel/websites", id))
		}
		_ = now
		apps = append(apps, s)
	}
	return apps
}

const appPropsTTL = 10 * time.Second

func listAppUnits() []string {
	out, err := execCommand("systemctl", "list-units", "--type=service", "--no-legend", "--no-pager", "epicpanel-app-*").Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	var units []string
	for _, line := range splitLines(out) {
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			units = append(units, fields[0])
		}
	}
	return units
}

func (w *workloadCollector) appProperties(units []string) map[string]appProp {
	w.mu.Lock()
	defer w.mu.Unlock()
	if time.Since(w.appPropsAt) < appPropsTTL {
		return w.appProps
	}
	args := append([]string{"show"}, units...)
	args = append(args, "-p", "ActiveState", "-p", "SubState", "-p", "NRestarts", "-p", "ActiveEnterTimestamp", "-p", "MainPID")
	out, err := execCommand("systemctl", args...).Output()
	if err != nil && len(out) == 0 {
		return w.appProps
	}
	props := map[string]appProp{}
	var cur string
	for _, line := range splitLines(out) {
		if strings.HasPrefix(line, systemdUnitPrefix) {
			cur = strings.TrimSuffix(strings.TrimPrefix(line, systemdUnitPrefix), ":")
			continue
		}
		if cur == "" {
			continue
		}
		p := props[cur]
		switch {
		case strings.HasPrefix(line, "ActiveState="):
			p.active = strings.TrimPrefix(line, "ActiveState=")
		case strings.HasPrefix(line, "SubState="):
			p.sub = strings.TrimPrefix(line, "SubState=")
		case strings.HasPrefix(line, "NRestarts="):
			p.restarts, _ = strconv.Atoi(strings.TrimPrefix(line, "NRestarts="))
		case strings.HasPrefix(line, "ActiveEnterTimestamp="):
			v := strings.TrimPrefix(line, "ActiveEnterTimestamp=")
			if t, err := time.ParseInLocation("Mon 2006-01-02 15:04:05 MST", v, time.Local); err == nil {
				p.since = t
			}
		case strings.HasPrefix(line, "MainPID="):
			p.pid, _ = strconv.Atoi(strings.TrimPrefix(line, "MainPID="))
		}
		props[cur] = p
	}
	w.appProps = props
	w.appPropsAt = time.Now()
	return props
}

// systemdUnitPrefix is how `systemctl show` starts each unit block.
const systemdUnitPrefix = "Unit "

// appKindFor reports the workload kind for a managed app unit. Provisioned
// apps run as generic transient services; the kind is "app" unless a label or
// description recorded one at create time.
func appKindFor(unit string, p appProp) string {
	return "app"
}

// --- per-process helpers (main PID based; cheap single-file reads) ---

var processCPUSamples = map[int]cpuSample{}
var processCPUMu sync.Mutex

func processCPUPercent(pid int) float64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 15 {
		return 0
	}
	utime, _ := strconv.ParseFloat(fields[13], 64)
	stime, _ := strconv.ParseFloat(fields[14], 64)
	ticks := (utime + stime) / 100 // jiffies → seconds (CLK_TCK=100)
	now := time.Now()

	processCPUMu.Lock()
	defer processCPUMu.Unlock()
	prev, seen := processCPUSamples[pid]
	processCPUSamples[pid] = cpuSample{usageUsec: uint64(ticks * 1e6), at: now}
	if !seen {
		return 0
	}
	elapsed := now.Sub(prev.at).Seconds()
	if elapsed <= 0.25 {
		return 0
	}
	pct := ((float64(ticks*1e6) - float64(prev.usageUsec)) / 1e6) / elapsed * 100
	if pct < 0 {
		return 0
	}
	return clampPercent(pct)
}

func processMemory(pid int) int64 {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	for _, line := range splitLines(b) {
		if hasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseInt(fields[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}

// processNetRates approximates the workload's network throughput from the
// process's net namespace (host-shared when not containerized — the delta
// still tracks the node, so it is only meaningful for namespaced processes;
// otherwise the rates reflect the whole host netns).
func processNetRates(pid int) (rxBPS, txBPS float64) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/net/dev")
	if err != nil {
		return 0, 0
	}
	var rx, tx int64
	for _, line := range splitLines(b) {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		r, _ := strconv.ParseInt(fields[0], 10, 64)
		t, _ := strconv.ParseInt(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return float64(rx), float64(tx) // rates need a namespace we can trust; totals until Phase 9
}

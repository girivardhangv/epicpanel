//go:build linux

// Linux metrics collection. Everything reads kernel interfaces directly
// (/proc, statfs) or uses fixed-argument systemctl/service queries — no
// shell, no user-controlled input, no /proc assumptions in shared code.
package monitoring

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// LinuxCollector reads kernel counters and keeps per-cycle deltas so the
// panel receives instantaneous percentages without the agent computing rates.
type LinuxCollector struct {
	mu        sync.Mutex
	prevCPU   cpuTimes
	prevProcs map[int32]procTimes
	pageSize  int64
}

func NewCollector() (Collector, error) {
	return &LinuxCollector{
		prevProcs: map[int32]procTimes{},
		pageSize:  int64(os.Getpagesize()),
	}, nil
}

type procTimes struct {
	utime, stime, total uint64 // jiffies; total = global total at read time
}

// Collect gathers one snapshot. Any failing subsystem records an honest
// error in the payload and leaves its metric nil — values are never invented.
func (c *LinuxCollector) Collect(ctx context.Context) (*Sample, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	s := &Sample{
		Disks:     []DiskMetric{},
		Network:   []NetworkMetric{},
		Processes: []ProcessMetric{},
		Services:  []ServiceHealth{},
		Errors:    map[string]string{},
	}

	// CPU ------------------------------------------------------------------
	cur, err := readCPUTimes()
	if err != nil {
		s.Errors["cpu"] = "unavailable"
	} else if !c.prevCPU.valid {
		// First cycle: deltas are not yet computable; report unavailable
		// rather than a fabricated zero.
		s.Errors["cpu"] = "warming up"
	} else {
		dTotal := float64(cur.total - c.prevCPU.total)
		if dTotal > 0 {
			pct := func(field func(cpuTimes) uint64) *float64 {
				v := 100 * float64(field(cur)-field(c.prevCPU)) / dTotal
				return &v
			}
			idle := pct(func(t cpuTimes) uint64 { return t.idle + t.iowait })
			sys := pct(func(t cpuTimes) uint64 { return t.system + t.irq + t.softirq })
			user := pct(func(t cpuTimes) uint64 { return t.user + t.nice })
			usage := 100.0 - *idle
			if usage < 0 {
				usage = 0
			}
			s.CPUUsage, s.CPUUser, s.CPUSystem, s.CPUIdle = &usage, user, sys, idle
		}
	}
	c.prevCPU = cur

	if l1, l5, l15, ok := readLoadavg(); ok {
		s.Load1, s.Load5, s.Load15 = l1, l5, l15
	}

	// Memory ---------------------------------------------------------------
	// "used" = MemTotal − MemAvailable: page cache is reclaimable and is NOT
	// counted as used (documented contract, §3 of the spec).
	if mi, err := readMemInfo(); err == nil {
		if total, ok := mi["MemTotal"]; ok {
			avail := mi["MemAvailable"]
			free := mi["MemFree"]
			used := total - avail
			usage := 100 * float64(used) / float64(total)
			s.MemoryTotalBytes = &total
			s.MemoryAvailableBytes = &avail
			s.MemoryFreeBytes = &free
			s.MemoryUsedBytes = &used
			s.MemoryUsagePercent = &usage
		}
		if swapTotal, ok := mi["SwapTotal"]; ok && swapTotal > 0 {
			swapUsed := swapTotal - mi["SwapFree"]
			pct := 100 * float64(swapUsed) / float64(swapTotal)
			s.SwapTotalBytes = &swapTotal
			s.SwapUsedBytes = &swapUsed
			s.SwapUsagePercent = &pct
		}
	} else {
		s.Errors["memory"] = "unavailable"
	}

	// Uptime ---------------------------------------------------------------
	if up, err := readUptime(); err == nil {
		sec := int64(up)
		s.UptimeSeconds = &sec
	}

	// Disks ----------------------------------------------------------------
	disks, err := collectDisks()
	if err == nil {
		s.Disks = disks
	} else {
		s.Errors["disk"] = err.Error()
	}

	// Network --------------------------------------------------------------
	nets, err := readNetDev()
	if err == nil {
		s.Network = nets
	} else {
		s.Errors["network"] = err.Error()
	}

	// Processes ------------------------------------------------------------
	procs, err := c.collectProcesses()
	if err == nil {
		s.Processes = procs
	} else {
		s.Errors["processes"] = err.Error()
	}

	// Services -------------------------------------------------------------
	s.Services = collectServices()

	return s, nil
}

// ---------------------------------------------------------------------------
// /proc readers (pure functions where practical, unit-tested)
// ---------------------------------------------------------------------------

func readCPUTimes() (cpuTimes, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	t, ok := parseProcStatCPU(data)
	return t, mapErr(ok)
}

func mapErr(ok bool) error {
	if ok {
		return nil
	}
	return errProcFormat
}

var errProcFormat = errParse("unexpected /proc format")

type errParse string

func (e errParse) Error() string { return string(e) }

func readLoadavg() (*float64, *float64, *float64, bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, nil, nil, false
	}
	return parseLoadavg(data)
}

func readMemInfo() (map[string]int64, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	return parseMemInfo(data), nil
}

func readUptime() (float64, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	if v, ok := parseUptime(data); ok {
		return v, nil
	}
	return 0, errProcFormat
}

func collectDisks() ([]DiskMetric, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	out := []DiskMetric{}
	for _, m := range parseMounts(data) {
		dev, mount, fsType := m[0], m[1], m[2]
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue
		}
		total := int64(st.Blocks) * int64(st.Bsize)
		free := int64(st.Bavail) * int64(st.Bsize)
		used := total - free
		if total <= 0 {
			continue
		}
		usage := 100 * float64(used) / float64(total)
		out = append(out, DiskMetric{
			Mount: mount, Filesystem: fsType,
			TotalBytes: total, UsedBytes: used, FreeBytes: free,
			UsagePercent: usage,
		})
		_ = dev
	}
	return out, nil
}

func readNetDev() ([]NetworkMetric, error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	return parseNetDev(data), nil
}

type procSample struct {
	pid   int32
	name  string
	state string
	utime uint64
	stime uint64
	rss   uint64
}

func (c *LinuxCollector) collectProcesses() ([]ProcessMetric, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var samples []procSample
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.ParseInt(e.Name(), 10, 32)
		if err != nil {
			continue
		}
		if ps, ok := readProcStat(int32(pid)); ok {
			samples = append(samples, ps)
		}
	}

	// CPU percent requires two cycles of per-process jiffies (the first
	// cycle reports nil honestly).
	curTotal, err := readCPUTimes()
	if err != nil {
		return nil, err
	}
	dTotal := float64(curTotal.total - c.prevCPU.total)

	type scored struct {
		p    ProcessMetric
		cpu  float64
		mem  uint64
	}
	scoredProcs := make([]scored, 0, len(samples))
	newPrev := map[int32]procTimes{}
	for _, ps := range samples {
		cur := procTimes{utime: ps.utime, stime: ps.stime, total: curTotal.total}
		if prev, ok := c.prevProcs[ps.pid]; ok && dTotal > 0 {
			dProc := float64(cur.utime + cur.stime - prev.utime - prev.stime)
			if dProc < 0 {
				dProc = 0
			}
			pct := 100 * dProc / dTotal
			scoredProcs = append(scoredProcs, scored{
				p: ProcessMetric{
					Name: ps.name, PID: ps.pid, Status: mapState(ps.state),
					MemoryBytes: ps.rss * uint64(c.pageSize),
					CPUPercent:  &pct,
				},
				cpu: pct, mem: ps.rss,
			})
		} else {
			scoredProcs = append(scoredProcs, scored{
				p: ProcessMetric{
					Name: ps.name, PID: ps.pid, Status: mapState(ps.state),
					MemoryBytes: ps.rss * uint64(c.pageSize),
					CPUPercent:  nil,
				},
				mem: ps.rss,
			})
		}
		newPrev[ps.pid] = cur
		if len(newPrev) > 8192 { // bounded state; protects against PID churn
			break
		}
	}
	c.prevProcs = newPrev
	c.prevCPU = curTotal // refresh baseline shared with the CPU metric

	sort.Slice(scoredProcs, func(i, j int) bool { return scoredProcs[i].cpu > scoredProcs[j].cpu })
	topCPU := scoredProcs
	if len(topCPU) > TopProcesses/2 {
		topCPU = topCPU[:TopProcesses/2]
	}
	sort.Slice(scoredProcs, func(i, j int) bool { return scoredProcs[i].mem > scoredProcs[j].mem })
	topMem := scoredProcs
	if len(topMem) > TopProcesses/2 {
		topMem = topMem[:TopProcesses/2]
	}

	out := []ProcessMetric{}
	seen := map[int32]bool{}
	for _, s := range topCPU {
		out = append(out, s.p)
		seen[s.p.PID] = true
	}
	for _, s := range topMem {
		if !seen[s.p.PID] {
			out = append(out, s.p)
			seen[s.p.PID] = true
		}
		if len(out) >= TopProcesses {
			break
		}
	}
	runtime.Gosched() // yield after the /proc sweep; keeps hot loops polite
	return out, nil
}

func readProcStat(pid int32) (procSample, bool) {
	var ps procSample
	raw, err := os.ReadFile("/proc/" + strconv.FormatInt(int64(pid), 10) + "/stat")
	if err != nil {
		return ps, false
	}
	content := string(raw)
	// comm may contain spaces/parens: split after the last ')'.
	closing := strings.LastIndexByte(content, ')')
	if closing < 0 || len(content) <= closing+2 {
		return ps, false
	}
	ps.name = strings.Trim(content[1:closing], "()")
	rest := strings.Fields(content[closing+2:])
	if len(rest) < 20 {
		return ps, false
	}
	ps.state = rest[0]
	ps.utime, _ = strconv.ParseUint(rest[11], 10, 64) // field 14 overall
	ps.stime, _ = strconv.ParseUint(rest[12], 10, 64) // field 15 overall
	if statm, err := os.ReadFile("/proc/" + strconv.FormatInt(int64(pid), 10) + "/statm"); err == nil {
		fields := strings.Fields(string(statm))
		if len(fields) >= 2 {
			ps.rss, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return ps, true
}

// ---------------------------------------------------------------------------
// Service health (no systemd assumptions: systemctl → service → /proc)
// ---------------------------------------------------------------------------

func collectServices() []ServiceHealth {
	now := time.Now().UTC().Format(time.RFC3339)
	out := []ServiceHealth{}
	out = append(out, probeService("nginx", "Nginx", now))
	for _, v := range phpFPMVersions() {
		out = append(out, probeService("php"+v+"-fpm", "PHP "+v+" (php-fpm)", now))
	}
	return out
}

func probeService(name, display, now string) ServiceHealth {
	svc := ServiceHealth{
		Name: name, DisplayName: display,
		Status: ServiceUnknown, Running: false, Enabled: nil,
		LastChecked: now,
	}
	if state, err := systemctlIsActive(name); err == nil {
		switch state {
		case "active":
			svc.Status, svc.Running = ServiceRunning, true
		case "failed":
			svc.Status = ServiceFailed
		case "inactive":
			svc.Status = ServiceStopped
		}
		if enabled, ok := systemctlIsEnabled(name); ok {
			svc.Enabled = &enabled
		}
		return svc
	}
	if state, ok := serviceScriptStatus(name); ok {
		switch {
		case strings.Contains(state, "running"):
			svc.Status, svc.Running = ServiceRunning, true
		case strings.Contains(state, "stopped"):
			svc.Status = ServiceStopped
		default:
			svc.Status = ServiceUnknown
		}
		return svc
	}
	// No service manager: fall back to a direct process probe.
	if processExists(name) {
		svc.Status, svc.Running = ServiceRunning, true
	} else {
		svc.Status = ServiceStopped
	}
	return svc
}

func systemctlIsActive(unit string) (string, error) {
	out, err := exec.Command("systemctl", "is-active", unit).Output()
	if err != nil {
		// systemctl exits non-zero for inactive/failed; the output still
		// carries the state word.
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) >= 0 {
			return strings.TrimSpace(string(out)), nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func systemctlIsEnabled(unit string) (bool, bool) {
	out, err := exec.Command("systemctl", "is-enabled", unit).Output()
	if err != nil {
		return false, false // unknown on distros without systemd unit state
	}
	state := strings.TrimSpace(string(out))
	return state == "enabled" || state == "static", true
}

func serviceScriptStatus(name string) (string, bool) {
	out, err := exec.Command("service", name, "status").CombinedOutput()
	if err != nil && len(out) == 0 {
		return "", false
	}
	return strings.ToLower(string(out)), true
}

func processExists(name string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.ParseInt(e.Name(), 10, 32); err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err == nil && strings.TrimSpace(string(raw)) == name {
			return true
		}
	}
	return false
}

func phpFPMVersions() []string {
	entries, err := os.ReadDir("/etc/php")
	if err != nil {
		return nil
	}
	var vers []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if fi, err := os.Stat("/etc/php/" + e.Name() + "/fpm"); err == nil && fi.IsDir() {
			vers = append(vers, e.Name())
		}
	}
	sort.Strings(vers)
	return vers
}

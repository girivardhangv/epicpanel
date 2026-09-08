package agent

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
) // Collector produces one agentproto.Sample per pass. All percentages and
// rates are computed from two consecutive snapshots (per-CPU /proc/stat
// deltas, /proc/net/dev, /proc/diskstats), never since-boot averages. The
// collector is read-only and safe to run from a single goroutine.
type Collector struct {
	mu       sync.Mutex
	prevCPU  cpuSnap
	prevNet  netSnap
	prevIO   diskIOSnap
	prevAt   time.Time
	services []string
}

func NewCollector() *Collector {
	return &Collector{services: watchedServices}
}

// watchedServices are the platform units whose health the node view shows.
var watchedServices = []string{
	"nginx", "httpd", "openlitespeed", "mariadb", "mysqld", "postgresql",
	"php-fpm", "redis", "epicpanel-agent",
}

// execCommand builds a command with an argument list (never a shell).
func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

var virtualFS = map[string]bool{
	"procfs": true, "sysfs": true, "devtmpfs": true, "tmpfs": true,
	"devpts": true, "squashfs": true, "securityfs": true, "cgroup2fs": true,
	"pstore": true, "bpf": true, "debugfs": true, "tracefs": true,
	"configfs": true, "fusectl": true, "hugetlbfs": true, "efivarfs": true,
	"btrfsctl": true, "overlay": true, "autofs": true, "mqueue": true,
	"ramfs": true, "cgroup": true,
}

var virtualIface = map[string]bool{
	"lo": true, "docker0": true, "veth": true, "br-": true, "virbr0": true,
	"epicpanel": true, "vethif": true,
}

func isVirtualIface(name string) bool {
	if virtualIface[name] {
		return true
	}
	return strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") ||
		strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "epicpanel")
}

// cpuSnap is the cumulative per-CPU jiffies from /proc/stat.
type cpuSnap struct {
	fields []uint64
	at     time.Time
}

type netSnap struct {
	ifaces map[string][2]int64 // name -> rx, tx
	at     time.Time
}

type diskIOSnap struct {
	devs map[string][4]int64 // device -> reads, sectorsRead, writes, sectorsWritten
	at   time.Time
}

func (c *Collector) lastPrevAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.prevAt
}

// Collect gathers one node sample. On the very first call rates are zero
// (the baseline); afterwards every sample carries true delta rates over the
// window between calls.
func (c *Collector) Collect() (*agentproto.NodeSample, bool) {
	start := time.Now()
	s := &agentproto.NodeSample{}

	s.CPUCores = float64(numCPU())
	s.CPUPercent = c.cpuPercent()
	s.Load1, s.Load5, s.Load15 = readLoadavg()
	s.MemoryTotal, s.MemoryUsed, s.MemoryAvailable = readMeminfo()
	s.SwapTotal, s.SwapUsed = readSwap()
	s.Disks = readDisks()
	s.IO = c.diskIORates()
	s.Net = c.netRates()
	s.TCPEstablished, s.TCPTotal = readTCP()
	s.Processes = countProcs()
	s.UptimeS = readUptime()
	s.Services = readServices(c.services)
	s.CollectMS = time.Since(start).Milliseconds()
	return s, true
}

// numCPU reads the online CPU count cheaply.
func numCPU() int {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 1
	}
	n := 0
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "cpu") && len(sc.Text()) > 3 &&
			sc.Text()[3] >= '0' && sc.Text()[3] <= '9' {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

// computeCPUPercent derives utilization from the delta between two
// cumulative jiffies snapshots (fixes the audit's "since-boot average" bug).
// Returns ok=false when no reliable delta exists (baseline or rollover).
func computeCPUPercent(prev, cur []uint64) (float64, bool) {
	if prev == nil || cur == nil || len(prev) != len(cur) {
		return 0, false
	}
	var total, idleAll uint64
	for i := range cur {
		d := cur[i] - prev[i]
		total += d
		if i == 3 || i == 4 { // idle, iowait
			idleAll += d
		}
	}
	if total == 0 {
		return 0, false
	}
	return clampPercent(float64(total-idleAll) / float64(total) * 100), true
}

// parseProcStatCPU parses the aggregate "cpu" line of /proc/stat.
func parseProcStatCPU(b []byte) []uint64 {
	line := firstLine(b)
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return nil
	}
	var vals []uint64
	for _, f := range fields[1:] {
		v, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			return nil
		}
		vals = append(vals, v)
	}
	return vals
}

// computeNetRates derives rx/tx rates over the window between two snapshots.
func computeNetRates(prev, cur netSnap) agentproto.NetSample {
	out := agentproto.NetSample{RxBytes: sumNet(cur.ifaces, 0), TxBytes: sumNet(cur.ifaces, 1)}
	if prev.ifaces == nil {
		return out
	}
	elapsed := cur.at.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return out
	}
	for name, v := range cur.ifaces {
		p, ok := prev.ifaces[name]
		if !ok {
			continue // new iface: no reliable delta
		}
		out.RxBPS += float64(v[0]-p[0]) / elapsed
		out.TxBPS += float64(v[1]-p[1]) / elapsed
	}
	if out.RxBPS < 0 {
		out.RxBPS = 0
	}
	if out.TxBPS < 0 {
		out.TxBPS = 0
	}
	return out
}

// computeIORates derives disk byte/IOPS rates over the window.
func computeIORates(prev, cur diskIOSnap) agentproto.DiskIOSample {
	out := agentproto.DiskIOSample{
		TotalRead:  sumIO(cur.devs, 1, 512),
		TotalWrite: sumIO(cur.devs, 3, 512),
	}
	if prev.devs == nil {
		return out
	}
	elapsed := cur.at.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return out
	}
	for dev, v := range cur.devs {
		p, ok := prev.devs[dev]
		if !ok {
			p = v // device appeared mid-window: count from zero delta
		}
		dRead := v[0] - p[0]
		dSectRead := v[1] - p[1]
		dWrite := v[2] - p[2]
		dSectWrite := v[3] - p[3]
		out.ReadIOPS += float64(dRead) / elapsed
		out.WriteIOPS += float64(dWrite) / elapsed
		out.ReadBPS += float64(dSectRead) * 512 / elapsed
		out.WriteBPS += float64(dSectWrite) * 512 / elapsed
	}
	return out
}

// parseTCPCounts parses /proc/net/tcp{,6} content: established + total
// connections (state "01" = ESTABLISHED, header row skipped).
func parseTCPCounts(b []byte) (established, total int) {
	for i, line := range splitLines(b) {
		if i == 0 {
			continue // header
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		total++
		if fields[3] == "01" {
			established++
		}
	}
	return
}

// cpuPercent computes utilization from the delta between the previous and
// current cumulative jiffies (fixes the audit's "since-boot average" bug).
func (c *Collector) cpuPercent() float64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	vals := parseProcStatCPU(b)
	if vals == nil {
		return 0
	}
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.prevCPU
	c.prevCPU = cpuSnap{fields: vals, at: now}
	if prev.fields == nil {
		return 0 // baseline established; first real percentage next pass
	}
	pct, ok := computeCPUPercent(prev.fields, vals)
	if !ok {
		return 0
	}
	return pct
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func readLoadavg() (l1, l5, l15 float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	_, _ = fmt.Sscanf(string(b), "%f %f %f", &l1, &l5, &l15)
	return
}

func readMeminfo() (total, used, avail int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0
	}
	var memFree, buffers, cached int64
	for _, line := range splitLines(b) {
		var val int64
		switch {
		case hasPrefix(line, "MemTotal:"):
			_, _ = fmt.Sscanf(line, "MemTotal: %d", &val)
			total = val * 1024
		case hasPrefix(line, "MemFree:"):
			_, _ = fmt.Sscanf(line, "MemFree: %d", &val)
			memFree = val * 1024
		case hasPrefix(line, "MemAvailable:"):
			_, _ = fmt.Sscanf(line, "MemAvailable: %d", &val)
			avail = val * 1024
		case hasPrefix(line, "Buffers:"):
			_, _ = fmt.Sscanf(line, "Buffers: %d", &val)
			buffers = val * 1024
		case hasPrefix(line, "Cached:"):
			_, _ = fmt.Sscanf(line, "Cached: %d", &val)
			cached = val * 1024
		}
	}
	if avail == 0 {
		avail = memFree + buffers + cached
	}
	if total > 0 && avail < total {
		used = total - avail
	}
	return
}

func readSwap() (total, used int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, line := range splitLines(b) {
		var val int64
		switch {
		case hasPrefix(line, "SwapTotal:"):
			_, _ = fmt.Sscanf(line, "SwapTotal: %d", &val)
			total = val * 1024
		case hasPrefix(line, "SwapFree:"):
			_, _ = fmt.Sscanf(line, "SwapFree: %d", &val)
			used = total - val*1024
		}
		if total > 0 && used >= 0 {
			break
		}
	}
	return
}

// readDisks enumerates real filesystems from /proc/mounts with usage +
// inode usage; the root filesystem is always reported.
func readDisks() []agentproto.DiskSample {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return rootDiskOnly()
	}
	seen := map[string]bool{}
	var out []agentproto.DiskSample
	for _, line := range splitLines(b) {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		fsType, mount := fields[2], fields[1]
		if virtualFS[fsType] || seen[mount] {
			continue
		}
		if mount == "/" || strings.HasPrefix(mount, "/srv") || strings.HasPrefix(mount, "/home") ||
			strings.HasPrefix(mount, "/var") || strings.HasPrefix(mount, "/boot") {
			seen[mount] = true
			out = append(out, diskSample(fields[0], mount))
		}
	}
	if len(out) == 0 {
		return rootDiskOnly()
	}
	return out
}

func rootDiskOnly() []agentproto.DiskSample {
	return []agentproto.DiskSample{diskSample("", "/")}
}

func diskSample(fs, mount string) agentproto.DiskSample {
	var st syscall.Statfs_t
	d := agentproto.DiskSample{Mount: mount}
	if err := syscall.Statfs(mount, &st); err != nil {
		return d
	}
	d.Fs = fs
	d.TotalBytes = int64(st.Blocks) * int64(st.Bsize)
	d.UsedBytes = (int64(st.Blocks) - int64(st.Bavail)) * int64(st.Bsize)
	d.InodesTotal = int64(st.Files)
	d.InodesUsed = int64(st.Files) - int64(st.Ffree)
	return d
}

// diskIORates reads /proc/diskstats and converts sector deltas into byte
// rates over the window between calls.
func (c *Collector) diskIORates() agentproto.DiskIOSample {
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return agentproto.DiskIOSample{}
	}
	cur := diskIOSnap{devs: map[string][4]int64{}, at: time.Now()}
	for _, line := range splitLines(b) {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		dev := fields[2]
		if !isPhysicalDisk(dev) {
			continue
		}
		reads, _ := strconv.ParseInt(fields[3], 10, 64)
		sectorsRead, _ := strconv.ParseInt(fields[5], 10, 64)
		writes, _ := strconv.ParseInt(fields[7], 10, 64)
		sectorsWritten, _ := strconv.ParseInt(fields[9], 10, 64)
		cur.devs[dev] = [4]int64{reads, sectorsRead, writes, sectorsWritten}
	}
	if len(cur.devs) == 0 {
		return agentproto.DiskIOSample{}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.prevIO
	c.prevIO = cur
	return computeIORates(prev, cur)
}

func sumIO(devs map[string][4]int64, idx, sectorSize int64) int64 {
	var total int64
	for _, v := range devs {
		total += v[idx] * sectorSize
	}
	return total
}

func isPhysicalDisk(dev string) bool {
	if strings.HasPrefix(dev, "loop") || strings.HasPrefix(dev, "ram") ||
		strings.HasPrefix(dev, "dm-") || strings.HasPrefix(dev, "zram") ||
		strings.HasPrefix(dev, "sr") || strings.HasPrefix(dev, "fd") {
		return false
	}
	return true
}

// netRates reads /proc/net/dev and computes rx/tx rates over the window,
// excluding virtual interfaces.
func (c *Collector) netRates() agentproto.NetSample {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return agentproto.NetSample{}
	}
	cur := netSnap{ifaces: map[string][2]int64{}, at: time.Now()}
	for _, line := range splitLines(b) {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if isVirtualIface(name) {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseInt(fields[0], 10, 64)
		tx, _ := strconv.ParseInt(fields[8], 10, 64)
		cur.ifaces[name] = [2]int64{rx, tx}
	}
	if len(cur.ifaces) == 0 {
		return agentproto.NetSample{}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.prevNet
	c.prevNet = cur
	return computeNetRates(prev, cur)
}

func sumNet(ifaces map[string][2]int64, idx int) int64 {
	var total int64
	for _, v := range ifaces {
		total += v[idx]
	}
	return total
}

// readTCP counts established/total TCP states from /proc/net/tcp{,6}.
func readTCP() (established, total int) {
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		e, t := parseTCPCounts(b)
		established += e
		total += t
	}
	return
}

func countProcs() int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && e.Name()[0] >= '0' && e.Name()[0] <= '9' {
			n++
		}
	}
	return n
}

func readUptime() int64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(fields[0], 64)
	return int64(f)
}

// readServices queries systemctl is-active for the watched units. Batched
// into ONE systemctl invocation to keep the collector cheap.
func readServices(names []string) []agentproto.ServiceSample {
	if len(names) == 0 {
		return nil
	}
	args := append([]string{"is-active"}, names...)
	out, err := execCommand("systemctl", args...).Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	lines := splitLines(out)
	var services []agentproto.ServiceSample
	for i, name := range names {
		if i >= len(lines) {
			break
		}
		state := strings.TrimSpace(lines[i])
		if state == "" {
			continue
		}
		// systemctl prints "unit not found" style text for missing units
		// plus an error suffix line; only accept known states.
		switch state {
		case "active", "inactive", "failed", "activating", "deactivating", "unknown":
			services = append(services, agentproto.ServiceSample{Name: name, State: state})
		}
	}
	return services
}

// --- container (Docker) metrics ---

// CollectContainers samples Docker containers via the cgroup filesystem
// (systemd driver: system.slice/docker-<id>.scope; cgroupfs driver:
// docker/<id>). Falls back to zero samples silently when Docker is absent:
// containers are optional. Container names are resolved through docker ps
// and cached (cheap /proc reads every pass, one exec per nameCacheTTL).
func CollectContainers() []agentproto.ContainerSample {
	var scopes []containerScope
	scopes = append(scopes, dockerSystemdScopes()...)
	scopes = append(scopes, dockerCgroupfsScopes()...)
	if len(scopes) == 0 {
		containerNamesMu.Lock()
		containerNames = nil
		containerNamesMu.Unlock()
		return nil
	}
	names := containerNameMap()
	now := time.Now()
	out := make([]agentproto.ContainerSample, 0, len(scopes))
	for _, sc := range scopes {
		s := agentproto.ContainerSample{ID: sc.id}
		if n, ok := names[sc.id]; ok {
			s.Name = n
		}
		dir := sc.dir
		if b, err := os.ReadFile(filepath.Join(dir, "memory.current")); err == nil {
			s.MemoryBytes, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "memory.max")); err == nil {
			v := strings.TrimSpace(string(b))
			if v != "" && v != "max" {
				s.MemoryLimit, _ = strconv.ParseInt(v, 10, 64)
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, "pids.current")); err == nil {
			v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
			s.PIDs = int(v)
		}
		if st, err := os.Stat(dir); err == nil {
			s.UptimeS = int64(now.Sub(st.ModTime()).Seconds())
			if s.UptimeS < 0 {
				s.UptimeS = 0
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, "cpu.stat")); err == nil {
			if usec, ok := parseCpuStat(b); ok {
				s.CPUPercent = containerCPUPercent(sc.id, usec)
			}
		}
		// Network: the container's netns via any of its processes
		// (/proc/<pid>/net/dev reflects the container namespace).
		s.NetRxBPS, s.NetTxBPS = containerNetRate(sc.id, dir)
		out = append(out, s)
	}
	return out
}

type containerScope struct {
	id  string
	dir string
}

func dockerSystemdScopes() []containerScope {
	base := cgroupRoot + "/system.slice"
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []containerScope
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "docker-") || !strings.HasSuffix(name, ".scope") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "docker-"), ".scope")
		if len(id) < 12 {
			continue
		}
		out = append(out, containerScope{id: id[:12], dir: filepath.Join(base, name)})
	}
	return out
}

func dockerCgroupfsScopes() []containerScope {
	base := cgroupRoot + "/docker"
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []containerScope
	for _, e := range entries {
		id := e.Name()
		if len(id) < 12 {
			continue
		}
		out = append(out, containerScope{id: id[:12], dir: filepath.Join(base, id)})
	}
	return out
}

var (
	containerNames   = map[string]string{}
	containerNamesMu sync.Mutex
	containerNamesAt time.Time
)

// containerNameMap resolves container id → name via docker ps, cached for
// 30s (one exec per 30s instead of per sample).
func containerNameMap() map[string]string {
	containerNamesMu.Lock()
	defer containerNamesMu.Unlock()
	if time.Since(containerNamesAt) < 30*time.Second {
		return containerNames
	}
	out, err := execCommand("docker", "ps", "--format", "{{.ID}} {{.Names}}").Output()
	if err == nil {
		m := map[string]string{}
		for _, line := range splitLines(out) {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				m[fields[0]] = fields[1]
			}
		}
		containerNames = m
		containerNamesAt = time.Now()
	}
	return containerNames
}

var containerNetSamples = map[string]netSnap{}
var containerNetMu sync.Mutex

// containerNetRate reads /proc/<pid>/net/dev for one container process to
// get its namespace counters (rate over the window).
func containerNetRate(id, dir string) (rxBPS, txBPS float64) {
	pid := firstProcInCgroup(dir)
	if pid == "" {
		return 0, 0
	}
	b, err := os.ReadFile("/proc/" + pid + "/net/dev")
	if err != nil {
		return 0, 0
	}
	cur := netSnap{ifaces: map[string][2]int64{}, at: time.Now()}
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
		rx, _ := strconv.ParseInt(fields[0], 10, 64)
		tx, _ := strconv.ParseInt(fields[8], 10, 64)
		cur.ifaces[name] = [2]int64{rx, tx}
	}

	containerNetMu.Lock()
	defer containerNetMu.Unlock()
	prev, ok := containerNetSamples[id]
	containerNetSamples[id] = cur
	if !ok {
		return 0, 0
	}
	elapsed := cur.at.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return 0, 0
	}
	for name, v := range cur.ifaces {
		p, ok := prev.ifaces[name]
		if !ok {
			continue
		}
		rxBPS += float64(v[0]-p[0]) / elapsed
		txBPS += float64(v[1]-p[1]) / elapsed
	}
	if rxBPS < 0 {
		rxBPS = 0
	}
	if txBPS < 0 {
		txBPS = 0
	}
	return
}

func firstProcInCgroup(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

var containerCPUSamples = map[string]cpuSample{}
var containerCPUMu sync.Mutex

func containerCPUPercent(id string, usec uint64) float64 {
	containerCPUMu.Lock()
	defer containerCPUMu.Unlock()
	now := time.Now()
	if prev, ok := containerCPUSamples[id]; ok {
		elapsed := now.Sub(prev.at).Seconds()
		if elapsed > 0.25 {
			pct := (float64(usec-prev.usageUsec) / 1e6) / elapsed * 100
			if pct >= 0 {
				containerCPUSamples[id] = cpuSample{usageUsec: usec, at: now}
				return clampPercent(pct)
			}
		}
	}
	containerCPUSamples[id] = cpuSample{usageUsec: usec, at: now}
	return 0
}

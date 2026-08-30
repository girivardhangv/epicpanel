//go:build windows

// Windows metrics collection through native APIs (no POSIX emulation, no
// shell): GetSystemTimes for CPU, GlobalMemoryStatusEx for memory,
// GetLogicalDrives + GetDiskFreeSpaceEx for disks, GetIfTable for network
// counters, Toolhelp32 + GetProcessTimes/K32GetProcessMemoryInfo for the
// bounded process list, and the Service Control Manager for registered
// services. Metrics unavailable on Windows are reported as unavailable
// rather than invented (e.g. load averages, /proc-style user/system split).
package monitoring

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

type WindowsCollector struct {
	mu             sync.Mutex
	prevCPU        winCPUTimes
	prevTotal      winCPUTimes // baseline for per-process CPU deltas
	prevTotalValid bool
	prevProcs      map[uint32]winProcTimes
	extraSvcs      []string
}

type winCPUTimes struct {
	idle, kernel, user uint64 // 100ns ticks; kernel includes idle
	valid              bool
}

type winProcTimes struct {
	kernel, user uint64
}

func NewCollector() (Collector, error) {
	var extra []string
	if v := os.Getenv("EPICPANEL_AGENT_SERVICES"); v != "" {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				extra = append(extra, name)
			}
		}
	}
	return &WindowsCollector{
		prevProcs: map[uint32]winProcTimes{},
		extraSvcs: extra,
	}, nil
}

var (
	modkernel32          = windows.NewLazySystemDLL("kernel32.dll")
	modiphlpapi          = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetSystemTimes   = modkernel32.NewProc("GetSystemTimes")
	procGetDriveType     = modkernel32.NewProc("GetDriveTypeW")
	procGetIfTable       = modiphlpapi.NewProc("GetIfTable")
	procK32GetProcMemInf = modkernel32.NewProc("K32GetProcessMemoryInfo")
	procGetProcessTimes  = modkernel32.NewProc("GetProcessTimes")
	procGlobalMemStatus  = modkernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64   = modkernel32.NewProc("GetTickCount64")
)

func getTickCount64() uint64 {
	r, _, _ := procGetTickCount64.Call()
	return uint64(r)
}

func filetimeToUint64(ft *syscall.Filetime) uint64 {
	return (uint64(ft.HighDateTime) << 32) | uint64(ft.LowDateTime)
}

func (c *WindowsCollector) Collect(ctx context.Context) (*Sample, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	s := &Sample{
		Disks:     []DiskMetric{},
		Network:   []NetworkMetric{},
		Processes: []ProcessMetric{},
		Services:  []ServiceHealth{},
		Errors:    map[string]string{},
	}

	// CPU: busy = (kernel+user) − idle. Load averages do not exist on
	// Windows and remain nil by contract.
	var idle, kernel, user syscall.Filetime
	if r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user))); r != 0 {
		cur := winCPUTimes{
			idle:   filetimeToUint64(&idle),
			kernel: filetimeToUint64(&kernel),
			user:   filetimeToUint64(&user),
			valid:  true,
		}
		if c.prevCPU.valid {
			dTotal := float64((cur.kernel + cur.user) - (c.prevCPU.kernel + c.prevCPU.user))
			dIdle := float64(cur.idle - c.prevCPU.idle)
			if dTotal > 0 {
				idlePct := 100 * dIdle / dTotal
				busy := 100 - idlePct
				if busy < 0 {
					busy = 0
				}
				s.CPUUsage = &busy
				s.CPUIdle = &idlePct
				// Windows does not split user/system the way /proc does
				// without extra PDH machinery; reported honestly as nil.
			}
		} else {
			s.Errors["cpu"] = "warming up"
		}
		// Preserve the previous cycle's totals as the per-process CPU
		// denominator baseline before the CPU section refreshes prevCPU.
		c.prevTotal, c.prevTotalValid = c.prevCPU, c.prevCPU.valid
		c.prevCPU = cur
	} else {
		s.Errors["cpu"] = "GetSystemTimes failed"
	}

	// Memory: used = TotalPhys − AvailPhys. AvailPhys is the memory freely
	// assignable to processes, so the standby cache is NOT counted as used.
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemStatus.Call(uintptr(unsafe.Pointer(&ms))); r != 0 {
		total := int64(ms.TotalPhys)
		avail := int64(ms.AvailPhys)
		used := total - avail
		usage := 100 * float64(used) / float64(total)
		s.MemoryTotalBytes, s.MemoryAvailableBytes, s.MemoryUsedBytes = &total, &avail, &used
		s.MemoryUsagePercent = &usage
		// Commit-based swap: pagefile beyond RAM.
		if ms.TotalPageFile > ms.TotalPhys {
			swapTotal := int64(ms.TotalPageFile - ms.TotalPhys)
			swapUsed := int64(ms.TotalPageFile-ms.AvailPageFile) - used
			if swapUsed < 0 {
				swapUsed = 0
			}
			pct := 100 * float64(swapUsed) / float64(swapTotal)
			s.SwapTotalBytes, s.SwapUsedBytes, s.SwapUsagePercent = &swapTotal, &swapUsed, &pct
		}
	} else {
		s.Errors["memory"] = "GlobalMemoryStatusEx failed"
	}

	// Uptime.
	sec := int64(getTickCount64() / 1000)
	s.UptimeSeconds = &sec

	if drives, err := collectWindowsDisks(); err == nil {
		s.Disks = drives
	} else {
		s.Errors["disk"] = err.Error()
	}

	if nets, err := collectWindowsNetwork(); err == nil {
		s.Network = nets
	} else {
		s.Errors["network"] = err.Error()
	}

	if procs, err := c.collectWindowsProcesses(); err == nil {
		s.Processes = procs
	} else {
		s.Errors["processes"] = err.Error()
	}

	s.Services = collectWindowsServices(c.extraSvcs)

	return s, nil
}

// memoryStatusEx mirrors GLOBALMEMORYSTATUSEX.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func collectWindowsDisks() ([]DiskMetric, error) {
	out := []DiskMetric{}
	bitmask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil, err
	}
	for i := 0; i < 26 && len(out) < MaxDisks; i++ {
		if bitmask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		ptr, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		if dtype, _, _ := procGetDriveType.Call(uintptr(unsafe.Pointer(ptr))); dtype != 3 {
			continue // DRIVE_FIXED only: relevant, accessible filesystems
		}
		var freeToCaller, total, free uint64
		if err := windows.GetDiskFreeSpaceEx(ptr, &freeToCaller, &total, &free); err != nil {
			continue
		}
		if total == 0 {
			continue
		}
		used := int64(total - free)
		usage := 100 * float64(used) / float64(total)
		out = append(out, DiskMetric{
			Mount:        strings.TrimSuffix(root, `\`),
			TotalBytes:   int64(total),
			UsedBytes:    used,
			FreeBytes:    int64(free),
			UsagePercent: usage,
		})
	}
	return out, nil
}

// mibIfRow mirrors MIB_IFROW (iphlpapi); all fields are fixed-size so the
// stride is stable across amd64/arm64.
type mibIfRow struct {
	Name            [256]uint16
	IfIndex         uint32
	Type            uint32
	Mtu             uint32
	Speed           uint32
	PhysAddrLen     uint32
	PhysAddr        [8]uint8
	AdminStatus     uint32
	OperStatus      uint32
	LastChange      uint32
	InOctets        uint32
	InUcastPkts     uint32
	InNUcastPkts    uint32
	InDiscards      uint32
	InErrors        uint32
	InUnknownProtos uint32
	OutOctets       uint32
	OutUcastPkts    uint32
	OutNUcastPkts   uint32
	OutDiscards     uint32
	OutErrors       uint32
	OutQLen         uint32
	DescrLen        uint32
	Descr           [256]uint8
}

const ifTypeSoftwareLoopback = 24

// maxInterfaceNameLen keeps displayed interface names short enough that the
// panel's storage validation (cap 128) can never reject them.
const maxInterfaceNameLen = 64

func truncateName(name string) string {
	if len(name) > maxInterfaceNameLen {
		runes := []rune(name)
		if len(runes) > maxInterfaceNameLen {
			return string(runes[:maxInterfaceNameLen-3]) + "..."
		}
	}
	return name
}

// parseIfTable decodes a MIB_IFTABLE buffer (GetIfTable output). Extracted
// as a pure function so the struct/stride handling is unit-testable.
func parseIfTable(buf []byte) []NetworkMetric {
	if len(buf) < 4 {
		return nil
	}
	numRows := binary.LittleEndian.Uint32(buf[0:4])
	rowSize := unsafe.Sizeof(mibIfRow{})
	out := []NetworkMetric{}
	for i := uint32(0); i < numRows && i < MaxInterfaces; i++ {
		off := uintptr(4) + uintptr(i)*rowSize
		if uintptr(len(buf)) < off+rowSize {
			break
		}
		row := (*mibIfRow)(unsafe.Pointer(&buf[off]))
		if row.Type == ifTypeSoftwareLoopback {
			continue
		}
		name := truncateName("if" + fmt.Sprint(row.IfIndex))
		if descrLen := int(row.DescrLen); descrLen > 0 && descrLen <= len(row.Descr) {
			d := strings.TrimSpace(strings.TrimRight(string(row.Descr[:descrLen]), "\x00 "))
			if d != "" && isMostlyASCII(d) {
				name = truncateName(d)
			}
		}
		out = append(out, NetworkMetric{
			Interface: name,
			RxBytes:   float64(row.InOctets), TxBytes: float64(row.OutOctets),
			RxPackets: float64(row.InUcastPkts), TxPackets: float64(row.OutUcastPkts),
			Errors: float64(row.InErrors + row.OutErrors),
			Drops:  float64(row.InDiscards + row.OutDiscards),
		})
	}
	return out
}

func collectWindowsNetwork() ([]NetworkMetric, error) {
	var size uint32
	if r, _, _ := procGetIfTable.Call(0, uintptr(unsafe.Pointer(&size)), 0); r == 0 || size == 0 {
		return nil, fmt.Errorf("GetIfTable sizing failed")
	}
	buf := make([]byte, size)
	if r, _, _ := procGetIfTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)), 0); r != 0 {
		return nil, fmt.Errorf("GetIfTable failed (r=%d)", r)
	}
	return parseIfTable(buf), nil
}

func isMostlyASCII(s string) bool {
	for _, r := range s {
		if r > 126 || r < 32 {
			return false
		}
	}
	return true
}

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS.
type processMemoryCounters struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

type winRawProc struct {
	pid  uint32
	name string
	ws   uint64
	ct   winProcTimes
	tOK  bool
}

func (c *WindowsCollector) collectWindowsProcesses() ([]ProcessMetric, error) {
	raws := enumerateWindowsProcesses()

	// Denominator for per-process CPU: the total (kernel+user) delta of this
	// cycle vs the previous one. On the first cycle percentages are honestly
	// nil rather than fabricated zeros.
	dTotal := float64(0)
	if c.prevTotalValid && c.prevCPU.valid {
		dTotal = float64((c.prevCPU.kernel + c.prevCPU.user) - (c.prevTotal.kernel + c.prevTotal.user))
	}

	type scored struct {
		p   ProcessMetric
		cpu float64
		mem uint64
	}
	scoredProcs := make([]scored, 0, len(raws))
	newPrev := map[uint32]winProcTimes{}
	for _, r := range raws {
		p := ProcessMetric{Name: r.name, PID: int32(r.pid), MemoryBytes: r.ws, Status: "running"}
		if r.tOK {
			newPrev[r.pid] = r.ct
			if prev, ok := c.prevProcs[r.pid]; ok && dTotal > 0 {
				dk := r.ct.kernel - prev.kernel
				du := r.ct.user - prev.user
				if dk < 0 || du < 0 { // pid reuse or counter anomaly
					dk, du = 0, 0
				}
				pct := 100 * float64(dk+du) / dTotal
				p.CPUPercent = &pct
				scoredProcs = append(scoredProcs, scored{p: p, cpu: pct, mem: r.ws})
				continue
			}
		}
		scoredProcs = append(scoredProcs, scored{p: p, mem: r.ws})
	}
	c.prevProcs = newPrev

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
	for _, sc := range topCPU {
		out = append(out, sc.p)
		seen[sc.p.PID] = true
	}
	for _, sc := range topMem {
		if !seen[sc.p.PID] && len(out) < TopProcesses {
			out = append(out, sc.p)
			seen[sc.p.PID] = true
		}
	}
	return out, nil
}

func enumerateWindowsProcesses() []winRawProc {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)

	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []winRawProc
	first := true
	for {
		var err error
		if first {
			err = windows.Process32First(snap, &entry)
			first = false
		} else {
			err = windows.Process32Next(snap, &entry)
		}
		if err != nil {
			return out
		}
		r := winRawProc{
			pid:  entry.ProcessID,
			name: windows.UTF16ToString(entry.ExeFile[:]),
		}
		if h, herr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID); herr == nil {
			var pmc processMemoryCounters
			pmc.Cb = uint32(unsafe.Sizeof(pmc))
			if r1, _, _ := procK32GetProcMemInf.Call(
				uintptr(h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.Cb)); r1 != 0 {
				r.ws = uint64(pmc.WorkingSetSize)
			}
			var creation, exit, ktime, utime syscall.Filetime
			if r1, _, _ := procGetProcessTimes.Call(
				uintptr(h),
				uintptr(unsafe.Pointer(&creation)),
				uintptr(unsafe.Pointer(&exit)),
				uintptr(unsafe.Pointer(&ktime)),
				uintptr(unsafe.Pointer(&utime))); r1 != 0 {
				r.ct = winProcTimes{
					kernel: filetimeToUint64(&ktime),
					user:   filetimeToUint64(&utime),
				}
				r.tOK = true
			}
			windows.CloseHandle(h)
		}
		out = append(out, r)
	}
}

func collectWindowsServices(extra []string) []ServiceHealth {
	now := time.Now().UTC().Format(time.RFC3339)
	out := []ServiceHealth{}
	appendProc := func(name, display string, running bool) {
		status := ServiceStopped
		if running {
			status = ServiceRunning
		}
		out = append(out, ServiceHealth{
			Name: name, DisplayName: display,
			Status: status, Running: running, Enabled: nil,
			LastChecked: now,
		})
	}

	// nginx / php-cgi run as plain processes under our management rather
	// than SCM services; process presence is the honest signal.
	appendProc("nginx", "Nginx", processNameRunning("nginx.exe"))
	appendProc("php-cgi", "PHP (php-cgi)", processNameRunning("php-cgi.exe"))

	// Operator-registered Windows services (EPICPANEL_AGENT_SERVICES).
	for _, name := range extra {
		out = append(out, queryWindowsService(name, now))
	}
	return out
}

// queryWindowsService asks the Service Control Manager; every failure mode
// maps to an honest status instead of a guess.
func queryWindowsService(name, now string) ServiceHealth {
	svc := ServiceHealth{
		Name: strings.ToLower(name), DisplayName: name,
		Status: ServiceNotInstalled, LastChecked: now,
	}
	m, err := mgr.Connect()
	if err != nil {
		svc.Status = ServiceUnknown
		return svc
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return svc // NotInstalled is accurate here
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		svc.Status = ServiceUnknown
		return svc
	}
	running := st.State == windows.SERVICE_RUNNING
	svc.Running = running
	switch st.State {
	case windows.SERVICE_RUNNING:
		svc.Status = ServiceRunning
	case windows.SERVICE_STOPPED:
		svc.Status = ServiceStopped
	default:
		svc.Status = ServiceUnknown
	}
	return svc
}

func processNameRunning(imageName string) bool {
	for _, p := range enumerateWindowsProcesses() {
		if strings.EqualFold(p.name, imageName) {
			return true
		}
	}
	return false
}

// Pure /proc parsers shared by the Linux collector and the test suite.
// Keeping them free of file I/O and build tags lets the whole suite run on
// any host while the collection endpoints stay platform-specific.
package monitoring

import (
	"strconv"
	"strings"
)

// cpuTimes holds one aggregate /proc/stat sample (linux). Declared here so
// tests can exercise the delta math without a Linux kernel.
type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal, total uint64
	valid                                                        bool
}

// parseProcStatCPU parses the aggregate "cpu" line of /proc/stat.
func parseProcStatCPU(data []byte) (cpuTimes, bool) {
	var t cpuTimes
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		vals := make([]uint64, len(fields))
		var total uint64
		for i, f := range fields {
			v, _ := strconv.ParseUint(f, 10, 64)
			vals[i] = v
			total += v
		}
		get := func(i int) uint64 {
			if i < len(vals) {
				return vals[i]
			}
			return 0
		}
		t = cpuTimes{
			user: get(0), nice: get(1), system: get(2), idle: get(3),
			iowait: get(4), irq: get(5), softirq: get(6), steal: get(7),
			total: total, valid: true,
		}
		return t, true
	}
	return t, false
}

// parseLoadavg parses the three load averages. Windows has no equivalent
// and its collector leaves the fields nil by contract.
func parseLoadavg(data []byte) (*float64, *float64, *float64, bool) {
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return nil, nil, nil, false
	}
	parse := func(raw string) *float64 {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil
		}
		return &v
	}
	return parse(fields[0]), parse(fields[1]), parse(fields[2]), true
}

// parseMemInfo converts /proc/meminfo (kB units) into byte values.
func parseMemInfo(data []byte) map[string]int64 {
	out := map[string]int64{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		key := strings.TrimSuffix(parts[0], ":")
		v, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		unit := int64(1)
		if len(parts) >= 3 && parts[2] == "kB" {
			unit = 1024
		}
		out[key] = v * unit
	}
	return out
}

// parseNetDev parses /proc/net/dev rows into bounded interface metrics.
func parseNetDev(data []byte) []NetworkMetric {
	out := []NetworkMetric{}
	for _, line := range strings.Split(string(data), "\n") {
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "lo" {
			continue // loopback counters are dashboard noise
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) < 12 {
			continue
		}
		nums := make([]float64, 12)
		for i := 0; i < 12; i++ {
			v, _ := strconv.ParseFloat(fields[i], 64)
			nums[i] = v
		}
		out = append(out, NetworkMetric{
			Interface: name,
			RxBytes:   nums[0], RxPackets: nums[1], Errors: nums[2], Drops: nums[3],
			TxBytes: nums[8], TxPackets: nums[9],
		})
		if len(out) >= MaxInterfaces {
			break
		}
	}
	return out
}

// realFilesystems lists on-disk filesystems worth reporting; pseudo and
// synthetic filesystems are excluded per spec §4.
var realFilesystems = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "f2fs": true, "jfs": true, "reiserfs": true, "vfat": true,
	"exfat": true, "ntfs": true, "ntfs3": true, "ufs": true,
}

// parseMounts extracts (device, mount, fstype) triples for real filesystems,
// skipping loop devices and duplicates-by-mount.
func parseMounts(data []byte) [][3]string {
	seen := map[string]bool{}
	var out [][3]string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		dev, mount, fsType := fields[0], fields[1], fields[2]
		if !realFilesystems[fsType] || seen[mount] || strings.HasPrefix(dev, "loop") {
			continue
		}
		seen[mount] = true
		out = append(out, [3]string{dev, mount, fsType})
		if len(out) >= MaxDisks {
			break
		}
	}
	return out
}

// mapState translates the /proc/[pid]/stat state character.
func mapState(state string) string {
	switch state {
	case "R":
		return "running"
	case "S":
		return "sleeping"
	case "D":
		return "disk-wait"
	case "Z":
		return "zombie"
	case "T", "t":
		return "stopped"
	default:
		return "unknown"
	}
}

// parseUptime reads the first field of /proc/uptime.
func parseUptime(data []byte) (float64, bool) {
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

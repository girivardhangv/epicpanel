//go:build linux

package platform

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// Current identifies the compiled-in platform implementation.
func Current() Kind { return KindLinux }

func osVersion() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			v := strings.TrimPrefix(line, "PRETTY_NAME=")
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

func cpuInfo() CPUInfo {
	c := CPUInfo{CoresLogical: 0}
	data, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		set := map[string]bool{}
		var firstModel string
		for _, block := range strings.Split(string(data), "\n\n") {
			block = strings.TrimSpace(block)
			if block == "" || !strings.HasPrefix(block, "processor") {
				continue
			}
			set[block] = true // each logical CPU has its own block
			if firstModel == "" {
				for _, line := range strings.Split(block, "\n") {
					l := strings.TrimSpace(line)
					if strings.HasPrefix(l, "model name") {
						parts := strings.SplitN(l, ":", 2)
						if len(parts) == 2 {
							firstModel = strings.TrimSpace(parts[1])
						}
					}
				}
			}
		}
		c.CoresLogical = len(set)
		c.Model = firstModel
	}
	if c.CoresLogical == 0 {
		c.CoresLogical = numCPU()
	}
	return c
}

func memoryInfo() MemoryInfo {
	var m MemoryInfo
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			m.TotalMB = parseProcKBToMB(line)
		case strings.HasPrefix(line, "MemAvailable:"):
			m.AvailableMB = parseProcKBToMB(line)
		}
		if m.TotalMB > 0 && m.AvailableMB > 0 {
			break
		}
	}
	return m
}

func parseProcKBToMB(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	kb, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || kb <= 0 {
		return 0
	}
	return kb / 1024
}

func diskInfo() []DiskInfo {
	out := []DiskInfo{}
	for _, mount := range []struct{ path, fs string }{
		{"/", "rootfs"},
	} {
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount.path, &st); err != nil {
			continue
		}
		total := int64(st.Blocks) * int64(st.Bsize)
		free := int64(st.Bavail) * int64(st.Bsize)
		out = append(out, DiskInfo{
			Mountpoint: mount.path,
			Filesystem: fsTypeString(&st),
			TotalMB:    total / (1024 * 1024),
			FreeMB:     free / (1024 * 1024),
		})
	}
	return out
}

func fsTypeString(st *syscall.Statfs_t) string {
	// st.Type holds the filesystem magic number; mapping the common ones is
	// enough for inventory purposes without shelling out to `stat`.
	switch st.Type {
	case 0x9123683e:
		return "btrfs"
	case 0xef53:
		return "ext4"
	case 0x58465342:
		return "xfs"
	default:
		return fmt.Sprintf("type-%d", st.Type)
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(h)
}

func numCPU() int { return runtime.NumCPU() }

func runtime_NumCPU() int { return runtime.NumCPU() }

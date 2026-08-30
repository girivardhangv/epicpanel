//go:build linux

package installer

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// totalMemoryMB reads MemTotal from /proc/meminfo on Linux.
func totalMemoryMB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				if err == nil && kb > 0 {
					return kb / 1024
				}
			}
		}
	}
	return 0
}

// dataDirFreeDiskMB stats the filesystem containing "." (panel working dir).
func dataDirFreeDiskMB() int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(".", &st); err != nil {
		return 0
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	return free / (1024 * 1024)
}

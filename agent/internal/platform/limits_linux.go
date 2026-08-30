//go:build linux

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// cgroup2Root is the cgroup v2 mount point. systemd-based hosts expose it at
// /sys/fs/cgroup with a unified hierarchy.
const cgroup2Root = "/sys/fs/cgroup"

// epicpanelCgroupPrefix is the parent slice for all EpicPanel-managed site
// limits. systemd slices live under the system slice by convention.
const epicpanelCgroupPrefix = "system.slice/epicpanel"

// ApplySiteLimits writes a per-site cgroup v2 slice with CPU + memory limits.
// cpuPct 0 means unlimited; memoryMB 0 means unlimited. Creating the slice
// requires a cgroup v2 unified hierarchy (systemd hosts) and a writable
// controller — the agent runs as root so this holds in production.
//
// Enforcement model: the slice is a resource ceiling. FPM pools are assigned
// to their site's slice (per-pool isolation), so a runaway site cannot starve
// its neighbours — the same model cPanel/CloudLinux use.
func ApplySiteLimits(slug string, cpuPct, memMB int) error {
	if !validSlug(slug) {
		return fmt.Errorf("invalid site slug %q", slug)
	}
	sliceDir := filepath.Join(cgroup2Root, epicpanelCgroupPrefix, slug)
	if err := os.MkdirAll(sliceDir, 0o755); err != nil {
		return fmt.Errorf("create cgroup slice for %s: %w", slug, err)
	}

	if cpuPct > 0 {
		// cpu.max format: "<quota> <period>". quota in microseconds.
		quota := strconv.Itoa(cpuPct * 1000)
		val := quota + " 100000\n"
		if err := os.WriteFile(filepath.Join(sliceDir, "cpu.max"), []byte(val), 0o644); err != nil {
			return fmt.Errorf("set cpu.max for %s: %w", slug, err)
		}
	} else {
		_ = os.WriteFile(filepath.Join(sliceDir, "cpu.max"), []byte("max 100000\n"), 0o644)
	}

	if memMB > 0 {
		val := strconv.Itoa(memMB*1024*1024) + "\n"
		if err := os.WriteFile(filepath.Join(sliceDir, "memory.max"), []byte(val), 0o644); err != nil {
			return fmt.Errorf("set memory.max for %s: %w", slug, err)
		}
	} else {
		_ = os.WriteFile(filepath.Join(sliceDir, "memory.max"), []byte("max\n"), 0o644)
	}

	return nil
}

func validSlug(slug string) bool {
	if slug == "" || len(slug) > 64 {
		return false
	}
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return !strings.Contains(slug, "..")
}

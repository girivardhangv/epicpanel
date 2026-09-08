package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SiteUsageJobPayload matches the control plane's site_usage enqueue payload.
type SiteUsageJobPayload struct {
	WebsiteID string `json:"website_id"`
}

// SiteUsageOutcome is the job result: live per-site resource consumption.
// CPU percent is computed from the site slice's cumulative cpu.stat deltas
// (agent-internal window). Memory is the instantaneous cgroup usage.
// Phase 9: limits are echoed from the SAME slice files enforcement wrote
// (readSliceLimits) — display and enforcement share one source.
type SiteUsageOutcome struct {
	WebsiteID     string  `json:"website_id"`
	CPUPercent    float64 `json:"cpu_percent"`    // % of ONE full core (100 = 1 core)
	CPUCoresUsed  float64 `json:"cpu_cores_used"` // same, expressed in cores
	MemoryBytes   int64   `json:"memory_bytes"`
	MemoryLimit   int64   `json:"memory_limit_bytes"` // 0 = unlimited
	CPULimitCores float64 `json:"cpu_limit_cores"`    // 0 = unlimited
	DiskUsedMB    int64   `json:"disk_used_mb"`
	Processes     int     `json:"processes"`
	PidsLimit     int64   `json:"pids_limit"` // 0 = unlimited
	IOWeight      int64   `json:"io_weight"`  // 0 = kernel default
	SampleAt      string  `json:"sample_at"`
}

type cpuSample struct {
	usageUsec uint64
	at        time.Time
}

var (
	cpuSamples   = map[string]cpuSample{}
	sampleWindow = 10 * time.Second
)

// SiteUsage collects live cgroup v2 stats for the site's unix-user slice
// plus disk usage of the site tree. Safe on hosts without cgroups: zeros.
func (e *Executor) SiteUsage(ctx context.Context, websiteID string) (*SiteUsageOutcome, error) {
	out := &SiteUsageOutcome{WebsiteID: websiteID, SampleAt: time.Now().UTC().Format(time.RFC3339)}

	user := siteUnixUser(websiteID)
	slice := siteSlicePath(user)

	// Limits: read through the SAME reader the enforcement verification uses
	// (drift-proof by construction — see enforce.go readSliceLimits).
	lim := readSliceLimits(slice)
	out.MemoryLimit = lim.MemoryMaxBytes
	if lim.CPUPeriodUsec > 0 && lim.CPUQuotaUsec > 0 {
		out.CPULimitCores = float64(lim.CPUQuotaUsec) / float64(lim.CPUPeriodUsec)
	}
	out.PidsLimit = lim.PidsMax
	out.IOWeight = lim.IOWeight

	// Memory: instantaneous usage from memory.current.
	if b, err := os.ReadFile(slice + "/memory.current"); err == nil {
		out.MemoryBytes, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	}
	if b, err := os.ReadFile(slice + "/memory.max"); err == nil {
		v := strings.TrimSpace(string(b))
		if v != "" && v != "max" {
			out.MemoryLimit, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	// CPU limit from cpu.max ("quota period" or "max").
	// (handled above via readSliceLimits — one read path for enforcement
	// verification and display)

	// CPU percent: delta of usage_usec over the sample window. The first
	// observation establishes the baseline (0% reported); subsequent polls
	// report the true rate.
	if b, err := os.ReadFile(slice + "/cpu.stat"); err == nil {
		if usec, ok := parseCpuStat(b); ok {
			now := time.Now()
			if prev, seen := cpuSamples[websiteID]; seen {
				elapsed := now.Sub(prev.at).Seconds()
				if elapsed > 0.5 {
					cpu := (float64(usec-prev.usageUsec) / 1e6) / elapsed // cores used
					if cpu < 0 {
						cpu = 0
					}
					out.CPUCoresUsed = cpu
					out.CPUPercent = cpu * 100
				}
			}
			cpuSamples[websiteID] = cpuSample{usageUsec: usec, at: now}
		}
	}

	// Process count.
	if b, err := os.ReadFile(slice + "/cgroup.procs"); err == nil {
		lines := strings.Fields(string(b))
		out.Processes = len(lines)
	}

	// Disk: walk the site tree.
	out.DiskUsedMB = dirSizeMB(filepath.Join(e.docRootBase, websiteID))

	return out, nil
}

func parseCpuStat(b []byte) (uint64, bool) {
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "usage_usec ") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				v, err := strconv.ParseUint(fields[1], 10, 64)
				return v, err == nil
			}
		}
	}
	return 0, false
}

// LimitsForSite returns the persisted limits (memory MB, cpu cores) from the
// limits state file — used to display caps next to usage.
func LimitsForSite(websiteID string) (memBytes int64, cpuCores float64) {
	user := siteUnixUser(websiteID)
	if user == "" {
		return 0, 0
	}
	b, err := os.ReadFile(limitsStateFile)
	if err != nil {
		return 0, 0
	}
	var state map[string]map[string]any
	if json.Unmarshal(b, &state) != nil || state == nil {
		return 0, 0
	}
	s := state[user]
	if s == nil {
		return 0, 0
	}
	if v, ok := s["memory_bytes"].(float64); ok {
		memBytes = int64(v)
	}
	if v, ok := s["cpu_quota"].(float64); ok && v > 0 {
		cpuCores = v / 100000
	}
	return memBytes, cpuCores
}

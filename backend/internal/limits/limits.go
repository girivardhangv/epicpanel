// Package limits is the single seam for resource-limit computation.
//
// Phase 9: this package is now a thin compatibility shim over the unified
// resource & limit engine (internal/resources). All limit enforcement
// wiring must go through the engine — this shim only preserves the Phase-4
// call sites' shapes (websites handler + lifecycle tests). No limit math
// lives here; the derivation is resources.PoolLimitsFor.
package limits

import (
	"github.com/epicbyte/epicpanel/backend/internal/packages"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
)

// PoolLimits describes the FPM pool sizing for a website. Zero values mean
// "no package quota configured" and leave the agent defaults in place.
type PoolLimits = resources.PoolLimits

// EngineLimits is the cgroup v2 per-site limit set (CPU percent, memory
// bytes, pids cap) applied by the engine-level enforcement path.
type EngineLimits struct {
	CPUPercent  int64
	MemoryBytes int64
	PidsMax     int64
}

// ForPackage maps hosting_packages quota columns to FPM pool values via the
// unified engine (single derivation; Phase 4 behavior preserved):
// MemoryLimitMB is the per-request php_admin_value, MaxChildren is
// clamp(memory_limit_mb / 32, 1, 100).
func ForPackage(pkg *packages.Package) PoolLimits {
	if pkg == nil || pkg.MemoryLimitMB <= 0 {
		return PoolLimits{}
	}
	pl := resources.PoolLimitsFor(int64(pkg.MemoryLimitMB))
	if pl.MemoryLimitMB <= 0 {
		return PoolLimits{}
	}
	return pl
}

// ForWebsite returns the engine-level cgroup limits for a website derived
// from the package columns (cpu.max percent, memory.max bytes, pids.max).
// Zero package → zero struct (agent defaults; unlimited).
func ForWebsite(pkg *packages.Package) EngineLimits {
	if pkg == nil {
		return EngineLimits{}
	}
	return EngineLimits{
		CPUPercent:  int64(pkg.CPUCores * 100),
		MemoryBytes: resources.CgroupMemoryBytes(int64(pkg.MemoryLimitMB)),
	}
}

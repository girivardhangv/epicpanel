package traffic

import "fmt"

// ============================================================================
// Tier math: how the allocator turns a package's base limits into the
// limits of the CURRENT allocation tier.
//
//   tier 0 = floor      — smallest useful allocation (default 32MB RAM)
//   tier 1 = base       — exactly the plan (or Free Perk) numbers
//   tier 2..MaxTier     — multiples of the base (2x/4x/8x)
//
// Disk, bandwidth and I/O weight are NOT tiered (data does not shrink with
// traffic); only CPU / RAM / process / PHP-worker ceilings scale.
// ============================================================================

// BaseLimits is the un-scaled (tier 1) limit set for one site. Zero means
// "unlimited" exactly like the enforce payload semantics.
type BaseLimits struct {
	MemoryMB    int64
	CPUPercent  float64 // % of one core
	PidsMax     int64
	FpmChildren int
}

// MaxTier is the highest scale-up multiple (tier 4 = 8x base).
const MaxTier = 4

var tierMultipliers = map[int]float64{
	0: 0, // floor — special-cased in Scale
	1: 1,
	2: 2,
	3: 4,
	4: 8,
}

// ClampTier bounds a stored tier into the valid range.
func ClampTier(t int) int {
	if t < 0 {
		return 0
	}
	if t > MaxTier {
		return MaxTier
	}
	return t
}

// Scale computes the effective limits for a tier. floorMB is the tier-0
// memory floor (panel setting, default 32). CPU/pids floor to 20%/25% of
// base at tier 0 (never fully zeroed — the site must still boot); unlimited
// bases stay unlimited.
func Scale(base BaseLimits, tier int, floorMB int64) BaseLimits {
	tier = ClampTier(tier)
	if tier == 0 {
		out := BaseLimits{}
		if floorMB > 0 {
			out.MemoryMB = floorMB
		} else {
			out.MemoryMB = base.MemoryMB // no floor configured: keep base
		}
		if base.CPUPercent > 0 {
			out.CPUPercent = base.CPUPercent * 0.2
			if out.CPUPercent < 10 {
				out.CPUPercent = 10
			}
		}
		if base.PidsMax > 0 {
			out.PidsMax = base.PidsMax / 4
			if out.PidsMax < 4 {
				out.PidsMax = 4
			}
		}
		out.FpmChildren = FPMChildrenFor(out.MemoryMB, base.FpmChildren)
		return out
	}
	m := tierMultipliers[tier]
	out := BaseLimits{
		MemoryMB:   base.MemoryMB * int64(m),
		CPUPercent: base.CPUPercent * m,
		PidsMax:    base.PidsMax * int64(m),
	}
	if out.CPUPercent > 800 { // 8-core ceiling: the panel never exceeds it
		out.CPUPercent = 800
	}
	if out.PidsMax > 1024 {
		out.PidsMax = 1024
	}
	out.FpmChildren = FPMChildrenFor(out.MemoryMB, base.FpmChildren)
	return out
}

// fpmChildrenFor re-derives the PHP worker ceiling for a memory ceiling
// (1 child per 32MB, clamped 1..100 — same math as the Phase 9 engine).
// Unlimited memory keeps the base children.
func FPMChildrenFor(memMB int64, base int) int {
	if memMB <= 0 {
		return base
	}
	children := memMB / 32
	if children < 1 {
		children = 1
	}
	if children > 100 {
		children = 100
	}
	if base > 0 && children > int64(base)*4 { // never explode workers on a small plan
		children = int64(base) * 4
	}
	return int(children)
}

// Describe renders a human-readable tier for events/UI.
func Describe(tier int, floorMB int64) string {
	tier = ClampTier(tier)
	switch tier {
	case 0:
		return fmt.Sprintf("floor (%dMB)", floorMB)
	case 1:
		return "base"
	default:
		return fmt.Sprintf("%dx base", int(tierMultipliers[tier]))
	}
}

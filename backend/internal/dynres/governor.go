package dynres

import "fmt"

// Governor is the safety layer between the engine's decisions and the
// enforcer. It answers one question: MAY this site take this tier right
// now, given node capacity and fleet-wide caps? A bug in the engine must
// never be able to allocate a node into memory death.
//
// Pure function — all inputs gathered by the caller (live store, DB, settings).
type GovernorInput struct {
	Tier        int
	DesiredTier int

	// Allocation math (memory) for the delta check.
	DesiredMemBytes  int64
	CurrentMemBytes  int64

	// Node capacity.
	NodeMemAvailableBytes int64
	NodeMemTotalBytes     int64

	// Fleet caps. GlobalCapPercent 0 disables the cap; MaxScaleUpsPerMinute
	// 0 disables the rate limit.
	GlobalDynamicAllocatedBytes int64
	GlobalCapPercent            int
	ScaleUpsLastMinute          int
	MaxScaleUpsPerMinute        int

	// MaxTier is the per-site ceiling (panel setting, ≤ traffic.MaxTier).
	MaxTier int
}

// GovernorVerdict is the governor's ruling. When Allowed is false the site
// stays at Tier and Reason explains why (surfaced in events/audit).
type GovernorVerdict struct {
	Allowed       bool
	EffectiveTier int
	Reason        string
}

// nodeReserveFraction of total node RAM is kept out of dynamic allocation —
// the host, control plane and non-dynamic workloads must never starve.
const nodeReserveFraction = 0.10

// Govern applies the ceiling, rate and capacity checks in escalation order
// and returns the tier that may actually be applied.
func Govern(in GovernorInput) GovernorVerdict {
	v := GovernorVerdict{EffectiveTier: in.Tier}

	if in.DesiredTier <= in.Tier {
		v.Allowed = true
		v.EffectiveTier = in.DesiredTier
		return v
	}

	if in.DesiredTier > in.MaxTier {
		v.Reason = fmt.Sprintf("site ceiling is tier %d", in.MaxTier)
		return v
	}

	if in.MaxScaleUpsPerMinute > 0 && in.ScaleUpsLastMinute >= in.MaxScaleUpsPerMinute {
		v.Reason = fmt.Sprintf("global scale-up rate limit reached (%d/min)", in.MaxScaleUpsPerMinute)
		return v
	}

	delta := in.DesiredMemBytes - in.CurrentMemBytes
	if delta > 0 {
		if in.GlobalCapPercent > 0 && in.NodeMemTotalBytes > 0 {
			capBytes := int64(float64(in.NodeMemTotalBytes) * float64(in.GlobalCapPercent) / 100)
			if in.GlobalDynamicAllocatedBytes+delta > capBytes {
				v.Reason = fmt.Sprintf("fleet dynamic allocation cap reached (%d%% of node RAM)", in.GlobalCapPercent)
				return v
			}
		}
		reserve := int64(float64(in.NodeMemTotalBytes) * nodeReserveFraction)
		if in.NodeMemAvailableBytes-delta < reserve {
			v.Reason = fmt.Sprintf("node would drop below its %.0f%% RAM reserve (available %dMB, need +%dMB)",
				nodeReserveFraction*100, in.NodeMemAvailableBytes/(1024*1024), delta/(1024*1024))
			return v
		}
	}

	v.Allowed = true
	v.EffectiveTier = in.DesiredTier
	return v
}

package resources

import (
	"fmt"

	"github.com/google/uuid"
)

// Workload identifies one enforceable unit of hosting. Kind selects the
// resource set; PlanInput carries the plan row's columns (data, not code).
type Workload struct {
	Kind      WorkloadKind
	WebsiteID uuid.UUID
	ServerID  uuid.UUID
	UnixUser  string
	Plan      PlanInput
}

// Engine is the single resource/limit API. Stateless + pure: callers own the
// DB row and the agent transports.
type Engine struct{}

// NewEngine constructs the engine.
func NewEngine() *Engine { return &Engine{} }

// ============================================================================
// GetLimits / GetUsage — one call for every consumer (WHM plan editor,
// per-account usage bars, create-time validation, agent payloads).
// ============================================================================

// GetLimits resolves the unified limit set for a workload from its plan
// row. Enforced marks kernel-enforceable resources; counts are validated at
// create time (validated-only) — both stay visible in the same list.
func (e *Engine) GetLimits(w Workload) Limits {
	spec := PlanFromInput(w.Plan)
	return SpecToLimits(spec, w)
}

// SpecToLimits renders a plan spec as the unified Resources map. Enforced =
// true for node-side enforceable resources (cpu/ram/io/processes); counts
// are validated-only by nature; disk/bandwidth enforcement depends on node
// capability reported by the agent (default honest: disk=quota-if-supported,
// bandwidth=accounted-only).
func SpecToLimits(spec PlanSpec, w Workload) Limits {
	out := Limits{Kind: spec.Kind, Plan: spec.Name, Resources: map[string]Resource{}}
	for _, name := range ResourceSetFor(spec.Kind) {
		qty, ok := spec.Limits[name]
		if !ok {
			continue
		}
		out.Resources[name] = newResource(name, qty)
	}
	// The kind's resource set is the display contract, but a plan row may
	// also govern create-time counts outside it (e.g. Backups on web plans
	// — the row has max_backups). Render those too so validation and WHM
	// see the full matrix; display layers that filter to the kind set are
	// unaffected.
	for name, qty := range spec.Limits {
		if _, ok := out.Resources[name]; ok {
			continue
		}
		out.Resources[name] = newResource(name, qty)
	}
	return out
}

// newResource renders one resource with honest enforcement metadata. For
// node resources (cpu/ram/disk/bandwidth/io/processes) a quantity of 0 means
// unlimited; for counted resources 0 is a real cap ("zero allowed").
func newResource(name string, qty float64) Resource {
	r := Resource{Name: name, Unit: UnitFor(name), Limit: qty}
	if qty <= 0 && !countResources[name] {
		r.LimitUnlimited = true
	}
	switch name {
	case ResCPU, ResRAM, ResIO, ResProcesses, ResPHPWorkers:
		r.Enforced = true
		r.Source = "cgroup-v2"
	case ResDisk:
		r.Enforced = false // enforced only when the node supports fs quotas
		r.Source = "fs-quota-or-accounted"
		r.Note = "hard cap applied when the site filesystem supports project/user quotas; usage always accounted by the agent"
	case ResBandwidth:
		r.Enforced = false
		r.Source = "nftables-accounting"
		r.Note = "RX+TX accounted per account by the agent; over-limit handled by policy (throttle/suspend)"
	default:
		r.Source = "control-plane"
	}
	return r
}

// Usage values come from the SAME node-side layer that enforces limits: the
// Phase 3 agent collector (LiveStore per-workload envelopes + cgroup slices)
// and the agent's quota/counter ops. The control plane never invents usage.
type Usage struct {
	// Values: resource name → quantity in the resource's single unit.
	Values map[string]float64
	// Source is the measurement mechanism string ("agent-collector:cgroup-v2",
	// "agent:nftables-counters", ...). Propagated to every Resource.Source.
	Source string
}

// GetUsage merges plan limits with authoritative agent usage — the shape the
// usage bars render from.
func (e *Engine) GetUsage(w Workload, u Usage) Limits {
	limits := e.GetLimits(w)
	for name, r := range limits.Resources {
		if v, ok := u.Values[name]; ok {
			r.Usage = v
			r.Source = u.Source
			limits.Resources[name] = r
		}
	}
	return limits
}

// ============================================================================
// Enforce — the node-side contract. The engine produces the exact numbers
// the agent applies; the agent reports honest per-resource outcomes.
// ============================================================================

// EnforcePayload is the enforce_limits job body: the resolved plan limits in
// engine units. The agent derives mechanism specifics (cgroup cpu.max,
// memory.max, pids.max, io.weight, fs quota, nftables chains, FPM pool).
type EnforcePayload struct {
	WebsiteID      string  `json:"website_id"`
	Plan           string  `json:"plan"`
	CPUPercent     float64 `json:"cpu_percent"`      // % of one core; 0 = unlimited
	MemoryMB       int64   `json:"memory_mb"`        // memory.max; 0 = unlimited
	DiskMB         int64   `json:"disk_mb"`          // quota target; 0 = accounted only
	BandwidthMB    int64   `json:"bandwidth_mb"`     // monthly RX+TX budget; 0 = unlimited
	IOWeight       int     `json:"io_weight"`        // cgroup io.weight; 0 = default
	PidsMax        int64   `json:"pids_max"`         // 0 = unlimited
	FpmMaxChildren int     `json:"fpm_max_children"` // PHP workers; 0 = agent default
	// Counts for the agent-side reconciliation guard: control-plane-observed
	// counts (databases/domains/ports/backups/email) vs plan caps.
	Counts map[string]int `json:"counts,omitempty"`
	// CountLimits: the plan caps for governed counts (0 = zero allowed).
	CountLimits map[string]int `json:"count_limits,omitempty"`
}

// EnforcePlan builds the agent payload from the resolved limits. This is the
// ONLY place plan numbers become enforcement numbers, so displayed limits
// and enforced limits cannot drift.
func (e *Engine) EnforcePlan(w Workload) EnforcePayload {
	limits := e.GetLimits(w)
	p := EnforcePayload{WebsiteID: w.WebsiteID.String(), Plan: limits.Plan}
	if r, ok := limits.Get(ResCPU); ok && r.Limit > 0 {
		p.CPUPercent = r.Limit
	}
	if r, ok := limits.Get(ResRAM); ok && r.Limit > 0 {
		p.MemoryMB = int64(r.Limit)
	}
	if r, ok := limits.Get(ResDisk); ok && r.Limit > 0 {
		p.DiskMB = int64(r.Limit)
	}
	if r, ok := limits.Get(ResBandwidth); ok && r.Limit > 0 {
		p.BandwidthMB = int64(r.Limit)
	}
	if r, ok := limits.Get(ResIO); ok && r.Limit > 0 {
		p.IOWeight = int(r.Limit)
	}
	if r, ok := limits.Get(ResProcesses); ok && r.Limit > 0 {
		p.PidsMax = int64(r.Limit)
	}
	if r, ok := limits.Get(ResPHPWorkers); ok && r.Limit > 0 {
		p.FpmMaxChildren = int(r.Limit)
	}
	for name, r := range limits.Resources {
		if r.LimitUnlimited || !countResources[name] {
			continue
		}
		if p.CountLimits == nil {
			p.CountLimits = map[string]int{}
		}
		p.CountLimits[name] = int(r.Limit)
	}
	return p
}

// ============================================================================
// Kernel/fs/network conversions + clamps. Single source: the control-plane
// display path (siteLimitsFor) and the agent apply path both call these.
// ============================================================================

// CgroupCPUMax converts a CPU percent (100 = 1 core) into cpu.max
// "quota period" microseconds over the standard 100ms period.
func CgroupCPUMax(percent float64) (quota, period int64, ok bool) {
	if percent <= 0 {
		return 0, 0, false
	}
	return int64(percent * 1000), 100000, true // 200% → 200000/100000
}

// CgroupMemoryBytes converts the plan RAM (MB) to memory.max bytes, floored
// at 128 MiB so a typo'd plan can't create a 0-byte slice.
func CgroupMemoryBytes(memMB int64) int64 {
	if memMB <= 0 {
		return 0
	}
	b := memMB * 1024 * 1024
	if b < 128*1024*1024 {
		b = 128 * 1024 * 1024
	}
	return b
}

// CgroupPidsMax renders the pids.max value: 0 → "max" (unlimited).
func CgroupPidsMax(pids int64) string {
	if pids <= 0 {
		return "max"
	}
	return fmt.Sprintf("%d", pids)
}

// ClampIOWeight bounds io.weight to the kernel's 1..10000 range (0 = keep
// kernel default, untouched).
func ClampIOWeight(w int) int {
	if w <= 0 {
		return 0
	}
	if w < 1 {
		return 1
	}
	if w > 10000 {
		return 10000
	}
	return w
}

// processMemoryMB is the assumed per-request PHP memory footprint used to
// derive FPM pool sizing from a plan's RAM budget (was the Phase 4 stub's
// private constant; now the engine's single derivation).
const processMemoryMB = 32

// PoolLimits describes the FPM pool sizing for a website. Zero values mean
// "no plan quota configured" and leave agent defaults in place.
type PoolLimits struct {
	MemoryLimitMB int
	MaxChildren   int
}

// PoolLimitsFor derives the FPM pool bound from the plan RAM:
//   - MaxChildren  = clamp(ram / 32MB assumed average worker, 1, 100) —
//     the "PHP workers" bound for pm.max_children;
//   - MemoryLimitMB = per-request php_admin_value[memory_limit] ceiling,
//     clamped to 256M so one request cannot consume the whole plan RAM
//     (the kernel memory.max at the plan RAM is the real backstop).
//
// Zero RAM (unlimited plan) → zero struct, agent defaults apply.
func PoolLimitsFor(ramMB int64) PoolLimits {
	if ramMB <= 0 {
		return PoolLimits{}
	}
	children := ramMB / processMemoryMB
	if children < 1 {
		children = 1
	}
	if children > 100 {
		children = 100
	}
	ceiling := ramMB
	if ceiling > 256 {
		ceiling = 256
	}
	if ceiling < 16 {
		ceiling = 16
	}
	return PoolLimits{MemoryLimitMB: int(ceiling), MaxChildren: int(children)}
}

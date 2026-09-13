// Package resources is the unified resource & limit engine (Phase 9).
//
// ONE engine for every workload kind (web hosting): the same
// Resource model, the same Plan→limits mapping (pure data) and the same
// three-call API — GetLimits / GetUsage / Enforce. Adding a new plan is a
// data change (a hosting_packages row), never a code change.
//
// The master doc's model, verbatim:
//
//	Resource: CPU · RAM · Disk · Bandwidth · Processes · Databases ·
//	          Domains · Ports · Backups
//	Web hosting adds: I/O · PHP workers · Email
//
// Enforcement contract ("the agent must enforce limits, not merely display
// them"): every mechanism applies node-side controls (cgroups v2, filesystem
// project quotas, nftables/tc shaping) and reports honest per-resource
// outcomes — enforce vs validated-only with a reason. Usage displayed in the
// UI comes from the same agent measurement layer that feeds enforcement
// (the Phase 3 collector + workload envelopes), never a second source.
package resources

import "fmt"

// Resource name constants. Web hosting (verbatim): CPU · RAM · Disk · I/O ·
// Processes · PHP workers · Databases · Domains · Email · Bandwidth.
// Counts (Databases/Domains/Ports/Backups/Email) are create-time validated.
const (
	ResCPU        = "cpu"
	ResRAM        = "ram"
	ResDisk       = "disk"
	ResBandwidth  = "bandwidth"
	ResIO         = "io"
	ResProcesses  = "processes"
	ResPHPWorkers = "php_workers"
	ResDatabases  = "databases"
	ResDomains    = "domains"
	ResEmail      = "email"
	ResPorts      = "ports"
	ResBackups    = "backups"
)

// Units used across the engine. One unit per resource, chosen once:
// cpu → percent of one core (100 = 1 core), ram/disk/bandwidth → MB,
// everything else → a plain count.
const (
	UnitPercent = "percent"
	UnitMB      = "MB"
	UnitCount   = "count"
)

// WorkloadKind is the resource-set selector (hosting_packages.kind).
type WorkloadKind string

const (
	KindWeb WorkloadKind = "web"
)

// Resource is the unified resource model (name, quantity, unit, usage).
type Resource struct {
	Name     string  `json:"name"`
	Limit    float64 `json:"limit"`          // 0 = unlimited when LimitUnlimited
	Unit     string  `json:"unit"`           // percent | MB | count
	Usage    float64 `json:"usage"`          // measured by the node agent layer
	Enforced bool    `json:"enforced"`       // kernel/fs/network-enforced vs validated-only
	Source   string  `json:"source"`         // which measurement mechanism produced Usage
	Note     string  `json:"note,omitempty"` // degrade reason / human context
	// LimitUnlimited marks a limit of 0 as genuinely unlimited (vs unset).
	LimitUnlimited bool `json:"limit_unlimited,omitempty"`
}

// PercentUsed is the display helper for usage-vs-limit bars. Returns >100
// when over limit, 0 when unlimited (bars render "no cap" instead of 0%).
func (r Resource) PercentUsed() float64 {
	if r.Limit <= 0 || r.LimitUnlimited {
		return 0
	}
	return r.Usage / r.Limit * 100
}

// OverLimit reports whether current usage exceeds the limit.
func (r Resource) OverLimit() bool {
	if r.Limit <= 0 || r.LimitUnlimited {
		return false
	}
	return r.Usage > r.Limit
}

// Limits is the complete limit set for one workload, resolved from its plan.
type Limits struct {
	Kind      WorkloadKind        `json:"kind"`
	Plan      string              `json:"plan"`
	Resources map[string]Resource `json:"resources"`
}

// Get returns the named resource (ok=false when the plan does not govern it).
func (l Limits) Get(name string) (Resource, bool) {
	r, ok := l.Resources[name]
	return r, ok
}

// CountLimit returns the integer cap for a counted resource (databases,
// domains, ports, backups, email, processes, php workers). ok=false when the
// plan does not govern that resource; unlimited → cap math.NoLimit.
func (l Limits) CountLimit(name string) (int, bool) {
	r, ok := l.Resources[name]
	if !ok {
		return 0, false
	}
	if r.LimitUnlimited || r.Limit <= 0 {
		return NoLimit, true
	}
	return int(r.Limit), true
}

// NoLimit is the sentinel for an ungoverned-but-unlimited count.
const NoLimit = -1

// fmtHuman is used by tests and the WHM display paths.
func fmtHuman(mb float64) string {
	return fmt.Sprintf("%.0f MB", mb)
}

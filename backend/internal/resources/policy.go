package resources

import "fmt"

// Action is an over-limit response. Defaults are conservative and honest:
// the kernel already hard-caps CPU/RAM/Processes (cpu.max, memory.max,
// pids.max), so runtime actions exist for the resources the kernel cannot
// cap (disk without fs quota, bandwidth) plus the notify trail.
type Action string

const (
	ActionNone     Action = "none"
	ActionNotify   Action = "notify"
	ActionThrottle Action = "throttle"
	ActionKill     Action = "kill"
	ActionSuspend  Action = "suspend"
)

// Policy maps a resource to the action taken when usage exceeds the limit.
// "default" policy (the only one until Phase 10 exposes per-plan policies):
//   - cpu / ram / processes: none — cgroups v2 already throttles/OOM-kills
//     inside the slice; no duplicate control-plane action.
//   - disk: kernel quota hard-caps writes when supported; when the agent
//     reports accounting-only (no quota support) the control plane suspends
//     the workload (reversible, via the suspend_website job — Phase 10
//     consumes the same hook).
//   - bandwidth: suspend at the monthly RX+TX budget (throttle available as
//     a policy; nftables rate limiting is agent-side).
//   - counts: never breach at runtime — they are create-time validated.
type Policy map[string]Action

// DefaultPolicy is the seed policy. Values are actions, keyed by resource.
func DefaultPolicy() Policy {
	return Policy{
		ResCPU:       ActionNone,    // kernel throttle (cpu.max)
		ResRAM:       ActionNone,    // kernel OOM-kill (memory.max)
		ResIO:        ActionNone,    // kernel weight (io.weight)
		ResProcesses: ActionNone,    // kernel pids.max
		ResDisk:      ActionSuspend, // no kernel backstop when quota unsupported
		ResBandwidth: ActionSuspend, // monthly budget; Phase 10 consumes the hook
	}
}

// Breach is one over-limit observation with its policy action.
type Breach struct {
	Resource string  `json:"resource"`
	Usage    float64 `json:"usage"`
	Limit    float64 `json:"limit"`
	Unit     string  `json:"unit"`
	Action   Action  `json:"action"`
	Message  string  `json:"message"`
}

// Evaluate applies the policy to a usage-vs-limits view. Unlimited limits
// (0) never breach; count resources only breach when the plan governs them.
func Evaluate(l Limits, u Usage, policy Policy) []Breach {
	if policy == nil {
		policy = DefaultPolicy()
	}
	var out []Breach
	for name, r := range l.Resources {
		if !r.OverLimit() {
			continue
		}
		action := policy[name]
		if action == "" {
			action = ActionNone
		}
		b := Breach{Resource: name, Usage: r.Usage, Limit: r.Limit, Unit: r.Unit, Action: action}
		b.Message = fmt.Sprintf("%s usage %s exceeds plan %s limit %s (%.1f%%)",
			name, formatQty(r.Unit, r.Usage), l.Plan, formatQty(r.Unit, r.Limit), r.PercentUsed())
		out = append(out, b)
	}
	return out
}

// ============================================================================
// Count-limit validation — create-time gates for Databases/Domains/Ports/
// Backups/Email (+ Processes/PHPWorkers/Websites style counts). Control
// plane validates before creating; the agent re-checks the counts it can
// observe (reconciliation guard) and reports — never mutates.
// ============================================================================

// ErrLimitReached is returned when a counted resource would exceed the plan.
type ErrLimitReached struct {
	Plan     string
	Resource string
	Limit    int
	Current  int
	Kind     string // "create" (adding one more) or "exists"
}

func (e ErrLimitReached) Error() string {
	return fmt.Sprintf("package limit reached: %s allows %d %s", e.Plan, e.Limit, e.Resource)
}

// CheckCount validates that current usage allows one more unit of the
// counted resource under the plan's limits. Ungoverned resources (not in
// the plan matrix) pass; a governed count of 0 genuinely forbids the
// resource (e.g. a plan with backups = 0).
func CheckCount(l Limits, name string, current int) error {
	r, ok := l.Resources[name]
	if !ok {
		return nil // not governed by this plan's resource set
	}
	if r.LimitUnlimited {
		return nil
	}
	if current >= int(r.Limit) {
		return ErrLimitReached{Plan: l.Plan, Resource: name, Limit: int(r.Limit), Current: current, Kind: "create"}
	}
	return nil
}

// CheckCounts validates every governed count in current against the plan.
// Returns the first violation (stable order = ResourceSetFor order).
func CheckCounts(l Limits, current map[string]int) error {
	for _, name := range ResourceSetFor(l.Kind) {
		if _, counted := countResources[name]; !counted {
			continue
		}
		cur, ok := current[name]
		if !ok {
			continue
		}
		if err := CheckCount(l, name, cur); err != nil {
			return err
		}
	}
	return nil
}

// countResources are the resources validated as counts at create time.
var countResources = map[string]bool{
	ResDatabases:  true,
	ResDomains:    true,
	ResEmail:      true,
	ResPorts:      true,
	ResBackups:    true,
	ResProcesses:  false, // node-side enforced (pids.max), not a create-time gate
	ResPHPWorkers: false, // pool bound, not a create-time gate
}

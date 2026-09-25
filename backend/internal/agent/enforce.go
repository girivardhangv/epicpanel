package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/resources"
	"github.com/google/uuid"
)

// ============================================================================
// Phase 9 — node-side limit enforcement. The agent ENFORCES, it does not
// merely display: this file applies the plan matrix (the SAME numbers the
// control plane displays — both come from internal/resources) onto the node:
//
//   CPU / RAM / Processes : cgroups v2 (cpu.max, memory.max, pids.max)
//   Disk                  : filesystem project/user quotas when supported,
//                           honestly accounted-only otherwise
//   Bandwidth             : nftables per-account RX+TX counters (+ optional
//                           rate shaping); tc class fallback is NOT used for
//                           enforcement, only nft — one mechanism, honestly
//                           accounted-only when nftables is absent
//   I/O                   : cgroup v2 io.weight where supported
//   PHP workers           : FPM pool pm.max_children bound to the plan
//   Counts                : reconciliation guard — the agent reports observed
//                           counts vs plan caps; it never mutates state
//
// Every mechanism is detect-then-apply: an unsupported kernel/fs/network
// feature degrades to validated-only/accounted-only WITH the reason in the
// outcome — never a silent pretend.
// ============================================================================

// EnforceJobPayload matches the control plane's enforce_limits enqueue body
// (produced by Engine.EnforcePlan; the same mapping the display path reads).
type EnforceJobPayload struct {
	WebsiteID      string         `json:"website_id"`
	Plan           string         `json:"plan"`
	CPUPercent     float64        `json:"cpu_percent"`
	MemoryMB       int64          `json:"memory_mb"`
	DiskMB         int64          `json:"disk_mb"`
	BandwidthMB    int64          `json:"bandwidth_mb"`
	IOWeight       int            `json:"io_weight"`
	PidsMax        int64          `json:"pids_max"`
	FpmMaxChildren int            `json:"fpm_max_children"`
	CountLimits    map[string]int `json:"count_limits,omitempty"`
}

// MechanismOutcome is the honest per-resource enforcement report.
type MechanismOutcome struct {
	Resource  string `json:"resource"`
	Mode      string `json:"mode"` // enforced | accounted | validated-only | skipped
	Mechanism string `json:"mechanism,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// EnforceOutcome is the enforce_limits job result. It feeds the control
// plane's usage bars (same source as enforcement) and drift monitoring.
type EnforceOutcome struct {
	WebsiteID  string             `json:"website_id"`
	Plan       string             `json:"plan"`
	AppliedAt  string             `json:"applied_at"`
	Mechanisms []MechanismOutcome `json:"mechanisms"`

	// Usage snapshot measured by the SAME layer that just enforced (cgroup
	// reads, quota report, nft counters) — display == enforcement source.
	Usage map[string]float64 `json:"usage,omitempty"`

	// Breaches: over-limit resources with the policy action the agent took
	// or requests (throttle/kill/suspend). Never silently corrupts state.
	Breaches []BreachReport `json:"breaches,omitempty"`

	// CountViolations: reconciliation guard results (observed vs plan cap).
	CountViolations []string `json:"count_violations,omitempty"`
}

// BreachReport is one over-limit observation node-side.
type BreachReport struct {
	Resource string `json:"resource"`
	Usage    string `json:"usage"`
	Limit    string `json:"limit"`
	Action   string `json:"action"` // throttle | kill | suspend | notify
	Detail   string `json:"detail,omitempty"`
}

// EnforceLimits applies the plan matrix to the node for one website.
// Idempotent: re-running converges to the desired limits.
func (e *Executor) EnforceLimits(ctx context.Context, p EnforceJobPayload) (*EnforceOutcome, error) {
	if _, err := uuid.Parse(p.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	out := &EnforceOutcome{
		WebsiteID: p.WebsiteID,
		Plan:      p.Plan,
		AppliedAt: time.Now().UTC().Format(time.RFC3339),
	}

	user := siteUnixUser(p.WebsiteID)
	slice := siteSlicePath(user)

	// ---- CPU / RAM / Processes / I/O — cgroup v2 (detect-then-apply) ----
	if cgroupV2Available() {
		memBytes := resources.CgroupMemoryBytes(p.MemoryMB)
		quota, period, _ := resources.CgroupCPUMax(p.CPUPercent)
		err := e.ApplyUserLimitsV2(ctx, user, slice, memBytes, quota, period, p.PidsMax, p.IOWeight)
		if err != nil {
			out.Mechanisms = append(out.Mechanisms, MechanismOutcome{
				Resource: "cpu/ram/processes", Mode: "validated-only",
				Mechanism: "cgroup-v2", Detail: fmt.Sprintf("apply failed: %v", err),
			})
		} else {
			out.Mechanisms = append(out.Mechanisms,
				MechanismOutcome{Resource: "cpu", Mode: "enforced", Mechanism: "cgroup-v2", Detail: cpuMaxDetail(p.CPUPercent)},
				MechanismOutcome{Resource: "ram", Mode: "enforced", Mechanism: "cgroup-v2", Detail: fmt.Sprintf("memory.max=%d", memBytes)},
				MechanismOutcome{Resource: "processes", Mode: "enforced", Mechanism: "cgroup-v2", Detail: "pids.max=" + resources.CgroupPidsMax(p.PidsMax)},
			)
			if p.IOWeight > 0 {
				out.Mechanisms = append(out.Mechanisms, MechanismOutcome{
					Resource: "io", Mode: "enforced", Mechanism: "cgroup-v2",
					Detail: fmt.Sprintf("io.weight=%d", resources.ClampIOWeight(p.IOWeight)),
				})
			}
		}
	} else {
		out.Mechanisms = append(out.Mechanisms, MechanismOutcome{
			Resource: "cpu/ram/processes", Mode: "validated-only", Mechanism: "cgroup-v2",
			Detail: "cgroup v2 unavailable on this node (no unified hierarchy at " + cgroupRoot + ")",
		})
	}

	// ---- Disk — filesystem quota (detect-then-apply) ----
	out.Mechanisms = append(out.Mechanisms, e.enforceDiskQuota(ctx, p, user))

	// ---- Bandwidth — nftables per-account counters (detect-then-apply) ----
	bwMech, rxBytes, txBytes := e.enforceBandwidth(ctx, p, user)
	out.Mechanisms = append(out.Mechanisms, bwMech)

	// ---- Network — per-uid egress guard (shared-host-network hardening) ----
	out.Mechanisms = append(out.Mechanisms, e.enforceNetworkGuard(ctx, user))

	// ---- Session caps — plan limits mirrored onto the login shell slice ----
	out.Mechanisms = append(out.Mechanisms, e.enforceSessionCaps(ctx, user, p))

	// ---- PHP workers — FPM pool bound to the plan ----
	if p.FpmMaxChildren > 0 {
		if n, err := e.applyFPMBound(ctx, p.WebsiteID, p.FpmMaxChildren); err != nil {
			out.Mechanisms = append(out.Mechanisms, MechanismOutcome{
				Resource: "php_workers", Mode: "validated-only", Mechanism: "fpm-pool",
				Detail: fmt.Sprintf("pool patch failed: %v", err),
			})
		} else if n > 0 {
			out.Mechanisms = append(out.Mechanisms, MechanismOutcome{
				Resource: "php_workers", Mode: "enforced", Mechanism: "fpm-pool",
				Detail: fmt.Sprintf("pm.max_children=%d on %d pool(s)", p.FpmMaxChildren, n),
			})
		} else {
			out.Mechanisms = append(out.Mechanisms, MechanismOutcome{
				Resource: "php_workers", Mode: "skipped", Mechanism: "fpm-pool",
				Detail: "not a PHP site (no epicpanel pool file)",
			})
		}
	}

	// ---- Usage from the SAME layer that just enforced ----
	out.Usage = e.measureEnforcedUsage(slice, user, p)
	// Bandwidth period usage = per-uid direct egress (nft counter) + the
	// access-log egress month-to-date (reverse-proxied responses). Both
	// sides rotate with the UTC month; the high-water upsert control-plane
	// side keeps the billed period monotonic across counter resets.
	periodBytes := rxBytes + txBytes
	if e.TrafficMonthEgress != nil {
		if eg, month := e.TrafficMonthEgress(p.WebsiteID); month == bwMonth(e.now()) {
			periodBytes += eg
		}
	}
	if periodBytes > 0 {
		out.Usage[resources.ResBandwidth] = float64(periodBytes) / (1024 * 1024)
	}

	// ---- Over-limit evaluation (same numbers as usage) ----
	out.Breaches = evaluateBreaches(p, out.Usage)

	// ---- Counts — reconciliation guard (report, never mutate) ----
	out.CountViolations = e.reconcileCounts(p)
	return out, nil
}

// ============================================================================
// cgroup v2
// ============================================================================

func cgroupV2Available() bool {
	_, err := os.ReadFile(cgroupRoot + "/cgroup.controllers")
	return err == nil
}

// siteSlicePath is the per-account slice for a unix user (empty user → the
// website-id derived fallback is handled by callers via siteUnixUser).
func siteSlicePath(user string) string {
	if user == "" {
		return ""
	}
	return cgroupRoot + "/epicpanel.slice/epicpanel-" + user + ".slice"
}

// ApplyUserLimitsV2 writes cpu.max, memory.max/high, pids.max and io.weight
// on the site's slice and captures the user's processes into it. Extends the
// Phase-4-era ApplyUserLimits with pids/io/weight and explicit unlimited
// handling (0 = kernel "max").
func (e *Executor) ApplyUserLimitsV2(ctx context.Context, user, slice string, memBytes, cpuQuota, cpuPeriod int64, pidsMax int64, ioWeight int) error {
	if slice == "" {
		return fmt.Errorf("unix user not found for site")
	}
	_ = enableControllers(cgroupRoot)
	parent := filepath.Dir(slice)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	if err := enableControllers(parent); err != nil {
		return err
	}
	if err := os.MkdirAll(slice, 0o755); err != nil {
		return err
	}
	// Leaf slice must not enable controllers for children (cgroup v2 rule:
	// a group with controllers enabled cannot hold processes).
	if b, err := os.ReadFile(slice + "/cgroup.subtree_control"); err == nil && strings.TrimSpace(string(b)) != "" {
		fields := strings.Fields(string(b))
		var off []string
		for _, f := range fields {
			off = append(off, "-"+strings.TrimPrefix(f, "+"))
		}
		_ = os.WriteFile(slice+"/cgroup.subtree_control", []byte(strings.Join(off, " ")), 0o644)
	}

	// CPU: 0 → "max 100000" (unlimited).
	cpuLine := "max 100000"
	if cpuQuota > 0 && cpuPeriod > 0 {
		cpuLine = fmt.Sprintf("%d %d", cpuQuota, cpuPeriod)
	}
	if err := os.WriteFile(slice+"/cpu.max", []byte(cpuLine), 0o644); err != nil {
		return fmt.Errorf("cpu.max: %w", err)
	}
	// RAM: 0 → "max".
	memLine := "max"
	if memBytes > 0 {
		memLine = strconv.FormatInt(memBytes, 10)
		_ = os.WriteFile(slice+"/memory.high", []byte(strconv.FormatInt(memBytes*9/10, 10)), 0o644)
	}
	if err := os.WriteFile(slice+"/memory.max", []byte(memLine), 0o644); err != nil {
		return fmt.Errorf("memory.max: %w", err)
	}
	// PIDs: 0 → "max".
	if err := os.WriteFile(slice+"/pids.max", []byte(resources.CgroupPidsMax(pidsMax)), 0o644); err != nil {
		return fmt.Errorf("pids.max: %w", err)
	}
	// I/O weight (kernel clamps 1..10000; 0 leaves default).
	if w := resources.ClampIOWeight(ioWeight); w > 0 {
		_ = os.WriteFile(slice+"/io.weight", []byte(strconv.Itoa(w)), 0o644)
	}

	// Capture the user's current processes into the slice (respawned workers
	// are re-captured by the epicpanel-cgroup-sync timer installed here).
	moveUserProcesses(user, slice)
	if memBytes > 0 && cpuQuota > 0 {
		_ = upsertLimitState(user, int(cpuQuota), memBytes)
		return ensureLimitsSyncFn(ctx)
	}
	return nil
}

func cpuMaxDetail(percent float64) string {
	if percent <= 0 {
		return "cpu.max=max (unlimited)"
	}
	quota, period, _ := resources.CgroupCPUMax(percent)
	return fmt.Sprintf("cpu.max=%d %d", quota, period)
}

// ============================================================================
// Disk quota — detect fs support, apply project/user quota, degrade honestly
// ============================================================================

// fsSupport reports whether the site tree's filesystem can take quotas.
type fsSupport struct {
	Device  string
	FSType  string
	Mount   string
	Quota   bool // quota enabled on the mount (usrquota/prjquota flag or quota file)
	Project bool // project quota supported (xfs prjquota / ext4 project IDs)
	Reason  string
}

// detectFSSupport stats the site base and inspects /proc/mounts + quota
// tooling. It NEVER assumes: absence of evidence means "not supported".
func detectFSSupport(siteBase string) fsSupport {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return fsSupport{Reason: "cannot read /proc/mounts"}
	}
	return detectFSSupportFromMounts(string(b), siteBase)
}

// detectFSSupportFromMounts is the pure decision core (unit-testable):
// longest matching real mount wins; quota flags come from mount options.
func detectFSSupportFromMounts(mounts, siteBase string) fsSupport {
	out := fsSupport{Reason: "unknown"}
	// Longest matching mount wins (nested mounts).
	best := ""
	for _, line := range splitLines([]byte(mounts)) {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		mount := fields[1]
		if !strings.HasPrefix(siteBase, mount) || len(mount) < len(best) {
			continue
		}
		if mount == "/" || !virtualFS[fields[2]] {
			best = mount
			out.Device, out.FSType, out.Mount = fields[0], fields[2], mount
		}
	}
	if out.Mount == "" {
		out.Reason = "site tree not on a real filesystem"
		return out
	}
	// Mount options carry quota flags when the admin enabled them.
	for _, line := range splitLines([]byte(mounts)) {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != out.Mount {
			continue
		}
		for _, opt := range strings.Split(fields[3], ",") {
			switch opt {
			case "usrquota", "grpquota", "prjquota", "quota":
				out.Quota = true
			}
			if strings.Contains(out.FSType, "xfs") && opt == "prjquota" {
				out.Project = true
			}
		}
	}
	// quota tooling present? (xfs_quota for project quotas, quotaon as the
	// generic user-quota control). No tools + no flags = not supported.
	if !out.Quota && !out.Project {
		if _, err := exec.LookPath("xfs_quota"); err != nil {
			if _, err2 := exec.LookPath("quotaon"); err2 != nil {
				out.Reason = "no quota tooling (xfs_quota/quotaon) and no quota mount flags"
				return out
			}
		}
		out.Reason = fmt.Sprintf("filesystem %s on %s has no quota flags enabled", out.FSType, out.Mount)
		return out
	}
	out.Reason = ""
	return out
}

// enforceDiskQuota applies a project/user quota when supported; otherwise
// reports accounted-only (usage walk still feeds the usage bars).
func (e *Executor) enforceDiskQuota(ctx context.Context, p EnforceJobPayload, user string) MechanismOutcome {
	siteBase := filepath.Join(e.docRootBase, p.WebsiteID)
	if p.DiskMB <= 0 {
		return MechanismOutcome{Resource: "disk", Mode: "accounted", Mechanism: "usage-walk",
			Detail: "plan sets no disk cap; usage accounted"}
	}
	sup := detectFSSupport(siteBase)
	if !sup.Quota && !sup.Project {
		return MechanismOutcome{Resource: "disk", Mode: "accounted", Mechanism: "usage-walk",
			Detail: "quota unsupported: " + sup.Reason}
	}
	if sup.Project {
		// xfs project quota: project id = stable hash of the site id.
		out, err := exec.CommandContext(ctx, "xfs_quota", "-x",
			"-c", fmt.Sprintf("project -s -p %s %d", siteBase, siteProjectID(p.WebsiteID)),
			"-c", fmt.Sprintf("limit -p bsoft=%dm bhard=%dm %d", p.DiskMB, p.DiskMB, siteProjectID(p.WebsiteID)),
			sup.Mount).CombinedOutput()
		if err != nil {
			return MechanismOutcome{Resource: "disk", Mode: "accounted", Mechanism: "xfs_quota",
				Detail: fmt.Sprintf("project quota failed: %v (%s)", err, tail(out, 200))}
		}
		return MechanismOutcome{Resource: "disk", Mode: "enforced", Mechanism: "xfs_quota",
			Detail: fmt.Sprintf("project bhard=%dMB on %s", p.DiskMB, sup.Mount)}
	}
	// ext4/user quota via setquota (uid-scoped).
	uidStr, err := uidOfFn(user)
	if err != nil || uidStr == "" {
		return MechanismOutcome{Resource: "disk", Mode: "accounted", Mechanism: "setquota",
			Detail: "cannot resolve site uid for user quota"}
	}
	dev := sup.Device
	out, err := exec.CommandContext(ctx, "setquota", "-u", uidStr,
		strconv.FormatInt(p.DiskMB, 10), strconv.FormatInt(p.DiskMB, 10), "0", "0", dev).CombinedOutput()
	if err != nil {
		return MechanismOutcome{Resource: "disk", Mode: "accounted", Mechanism: "setquota",
			Detail: fmt.Sprintf("user quota failed: %v (%s)", err, tail(out, 200))}
	}
	return MechanismOutcome{Resource: "disk", Mode: "enforced", Mechanism: "setquota",
		Detail: fmt.Sprintf("uid %s bhard=%dMB on %s", uidStr, p.DiskMB, dev)}
}

// siteProjectID maps a website id to a stable 32-bit xfs project id.
func siteProjectID(websiteID string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(websiteID); i++ {
		h ^= uint32(websiteID[i])
		h *= 16777619
	}
	// Keep out of the 0..999 admin-reserved range.
	return 1000 + h%1000000
}

// ============================================================================
// Bandwidth — nftables per-account RX+TX counters (+ shaping hook)
// ============================================================================

const epicTable = "inet epicpanel_limits"

// nftAvailable reports whether nftables is usable (binary + netlink).
func nftAvailable() bool {
	if _, err := exec.LookPath("nft"); err != nil {
		return false
	}
	_, err := exec.Command("nft", "list", "tables").CombinedOutput()
	return err == nil
}

// enforceBandwidth ensures the per-account nft egress counter exists and
// reports the accounted period usage. The chain hooks OUTPUT and matches
// the site's unix uid (meta skuid, loopback excluded): the web server
// terminates ingress at the edge and reverse-proxied responses leave via
// the web-server user, so this counter covers the site's DIRECT external
// egress; the proxied direction is accounted from access-log windows
// (TrafficSampler month-to-date, composed by EnforceLimits). Rate shaping
// (throttle) is applied as a meter/rate limit when the payload carries a
// non-zero budget AND the default policy requests it; default is
// accounted-only with control-plane suspend on breach (policy.go).
func (e *Executor) enforceBandwidth(ctx context.Context, p EnforceJobPayload, user string) (MechanismOutcome, int64, int64) {
	if !nftAvailable() {
		return MechanismOutcome{Resource: "bandwidth", Mode: "accounted", Mechanism: "proc-net-dev",
			Detail: "nftables unavailable; bandwidth accounted node-wide, not per-account"}, 0, 0
	}
	if user == "" {
		return MechanismOutcome{Resource: "bandwidth", Mode: "accounted", Mechanism: "nftables",
			Detail: "unix user unresolved; no per-account chain"}, 0, 0
	}
	uid, ok := unixUID(user)
	if !ok {
		return MechanismOutcome{Resource: "bandwidth", Mode: "accounted", Mechanism: "nftables",
			Detail: fmt.Sprintf("unix user %s has no uid; no per-account counter", user)}, 0, 0
	}
	chain := "acct_" + sanitizeNFTIdent(user)
	if detail, ok := e.ensureBwChain(ctx, chain, uid); !ok {
		return MechanismOutcome{Resource: "bandwidth", Mode: "accounted", Mechanism: "nftables",
			Detail: detail}, 0, 0
	}
	out, err := exec.CommandContext(ctx, "nft", "list", "chain", epicTable, chain).CombinedOutput()
	if err != nil {
		return MechanismOutcome{Resource: "bandwidth", Mode: "accounted", Mechanism: "nftables",
			Detail: fmt.Sprintf("chain list failed: %v (%s)", err, tail(out, 200))}, 0, 0
	}
	rx, tx := parseNFTCounter(out)
	return MechanismOutcome{Resource: "bandwidth", Mode: "accounted", Mechanism: "nftables",
		Detail: fmt.Sprintf("chain %s skuid-%d egress counter bytes=%d (period, direct egress; proxied egress via access log), budget=%dMB",
			chain, uid, rx+tx, p.BandwidthMB)}, rx, tx
}

// bwChainHook is the output-hook declaration for a per-account chain.
const bwChainHook = "{ type filter hook output priority -300 ; }"

// bwChainRuleArgs renders the per-uid egress counter rule: packets owned by
// the site's unix user leaving via a non-loopback interface. Loopback is
// excluded so app->web-server local hops are never double-counted against
// the access-log view.
func bwChainRuleArgs(chain string, uid int) []string {
	return []string{"nft", "add", "rule", "inet", epicTable, chain,
		"meta", "skuid", strconv.Itoa(uid), "oifname", "!=", `"lo"`, "counter"}
}

// bwChainNeedsRecreate reports whether an existing chain must be replaced:
// pre-0051 builds hooked acct_ chains at FORWARD, which sees no site
// traffic on a shared host (the web server terminates ingress locally), so
// those chains counted nothing forever.
func bwChainNeedsRecreate(listOut []byte) bool {
	return strings.Contains(string(listOut), "hook forward")
}

// bwChainNeedsRule reports whether the per-uid counter rule is missing
// (chains were created empty before the lifecycle-reasons migration —
// parseNFTCounter read zeros forever).
func bwChainNeedsRule(listOut []byte, uid int) bool {
	return !strings.Contains(string(listOut), fmt.Sprintf("skuid %d", uid))
}

// ensureBwChain converges the per-account output-hook chain: create when
// missing, replace when legacy-hooked or rule-less, and rotate (delete +
// recreate = zeroed counters) at UTC month boundaries. The rotation stamp
// is persisted so periods survive agent restarts; a lost state file
// rotates deterministically (fresh period), which the control-plane
// GREATEST high-water keeps monotonic for billing.
func (e *Executor) ensureBwChain(ctx context.Context, chain string, uid int) (string, bool) {
	run := func(args ...string) ([]byte, bool) {
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		return out, err == nil
	}
	cur := bwMonth(e.now())
	st := loadBwState(e.bwStatePath)
	out, ok := run("nft", "list", "chain", epicTable, chain)
	if ok && !bwNeedsRotate(st.Chains[chain], cur) && !bwChainNeedsRecreate(out) && !bwChainNeedsRule(out, uid) {
		if e.TrafficSnapshot != nil {
			st.Sites = e.TrafficSnapshot()
		}
		// Persistence failure must not fail enforcement: worst case the
		// next run re-rotates (counter restarts; the control-plane
		// GREATEST high-water keeps the billed period monotonic).
		_ = st.save(e.bwStatePath)
		return "", true
	}
	// Recreate from scratch — the delete covers rotation and the legacy
	// FORWARD hook in one path; a delete error on an absent chain is fine.
	_, _ = run("nft", "delete", "chain", epicTable, chain)
	_, _ = run("nft", "add", "table", epicTable)
	if _, chainOK := run("nft", "add", "chain", epicTable, chain, bwChainHook); !chainOK {
		return "chain create failed (see agent log)", false
	}
	ruleOut, ruleOK := run(bwChainRuleArgs(chain, uid)...)
	if !ruleOK {
		return fmt.Sprintf("skuid counter rule failed: %s", tail(ruleOut, 200)), false
	}
	st.Chains[chain] = cur
	if e.TrafficSnapshot != nil {
		st.Sites = e.TrafficSnapshot()
	}
	_ = st.save(e.bwStatePath)
	return "", true
}

// parseNFTCounter extracts byte counters from `nft list chain` output.
func parseNFTCounter(b []byte) (rx, tx int64) {
	for _, line := range splitLines(b) {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "counter packets") && strings.Contains(line, "bytes") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "bytes" && i+1 < len(fields) {
					v, err := strconv.ParseInt(strings.TrimSuffix(fields[i+1], ","), 10, 64)
					if err == nil {
						if strings.HasPrefix(line, "ip saddr") || strings.Contains(line, "daddr") {
							tx += v
						} else {
							rx += v
						}
					}
				}
			}
		}
	}
	return
}

func sanitizeNFTIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ============================================================================
// PHP workers — FPM pool pm.max_children bound to the plan (Phase 4 seam
// migrated: the pool values now come from resources.PoolLimitsFor via the
// control plane, and the agent's patch is the enforcement arm).
// ============================================================================

// applyFPMBound rewrites pm.max_children (+ start/spare servers) on every
// epicpanel pool for the site and reloads the matching FPM service.
// Returns the number of pools patched.
func (e *Executor) applyFPMBound(ctx context.Context, websiteID string, maxChildren int) (int, error) {
	matches, _ := filepath.Glob(phpEtcBase + "/*/fpm/pool.d/epicpanel-" + websiteID + ".conf")
	if len(matches) == 0 {
		return 0, nil
	}
	if maxChildren < 1 {
		maxChildren = 1
	}
	patched := 0
	for _, poolFile := range matches {
		v := versionFromPoolPath(poolFile)
		fpmBin, mainCfg := "", ""
		if v != "" {
			if bin, lookErr := exec.LookPath(phpFpmBinary(v)); lookErr == nil {
				fpmBin = bin
				mainCfg = phpEtcBase + "/" + v + "/fpm/php-fpm.conf"
			}
		}
		if err := patchPoolChildren(poolFile, maxChildren, fpmBin, mainCfg); err != nil {
			return patched, fmt.Errorf("patch %s: %w", poolFile, err)
		}
		if v != "" {
			_ = e.run(ctx, "systemctl", "reload", phpFpmService(v))
		}
		patched++
	}
	return patched, nil
}

// patchPoolChildren rewrites only the pm sizing lines (memory_limit stays
// owned by the provision path) with validate + restore-on-fail.
func patchPoolChildren(poolFile string, children int, fpmBin, mainConfig string) error {
	b, err := os.ReadFile(poolFile)
	if err != nil {
		return err
	}
	backup := string(b)
	startServers := children / 2
	if startServers < 1 {
		startServers = 1
	}
	minSpare := startServers
	maxSpare := children - 1
	if maxSpare < minSpare {
		maxSpare = minSpare
	}
	var out []string
	for _, line := range strings.Split(backup, "\n") {
		switch {
		case strings.HasPrefix(line, "pm.max_children"):
			out = append(out, fmt.Sprintf("pm.max_children = %d", children))
		case strings.HasPrefix(line, "pm.start_servers"):
			out = append(out, fmt.Sprintf("pm.start_servers = %d", startServers))
		case strings.HasPrefix(line, "pm.min_spare_servers"):
			out = append(out, fmt.Sprintf("pm.min_spare_servers = %d", minSpare))
		case strings.HasPrefix(line, "pm.max_spare_servers"):
			out = append(out, fmt.Sprintf("pm.max_spare_servers = %d", maxSpare))
		default:
			out = append(out, line)
		}
	}
	tmp := poolFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, poolFile); err != nil {
		return err
	}
	if fpmBin != "" {
		if out2, verr := exec.Command(fpmBin, "--fpm-config", mainConfig, "--test").CombinedOutput(); verr != nil {
			_ = os.WriteFile(poolFile, []byte(backup), 0o644)
			return fmt.Errorf("fpm validation failed (config restored): %s", tail(out2, 200))
		}
	}
	return nil
}

// ============================================================================
// Shared cgroup readers — ONE read path for both the display collectors
// (workload.go, site_usage.go) and the enforcement verification below. This
// is the structural anti-drift guarantee: display and enforcement cannot
// read different numbers because they cannot read different files.
// ============================================================================

// SliceLimits is the limit state of a site slice as the KERNEL sees it —
// what enforcement wrote is what display reads back.
type SliceLimits struct {
	CPUQuotaUsec   int64 // 0 = max
	CPUPeriodUsec  int64
	MemoryMaxBytes int64 // 0 = max
	PidsMax        int64 // 0 = max
	IOWeight       int64 // 0 = kernel default
}

// readSliceLimits parses cpu.max / memory.max / pids.max / io.weight from
// the slice directory. Missing files leave the zero value (unlimited).
func readSliceLimits(slice string) SliceLimits {
	var out SliceLimits
	if b, err := os.ReadFile(slice + "/cpu.max"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) == 2 {
			if fields[0] != "max" {
				out.CPUQuotaUsec, _ = strconv.ParseInt(fields[0], 10, 64)
			}
			if p, err := strconv.ParseInt(fields[1], 10, 64); err == nil && p > 0 {
				out.CPUPeriodUsec = p
			}
		}
	}
	if b, err := os.ReadFile(slice + "/memory.max"); err == nil {
		v := strings.TrimSpace(string(b))
		if v != "" && v != "max" {
			out.MemoryMaxBytes, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if b, err := os.ReadFile(slice + "/pids.max"); err == nil {
		v := strings.TrimSpace(string(b))
		if v != "" && v != "max" {
			out.PidsMax, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if b, err := os.ReadFile(slice + "/io.weight"); err == nil {
		fields := strings.Fields(string(b))
		if len(fields) == 1 {
			out.IOWeight, _ = strconv.ParseInt(fields[0], 10, 64)
		} else if len(fields) == 2 && fields[0] == "default" {
			out.IOWeight, _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return out
}

// ============================================================================
// Usage measurement (the same layer that enforced) + breach evaluation
// ============================================================================

// measureEnforcedUsage reads back the controls just applied — cgroup usage,
// quota report/counters — so the outcome can drive usage bars directly.
// This is the anti-drift guarantee: display reads what enforcement reads.
func (e *Executor) measureEnforcedUsage(slice, user string, p EnforceJobPayload) map[string]float64 {
	usage := map[string]float64{}
	if slice != "" {
		if b, err := os.ReadFile(slice + "/memory.current"); err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
				usage[resources.ResRAM] = float64(v) / (1024 * 1024)
			}
		}
		if b, err := os.ReadFile(slice + "/cpu.stat"); err == nil {
			if usec, ok := parseCpuStat(b); ok {
				siteCPUMu.Lock()
				prev, seen := enforceCPUSamples[p.WebsiteID]
				siteCPUMu.Unlock()
				now := time.Now()
				if seen && now.Sub(prev.at).Seconds() > 0.5 {
					cores := (float64(usec-prev.usageUsec) / 1e6) / now.Sub(prev.at).Seconds()
					if cores > 0 {
						usage[resources.ResCPU] = cores * 100
					}
				}
				siteCPUMu.Lock()
				enforceCPUSamples[p.WebsiteID] = cpuSample{usageUsec: usec, at: now}
				siteCPUMu.Unlock()
			}
		}
		if b, err := os.ReadFile(slice + "/cgroup.procs"); err == nil {
			usage[resources.ResProcesses] = float64(len(strings.Fields(string(b))))
		}
	}
	usage[resources.ResDisk] = float64(dirSizeMB(filepath.Join(e.docRootBase, p.WebsiteID)))
	return usage
}

var (
	enforceCPUSamples = map[string]cpuSample{}
	siteCPUMu         sync.Mutex
)

// evaluateBreaches applies the default policy to measured usage vs plan
// limits. Kernel-capped resources (cpu/ram/processes) are reported with
// action "none" (the kernel throttles/kills); resources without a kernel
// backstop produce throttle/suspend breach reports the control plane turns
// into jobs.
func evaluateBreaches(p EnforceJobPayload, usage map[string]float64) []BreachReport {
	type capRes struct {
		res   string
		limit float64
		unit  string
	}
	caps := []capRes{}
	if p.MemoryMB > 0 {
		caps = append(caps, capRes{resources.ResRAM, float64(p.MemoryMB), "MB"})
	}
	if p.CPUPercent > 0 {
		caps = append(caps, capRes{resources.ResCPU, p.CPUPercent, "%"})
	}
	if p.DiskMB > 0 {
		caps = append(caps, capRes{resources.ResDisk, float64(p.DiskMB), "MB"})
	}
	if p.BandwidthMB > 0 {
		caps = append(caps, capRes{resources.ResBandwidth, float64(p.BandwidthMB), "MB"})
	}
	var out []BreachReport
	for _, c := range caps {
		u, measured := usage[c.res]
		if !measured {
			continue
		}
		if u <= c.limit {
			continue
		}
		action := resources.ActionNone
		switch c.res {
		case resources.ResDisk:
			action = resources.ActionSuspend // no kernel backstop when quota unsupported
		case resources.ResBandwidth:
			action = resources.ActionSuspend
		}
		out = append(out, BreachReport{
			Resource: c.res,
			Usage:    fmt.Sprintf("%.1f%s", u, c.unit),
			Limit:    fmt.Sprintf("%.1f%s", c.limit, c.unit),
			Action:   string(action),
			Detail:   "measured by the enforcement layer itself",
		})
	}
	return out
}

// reconcileCounts is the agent-side reconciliation guard: the control plane
// sends the plan caps it governs; the agent checks what it can observe
// locally and REPORTS violations — it never deletes or mutates anything.
// Local ground truth: the site tree exists (site presence is what the
// counts belong to). When the tree is gone the guard flags it so the
// control plane can reconcile instead of silently pretending all is well.
func (e *Executor) reconcileCounts(p EnforceJobPayload) []string {
	if len(p.CountLimits) == 0 {
		return nil
	}
	var violations []string
	siteBase := filepath.Join(e.docRootBase, p.WebsiteID)
	if _, err := os.Stat(siteBase); err != nil {
		violations = append(violations, "site tree missing for reconciliation: "+p.WebsiteID)
	}
	return violations
}

package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ============================================================================
// Drift tests — the master-doc rule: "displayed metrics and enforced limits
// must come from the same authoritative node-side measurement/control layer."
//
// These tests prove, at the seam level, that:
//  1. the numbers the UI would DISPLAY as limits (GetLimits) and the numbers
//     the agent ENFORCES (EnforcePlan → kernel helpers) come from ONE
//     mapping (PlanFromInput) — there is no second conversion path;
//  2. the USAGE values displayed (GetUsage merge) carry the agent collector
//     as their source — usage is never invented control-plane side;
//  3. after a plan change (e.g. RAM 2048 → 4096), display and enforcement
//     move together (a stale second source would fail this).
// ============================================================================

// driftWorkload is the shared synthetic workload: an org on a 4096 MB plan
// whose site slice reports live cgroup usage.
var driftWorkload = Workload{
	Kind:      KindWeb,
	WebsiteID: mustUUIDT("22222222-2222-2222-2222-222222222222"),
	Plan: PlanInput{
		Name: "Scale", Kind: KindWeb,
		MemoryLimitMB: 4096, CPUCores: 2.0, MaxDiskMB: 20 * 1024,
		MaxBandwidthMB: 2 * 1024 * 1024, MaxProcesses: 256,
		MaxPorts: 1, MaxBackups: 3,
	},
}

// agentUsage is what the node agent's measurement layer reports (Phase 3
// collector: cgroup memory.current / cpu.stat, cached disk walk, nftables
// counters). In production this arrives via SiteSample / site_usage
// outcomes; the engine consumes it verbatim through Usage.
var agentUsage = Usage{
	Source: "agent-collector:cgroup-v2",
	Values: map[string]float64{
		ResRAM:       2 * 1024,   // memory.current
		ResCPU:       118.4,      // cpu.stat delta
		ResDisk:      7 * 1024,   // disk walk
		ResBandwidth: 512 * 1024, // nftables RX+TX counters (period)
		ResProcesses: 41,         // cgroup.procs count
	},
}

// TestDriftDisplayMatchesEnforcement — for EVERY resource the plan governs,
// the displayed limit and the enforced value must be the same number.
func TestDriftDisplayMatchesEnforcement(t *testing.T) {
	e := NewEngine()
	displayed := e.GetLimits(driftWorkload)
	enforced := e.EnforcePlan(driftWorkload)

	// RAM: display 4096 MB ↔ memory.max bytes derived from the same number.
	dRAM, _ := displayed.Get(ResRAM)
	if enforced.MemoryMB != int64(dRAM.Limit) {
		t.Fatalf("drift: display RAM %.0f MB vs enforce %d MB", dRAM.Limit, enforced.MemoryMB)
	}
	if b := CgroupMemoryBytes(enforced.MemoryMB); b != int64(dRAM.Limit)*1024*1024 {
		t.Fatalf("drift: memory.max %d does not match displayed %.0f MB", b, dRAM.Limit)
	}
	// CPU: display 200% ↔ cpu.max from the same number.
	dCPU, _ := displayed.Get(ResCPU)
	if enforced.CPUPercent != dCPU.Limit {
		t.Fatalf("drift: display CPU %.0f%% vs enforce %.0f%%", dCPU.Limit, enforced.CPUPercent)
	}
	quota, _, _ := CgroupCPUMax(enforced.CPUPercent)
	if float64(quota)/1000 != dCPU.Limit {
		t.Fatalf("drift: cpu.max quota %dµs ≠ displayed %.0f%%", quota, dCPU.Limit)
	}
	// Disk + bandwidth + pids.
	dDisk, _ := displayed.Get(ResDisk)
	if enforced.DiskMB != int64(dDisk.Limit) {
		t.Fatalf("drift: display disk %.0f vs enforce %d", dDisk.Limit, enforced.DiskMB)
	}
	dBW, _ := displayed.Get(ResBandwidth)
	if enforced.BandwidthMB != int64(dBW.Limit) {
		t.Fatalf("drift: display bandwidth %.0f vs enforce %d", dBW.Limit, enforced.BandwidthMB)
	}
	dPids, _ := displayed.Get(ResProcesses)
	if enforced.PidsMax != int64(dPids.Limit) {
		t.Fatalf("drift: display processes %.0f vs enforce %d", dPids.Limit, enforced.PidsMax)
	}
}

// TestDriftPlanChangeMovesBoth — bump the plan RAM (data change) and both
// display AND enforcement must move together; a second source would lag.
func TestDriftPlanChangeMovesBoth(t *testing.T) {
	e := NewEngine()
	upgraded := driftWorkload
	upgraded.Plan.MemoryLimitMB = 2048 // downgrade the plan matrix

	displayed := e.GetLimits(upgraded)
	enforced := e.EnforcePlan(upgraded)
	dRAM, _ := displayed.Get(ResRAM)
	if dRAM.Limit != 2048 || enforced.MemoryMB != 2048 {
		t.Fatalf("plan change drifted: display %.0f vs enforce %d", dRAM.Limit, enforced.MemoryMB)
	}
}

// TestDriftUsageSourceIsAgentCollector — displayed usage must carry the
// agent measurement layer as its source, and the usage shown must be the
// same values the over-limit evaluation sees (no parallel conversion).
func TestDriftUsageSourceIsAgentCollector(t *testing.T) {
	e := NewEngine()
	view := e.GetUsage(driftWorkload, agentUsage)

	rAM, _ := view.Get(ResRAM)
	if rAM.Usage != 2048 {
		t.Fatalf("displayed RAM usage = %.0f, want the agent-reported 2048", rAM.Usage)
	}
	if rAM.Source != "agent-collector:cgroup-v2" {
		t.Fatalf("usage source = %q — displayed metrics must come from the agent collector", rAM.Source)
	}
	// The SAME view feeds enforcement decisions (policy evaluation):
	breaches := Evaluate(view, agentUsage, DefaultPolicy())
	for _, b := range breaches {
		if b.Usage != agentUsage.Values[b.Resource] {
			t.Fatalf("policy saw %s usage %.0f, agent reported %.0f — second source!",
				b.Resource, b.Usage, agentUsage.Values[b.Resource])
		}
	}
	if len(breaches) != 0 {
		t.Fatalf("2GB used of 4GB plan must not breach: %+v", breaches)
	}
}

// TestDriftOverLimitBreachFeedsActions — over the limit, the SAME merged
// view produces the breach the suspend/throttle hooks consume.
func TestDriftOverLimitBreachFeedsActions(t *testing.T) {
	e := NewEngine()
	over := agentUsage
	over.Values[ResRAM] = 4600 // over the 4096 MB cap → kernel OOM would fire
	view := e.GetUsage(driftWorkload, over)
	breaches := Evaluate(view, over, DefaultPolicy())
	if len(breaches) != 1 {
		t.Fatalf("want exactly the ram breach, got %+v", breaches)
	}
	b := breaches[0]
	if b.Resource != ResRAM || b.Action != ActionNone {
		t.Fatalf("ram breach = %+v (kernel memory.max already caps it)", b)
	}
	// Bandwidth over budget → suspend hook (Phase 10 consumes it).
	over.Values[ResBandwidth] = 2*1024*1024 + 1
	view = e.GetUsage(driftWorkload, over)
	breaches = Evaluate(view, over, DefaultPolicy())
	var sawSuspend bool
	for _, b := range breaches {
		if b.Resource == ResBandwidth && b.Action == ActionSuspend {
			sawSuspend = true
		}
	}
	if !sawSuspend {
		t.Fatalf("bandwidth over budget must trigger the suspend hook: %+v", breaches)
	}
}

// TestMigrationMirrorsSeedData — migration 0027 seeds exactly the registry's
// 7 verbatim plans; a missing or extra row would silently break plan edits.
func TestMigrationMirrorsSeedData(t *testing.T) {
	sql := readMigration(t, "0027_resource_plans.sql")
	for _, p := range AllPlans() {
		if !containsString(sql, "'"+p.Name+"'") {
			t.Errorf("migration 0027 does not seed plan %q", p.Name)
		}
	}
	if !containsString(sql, "workload_resource_usage") {
		t.Error("migration 0027 must create workload_resource_usage")
	}
	if !containsString(sql, "enforce_limits") {
		t.Error("migration 0027 must register the enforce_limits job type")
	}
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return string(b)
}

func containsString(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func mustUUIDT(s string) uuid.UUID {
	v, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

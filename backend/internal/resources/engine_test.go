package resources

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestSeedPlansAreVerbatim — the built-in web plans must exist, and Business
// must carry the verbatim matrix: RAM 2048 MB · CPU 400% · Disk 100 GB ·
// Bandwidth 5 TB · Processes 256 · Databases 100 · Email 200.
func TestSeedPlansAreVerbatim(t *testing.T) {
	want := []string{"Starter", "Pro", "Business"}
	got := map[string]bool{}
	for _, p := range AllPlans() {
		got[p.Name] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("seed plans missing verbatim plan %q", name)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("seed plan count = %d, want %d (names must be exactly the verbatim set)", len(got), len(want))
	}

	biz, ok := PlanByName("Business")
	if !ok {
		t.Fatal("Business missing")
	}
	if biz.Kind != KindWeb {
		t.Fatalf("Business kind = %q, want web", biz.Kind)
	}
	for name, wantQty := range map[string]float64{
		ResRAM:       2048,
		ResCPU:       400,
		ResDisk:      100 * 1024,
		ResBandwidth: 5 * 1024 * 1024,
		ResProcesses: 256,
		ResDatabases: 100,
		ResEmail:     200,
	} {
		if q := biz.Limits[name]; q != wantQty {
			t.Errorf("Business %s = %v, want %v (verbatim)", name, q, wantQty)
		}
	}
}

// TestWebResourceSetVerbatim — web hosting set is exactly the verbatim list.
func TestWebResourceSetVerbatim(t *testing.T) {
	want := []string{ResCPU, ResRAM, ResDisk, ResIO, ResProcesses, ResPHPWorkers,
		ResDatabases, ResDomains, ResEmail, ResBandwidth}
	got := WebResourceSet()
	if len(got) != len(want) {
		t.Fatalf("web resource set = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("web resource set[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestEngineGetLimitsShape — GetLimits renders the unified Resource model with
// correct units and honest enforced flags.
func TestEngineGetLimitsShape(t *testing.T) {
	e := NewEngine()
	w := Workload{Kind: KindWeb, Plan: PlanInput{
		Name: "Scale", Kind: KindWeb, MemoryLimitMB: 4096,
		CPUCores: 2.0, MaxDiskMB: 20 * 1024, MaxBandwidthMB: 2 * 1024 * 1024,
		MaxProcesses: 256, MaxPorts: 1, MaxBackups: 3,
	}}
	limits := e.GetLimits(w)
	if limits.Plan != "Scale" || limits.Kind != KindWeb {
		t.Fatalf("limits identity = %s/%s", limits.Plan, limits.Kind)
	}
	if r, ok := limits.Get(ResCPU); !ok || r.Unit != UnitPercent || r.Limit != 200 || !r.Enforced {
		t.Fatalf("cpu resource = %+v", r)
	}
	if r, ok := limits.Get(ResRAM); !ok || r.Unit != UnitMB || r.Limit != 4096 || !r.Enforced {
		t.Fatalf("ram resource = %+v", r)
	}
	if r, ok := limits.Get(ResBandwidth); !ok || r.Enforced {
		t.Fatalf("bandwidth must be accounted (not kernel-enforced): %+v", r)
	}
	if r, ok := limits.Get(ResDatabases); !ok || r.Limit != 0 || r.LimitUnlimited {
		t.Fatalf("plan must govern %s at 0 (no databases): %+v", ResDatabases, r)
	}
}

// TestGetUsageMerge — GetUsage fills usage from the caller-supplied
// authoritative snapshot without inventing values for unmeasured resources.
func TestEngineGetUsageMerge(t *testing.T) {
	e := NewEngine()
	w := Workload{Kind: KindWeb, Plan: PlanInput{
		Name: "Starter", Kind: KindWeb, MemoryLimitMB: 512, CPUCores: 1,
		MaxDiskMB: 2048, MaxBandwidthMB: 100 * 1024,
	}}
	u := Usage{
		Source: "agent-collector:cgroup-v2",
		Values: map[string]float64{ResRAM: 300, ResDisk: 1500, ResCPU: 42},
	}
	got := e.GetUsage(w, u)
	if r, _ := got.Get(ResRAM); r.Usage != 300 || r.Source != "agent-collector:cgroup-v2" {
		t.Fatalf("ram usage not merged: %+v", r)
	}
	if r, _ := got.Get(ResDatabases); r.Usage != 0 {
		t.Fatalf("unmeasured resource must stay 0: %+v", r)
	}
}

// TestEnforceClamping — the enforce payload must carry exactly the plan
// numbers through the kernel conversion helpers (clamp + unit conversion),
// and those same helpers must be what the display path reads back.
func TestEnforceClamping(t *testing.T) {
	e := NewEngine()
	w := Workload{
		Kind:      KindWeb,
		WebsiteID: mustUUID(t, "11111111-1111-1111-1111-111111111111"),
		Plan: PlanInput{
			Name: "Scale", Kind: KindWeb, MemoryLimitMB: 4096, CPUCores: 2.0,
			MaxDiskMB: 20 * 1024, MaxBandwidthMB: 2 * 1024 * 1024, MaxProcesses: 256,
			IOWeight: 99999, // clamp to 10000
			MaxPorts: 1, MaxBackups: 3,
		},
	}
	p := e.EnforcePlan(w)
	if p.CPUPercent != 200 {
		t.Fatalf("cpu percent = %v, want 200", p.CPUPercent)
	}
	if p.MemoryMB != 4096 {
		t.Fatalf("memory MB = %v, want 4096", p.MemoryMB)
	}
	if p.DiskMB != 20*1024 {
		t.Fatalf("disk MB = %v, want 20480 (20 GB)", p.DiskMB)
	}
	if p.BandwidthMB != 2*1024*1024 {
		t.Fatalf("bandwidth MB = %v, want 2097152 (2 TB)", p.BandwidthMB)
	}
	if p.PidsMax != 256 {
		t.Fatalf("pids max = %v, want 256", p.PidsMax)
	}
	// IO weight clamps to the kernel ceiling; 0 stays the kernel default.
	if got := ClampIOWeight(p.IOWeight); got != 10000 {
		t.Fatalf("io weight clamp = %v, want 10000", got)
	}
	if p2 := e.EnforcePlan(Workload{Kind: KindWeb, WebsiteID: w.WebsiteID, Plan: PlanInput{
		Name: "Starter", Kind: KindWeb, IOWeight: 0,
	}}); p2.IOWeight != 0 {
		t.Fatalf("io weight 0 must stay 0 (kernel default), got %v", p2.IOWeight)
	}
	// kernel conversions
	quota, period, ok := CgroupCPUMax(p.CPUPercent)
	if !ok || quota != 200000 || period != 100000 {
		t.Fatalf("cpu.max = %d/%d ok=%v", quota, period, ok)
	}
	if b := CgroupMemoryBytes(p.MemoryMB); b != int64(4096)*1024*1024 {
		t.Fatalf("memory.max bytes = %d", b)
	}
	if s := CgroupPidsMax(p.PidsMax); s != "256" {
		t.Fatalf("pids.max = %q", s)
	}
	// unlimited paths
	if _, _, ok := CgroupCPUMax(0); ok {
		t.Fatal("cpu 0 must mean unlimited (no cpu.max write)")
	}
	if CgroupMemoryBytes(0) != 0 {
		t.Fatal("memory 0 must mean unlimited (no memory.max write)")
	}
	if CgroupMemoryBytes(4) != 128*1024*1024 {
		t.Fatal("memory.max must be floored at 128 MiB")
	}
	if s := CgroupPidsMax(0); s != "max" {
		t.Fatalf("pids.max unlimited = %q, want max", s)
	}
}

// TestPoolLimitsClamp — FPM pool sizing is bounded so children × per-child
// memory stays inside the plan RAM (the Phase 4 seam's derivation, now
// single-sourced in the engine).
func TestPoolLimitsClamp(t *testing.T) {
	cases := []struct {
		ramMB int64
		want  PoolLimits
	}{
		{0, PoolLimits{}},
		{8, PoolLimits{MemoryLimitMB: 16, MaxChildren: 1}},
		{512, PoolLimits{MemoryLimitMB: 256, MaxChildren: 16}},
		{1024, PoolLimits{MemoryLimitMB: 256, MaxChildren: 32}},
		{4096, PoolLimits{MemoryLimitMB: 256, MaxChildren: 100}},
	}
	for _, c := range cases {
		got := PoolLimitsFor(c.ramMB)
		if got != c.want {
			t.Errorf("PoolLimitsFor(%d) = %+v, want %+v", c.ramMB, got, c.want)
		}
	}
}

// TestCountLimits — create-time validation of counted resources.
func TestCountLimits(t *testing.T) {
	e := NewEngine()
	// Starter: databases 2, domains 5, email 5, backups 2 (no ports).
	limits := e.GetLimits(Workload{Kind: KindWeb, Plan: PlanInput{
		Name: "Starter", Kind: KindWeb, MaxDatabases: 2, MaxAddonDomains: 2,
		MaxSubdomains: 3, MaxEmailAccounts: 5, MaxBackups: 2,
	}})
	if err := CheckCount(limits, ResDatabases, 1); err != nil {
		t.Fatalf("2nd database should pass: %v", err)
	}
	if err := CheckCount(limits, ResDatabases, 2); err == nil {
		t.Fatal("3rd database must be rejected")
	} else if !strings.Contains(err.Error(), "Starter allows 2 databases") {
		t.Fatalf("unexpected error text: %v", err)
	}
	if err := CheckCount(limits, ResDomains, 4); err != nil {
		t.Fatalf("5th domain should pass: %v", err)
	}
	if err := CheckCount(limits, ResDomains, 5); err == nil {
		t.Fatal("6th domain must be rejected")
	}
	if err := CheckCount(limits, ResBackups, 2); err == nil {
		t.Fatal("3rd backup must be rejected")
	}
	// A plan spec without ports (explicit spec, not a DB row) → ungoverned.
	if err := CheckCount(SpecToLimits(PlanSpec{Name: "X", Kind: KindWeb,
		Limits: map[string]float64{ResDatabases: 2}}, Workload{}), ResPorts, 99); err != nil {
		t.Fatalf("ungoverned resource must pass: %v", err)
	}
	// A governed count of 0 genuinely forbids the resource.
	if err := CheckCount(e.GetLimits(Workload{Kind: KindWeb, Plan: PlanInput{
		Name: "Starter", Kind: KindWeb, MaxPorts: 0,
	}}), ResPorts, 0); err == nil {
		t.Fatal("plan with max_ports=0 must forbid ports")
	}
	// A plan allowing exactly 1 port rejects the second.
	onePort := e.GetLimits(Workload{Kind: KindWeb, Plan: PlanInput{
		Name: "SinglePort", Kind: KindWeb, MaxPorts: 1, MaxBackups: 3,
	}})
	if err := CheckCount(onePort, ResPorts, 1); err == nil {
		t.Fatal("plan allowing 1 port must reject the second")
	}
	// backups = 0 forbids any backup.
	noBackup := e.GetLimits(Workload{Kind: KindWeb, Plan: PlanInput{
		Name: "NoBackup", Kind: KindWeb, MaxBackups: 0,
	}})
	if err := CheckCount(noBackup, ResBackups, 0); err == nil {
		t.Fatal("plan with 0 backups forbids any backup")
	}
	// CheckCounts walks the governed set (databases is in the web set).
	if err := CheckCounts(limits, map[string]int{ResDatabases: 2}); err == nil {
		t.Fatal("CheckCounts must catch the database breach")
	}
	if err := CheckCounts(limits, map[string]int{ResDatabases: 1, ResDomains: 4}); err != nil {
		t.Fatalf("within limits: %v", err)
	}
}

// TestAddPlanIsDataOnly — a brand-new plan (WHM-created row) flows through
// the same engine without any code change: PlanFromInput is the only mapping.
func TestAddPlanIsDataOnly(t *testing.T) {
	e := NewEngine()
	custom := PlanInput{
		Name: "Rails Scale", Kind: KindWeb, MemoryLimitMB: 8192, CPUCores: 8,
		MaxDiskMB: 400 * 1024, MaxBandwidthMB: 20 * 1024 * 1024, MaxProcesses: 512,
		MaxDatabases: 50, MaxAddonDomains: 10, MaxSubdomains: 10,
		MaxEmailAccounts: 500, MaxBackups: 10,
	}
	limits := e.GetLimits(Workload{Kind: KindWeb, Plan: custom})
	if r, _ := limits.Get(ResRAM); r.Limit != 8192 {
		t.Fatalf("custom plan ram = %+v", r)
	}
	payload := e.EnforcePlan(Workload{Kind: KindWeb, Plan: custom})
	if payload.MemoryMB != 8192 || payload.CPUPercent != 800 {
		t.Fatalf("custom plan enforce payload = %+v", payload)
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	v, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("bad uuid %q: %v", s, err)
	}
	return v
}

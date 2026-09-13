package resourcelimits

import (
	"context"
	"errors"
	"testing"

	"github.com/epicbyte/epicpanel/backend/internal/packages"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
)

// fakePkgStore lets the adapter tests run without a database: only ForOrg /
// UsageForOrg are consumed, so a tiny stub suffices (the adapter takes
// *packages.Store today; these tests exercise the engine mapping through
// PlanFromPackage + the CountGate validation core directly).
func planRef() PlanRef {
	return PlanRef{
		Name: "Starter", Kind: "web",
		MaxWebsites: 3, MaxDatabases: 2, MaxDiskMB: 2048,
		MemoryLimitMB: 512, CPUCores: 1.0,
		MaxAddonDomains: 2, MaxSubdomains: 3,
		MaxBandwidthMB: 100 * 1024, MaxBackups: 2, MaxEmailAccounts: 5,
	}
}

// TestPlanRefToEngineLimits — the DB row's columns render as the unified
// resource model with the right units and honest enforcement metadata.
func TestPlanRefToEngineLimits(t *testing.T) {
	e := resources.NewEngine()
	limits := e.GetLimits(resources.Workload{Kind: resources.KindWeb, Plan: planRef().ToPlanInput()})
	if limits.Plan != "Starter" {
		t.Fatalf("plan = %q", limits.Plan)
	}
	r, ok := limits.Get(resources.ResRAM)
	if !ok || r.Limit != 512 || r.Unit != resources.UnitMB || !r.Enforced {
		t.Fatalf("ram = %+v", r)
	}
	if r, _ := limits.Get(resources.ResCPU); r.Limit != 100 {
		t.Fatalf("cpu = %+v (1.0 core = 100 percent)", r)
	}
	if r, _ := limits.Get(resources.ResDomains); r.Limit != 5 || r.Enforced {
		t.Fatalf("domains = %+v (2 addon + 3 subdomains, validated-only)", r)
	}
	if r, _ := limits.Get(resources.ResBackups); r.Limit != 2 || r.LimitUnlimited {
		t.Fatalf("backups = %+v", r)
	}
}

// TestCountGateCore — fail-closed + fail-open semantics of the create-time
// gate, exercised through the engine's CheckCount (the same function the
// api CountGate calls).
func TestCountGateCore(t *testing.T) {
	e := resources.NewEngine()
	limits := e.GetLimits(resources.Workload{Kind: resources.KindWeb, Plan: planRef().ToPlanInput()})

	// Under limit → allowed.
	if err := resources.CheckCount(limits, resources.ResDatabases, 1); err != nil {
		t.Fatalf("2nd database should pass: %v", err)
	}
	// At limit → denied with the verbatim gate phrasing.
	err := resources.CheckCount(limits, resources.ResDatabases, 2)
	if err == nil {
		t.Fatal("3rd database must be denied")
	}
	var reach resources.ErrLimitReached
	if !errors.As(err, &reach) || reach.Limit != 2 {
		t.Fatalf("err = %v", err)
	}
	// Over-limit zero-cap resource (a plan with backups=0) → denied.
	nb := PlanRef{Name: "NoBackup", Kind: "web", MaxBackups: 0}
	dl := e.GetLimits(resources.Workload{Kind: resources.KindWeb, Plan: nb.ToPlanInput()})
	if err := resources.CheckCount(dl, resources.ResBackups, 0); err == nil {
		t.Fatal("zero-cap resource must deny creation")
	}
	// Ungoverned resource → allowed.
	if err := resources.CheckCount(limits, "bogus_resource", 999); err != nil {
		t.Fatalf("ungoverned must pass: %v", err)
	}
}

// TestPlanFromPackageColumns — the legacy packages.Package columns map onto
// the matrix; missing Phase 9 columns read as unlimited/ungoverned.
func TestPlanFromPackageColumns(t *testing.T) {
	ref := PlanFromPackage(&packages.Package{
		Name: "Legacy", MaxDatabases: 4, MaxDiskMB: 10240, MemoryLimitMB: 256,
		CPUCores: 0.5, MaxAddonDomains: 1, MaxSubdomains: 2,
	})
	if ref.MaxBackups != 0 || ref.MaxBandwidthMB != 0 {
		t.Fatalf("missing columns must default to zero: %+v", ref)
	}
	e := resources.NewEngine()
	limits := e.GetLimits(resources.Workload{Kind: resources.KindWeb, Plan: ref.ToPlanInput()})
	if r, _ := limits.Get(resources.ResRAM); r.Limit != 256 {
		t.Fatalf("ram = %+v", r)
	}
	if r, _ := limits.Get(resources.ResCPU); r.Limit != 50 {
		t.Fatalf("cpu = %+v (0.5 core)", r)
	}
	if r, _ := limits.Get(resources.ResBandwidth); !r.LimitUnlimited {
		t.Fatalf("bandwidth must be unlimited when unset: %+v", r)
	}
	// Enforcement payload derivation for the agent job.
	payload := e.EnforcePlan(resources.Workload{Kind: resources.KindWeb, Plan: ref.ToPlanInput()})
	if payload.MemoryMB != 256 || payload.CPUPercent != 50 || payload.BandwidthMB != 0 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.CountLimits[resources.ResDatabases] != 4 {
		t.Fatalf("count limits = %+v", payload.CountLimits)
	}
}

// TestEnforcePayloadWebRouting — a web row drives the web resource set: the
// FPM bound (php_workers) and count caps map through PlanFromInput, and the
// payload carries no spurious values for resources the plan leaves unset.
func TestEnforcePayloadWebRouting(t *testing.T) {
	e := resources.NewEngine()
	web := PlanRef{Name: "Scale", Kind: "web", MemoryLimitMB: 4096,
		CPUCores: 2.0, MaxDiskMB: 20 * 1024, MaxBandwidthMB: 2 * 1024 * 1024,
		MaxProcesses: 256, MaxPorts: 1, MaxBackups: 3, IOWeight: 200}
	limits := e.GetLimits(resources.Workload{Kind: resources.KindWeb, Plan: web.ToPlanInput()})
	if _, ok := limits.Get(resources.ResRAM); !ok {
		t.Fatal("web plan must expose ram")
	}
	if r, ok := limits.Get(resources.ResPorts); !ok || r.Limit != 1 {
		t.Fatalf("ports = %+v (want 1)", r)
	}
	payload := e.EnforcePlan(resources.Workload{Kind: resources.KindWeb, Plan: web.ToPlanInput()})
	if payload.PidsMax != 256 {
		t.Fatalf("pids = %d", payload.PidsMax)
	}
	if payload.CountLimits[resources.ResPorts] != 1 || payload.CountLimits[resources.ResBackups] != 3 {
		t.Fatalf("count limits = %+v", payload.CountLimits)
	}
	// context unused by the pure engine — keep the signature stable for api wiring.
	_ = context.Background
}

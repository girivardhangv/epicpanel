package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// ============================================================================
// Phase 9 enforcement tests. Real kernel features (cgroup v2 unified
// hierarchy, xfs project quotas, nftables) are unavailable in this test
// environment, so the mechanism-specific paths are covered by
// detect-then-degrade unit tests with injected handles: the tests assert
// the agent NEVER pretends — unsupported = honest validated-only/accounted
// outcome with the reason attached.
// ============================================================================

const enforceTestWebsite = "00000000-0000-0000-0000-000000000001"

// fakeUnixUser is the user declared by the fake pool file; readSliceLimits
// and the slice path both key on it.
const fakeUnixUser = "ep-test-site"

type enforceFixture struct {
	t         *testing.T
	exec      *Executor
	siteBase  string
	cgroupDir string
	oldRoot   string
	oldState  string
	oldPS     func(string, ...string) *exec.Cmd
	oldSync   func(context.Context) error
	oldPHP    string
}

func newEnforceFixture(t *testing.T) *enforceFixture {
	t.Helper()
	base := t.TempDir()
	siteBase := filepath.Join(base, "sites")
	if err := os.MkdirAll(filepath.Join(siteBase, enforceTestWebsite), 0o755); err != nil {
		t.Fatal(err)
	}
	cgroupDir := filepath.Join(base, "cgroup")
	if err := os.MkdirAll(filepath.Join(cgroupDir, "epicpanel.slice"), 0o755); err != nil {
		t.Fatal(err)
	}
	// cgroup.controllers at the root AND the parent slice (enableControllers
	// walks root → parent before creating the leaf).
	for _, dir := range []string{cgroupDir, filepath.Join(cgroupDir, "epicpanel.slice")} {
		if err := os.WriteFile(filepath.Join(dir, "cgroup.controllers"), []byte("cpu io memory pids\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fx := &enforceFixture{
		t: t, siteBase: siteBase, cgroupDir: cgroupDir,
		oldRoot: cgroupRoot, oldState: limitsStateFile, oldPS: psCommand,
		oldSync: ensureLimitsSyncFn, oldPHP: phpEtcBase,
	}
	fx.exec = NewExecutor()
	fx.exec.docRootBase = siteBase

	// Inject: fake unified hierarchy, state file, no processes, no systemd.
	cgroupRoot = cgroupDir
	limitsStateFile = filepath.Join(base, "limits.json")
	psCommand = func(string, ...string) *exec.Cmd { return exec.Command("true") }
	ensureLimitsSyncFn = func(context.Context) error { return nil }
	// Fake PHP pool declaring the site's unix user (siteUnixUser resolution).
	poolDir := filepath.Join(base, "php", "8.2", "fpm", "pool.d")
	if err := os.MkdirAll(poolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pool := "user = " + fakeUnixUser + "\n" +
		"php_admin_value[memory_limit] = 128M\n" +
		"pm.max_children = 10\n" +
		"pm.start_servers = 2\n" +
		"pm.min_spare_servers = 2\n" +
		"pm.max_spare_servers = 4\n"
	if err := os.WriteFile(filepath.Join(poolDir, "epicpanel-"+enforceTestWebsite+".conf"), []byte(pool), 0o644); err != nil {
		t.Fatal(err)
	}
	phpEtcBase = filepath.Join(base, "php")
	t.Cleanup(fx.restore)
	return fx
}

func (fx *enforceFixture) restore() {
	cgroupRoot = fx.oldRoot
	limitsStateFile = fx.oldState
	psCommand = fx.oldPS
	ensureLimitsSyncFn = fx.oldSync
	phpEtcBase = fx.oldPHP
}

func (fx *enforceFixture) siteSlice() string {
	return filepath.Join(fx.cgroupDir, "epicpanel.slice", "epicpanel-"+fakeUnixUser+".slice")
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}

// findMechanism returns the outcome entry for one resource.
func findMechanism(out *EnforceOutcome, resource string) *MechanismOutcome {
	for i := range out.Mechanisms {
		if out.Mechanisms[i].Resource == resource {
			return &out.Mechanisms[i]
		}
	}
	return nil
}

// TestEnforceCgroupV2ApplyAndReadback — with an injected fake cgroup root
// the limits are applied to the slice files AND read back through the SAME
// reader the display collectors use (drift-proof by construction).
func TestEnforceCgroupV2ApplyAndReadback(t *testing.T) {
	fx := newEnforceFixture(t)
	out, err := fx.exec.EnforceLimits(context.Background(), EnforceJobPayload{
		WebsiteID: enforceTestWebsite, Plan: "Minecraft 4GB",
		CPUPercent: 200, MemoryMB: 4096, PidsMax: 256, IOWeight: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	slice := fx.siteSlice()
	for name, want := range map[string]string{
		"cpu.max":    "200000 100000",
		"memory.max": "4294967296",
		"pids.max":   "256",
		"io.weight":  "200",
	} {
		if got := readString(t, filepath.Join(slice, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	// Display read-back via the shared reader: identical numbers.
	lim := readSliceLimits(slice)
	if lim.MemoryMaxBytes != int64(4096)*1024*1024 {
		t.Errorf("readSliceLimits memory = %d", lim.MemoryMaxBytes)
	}
	if lim.CPUQuotaUsec != 200000 || lim.CPUPeriodUsec != 100000 {
		t.Errorf("readSliceLimits cpu = %d/%d", lim.CPUQuotaUsec, lim.CPUPeriodUsec)
	}
	if lim.PidsMax != 256 || lim.IOWeight != 200 {
		t.Errorf("readSliceLimits pids/io = %d/%d", lim.PidsMax, lim.IOWeight)
	}
	for _, res := range []string{"cpu", "ram", "processes"} {
		if m := findMechanism(out, res); m == nil || m.Mode != "enforced" {
			t.Errorf("%s mechanism = %+v, want enforced", res, m)
		}
	}
	if m := findMechanism(out, "io"); m == nil || m.Mode != "enforced" {
		t.Errorf("io mechanism = %+v, want enforced", m)
	}
	// Usage echo measured by the enforcement layer (memory.current exists
	// only after we create it — the fake fs starts empty, so only disk is
	// guaranteed here).
	if _, ok := out.Usage["disk"]; !ok {
		t.Fatalf("usage missing disk: %+v", out.Usage)
	}
}

// TestEnforceDegradesHonestlyWithoutCgroups — on a node without cgroup v2
// (injected root without cgroup.controllers) the outcome must say
// validated-only with the reason, never "enforced".
func TestEnforceDegradesHonestlyWithoutCgroups(t *testing.T) {
	fx := newEnforceFixture(t)
	os.Remove(filepath.Join(fx.cgroupDir, "cgroup.controllers"))

	out, err := fx.exec.EnforceLimits(context.Background(), EnforceJobPayload{
		WebsiteID: enforceTestWebsite, Plan: "Minecraft 4GB",
		CPUPercent: 200, MemoryMB: 4096, DiskMB: 20480,
		BandwidthMB: 2 * 1024 * 1024, PidsMax: 256,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := findMechanism(out, "cpu/ram/processes")
	if m == nil {
		t.Fatalf("missing cgroup mechanism report: %+v", out.Mechanisms)
	}
	if m.Mode != "validated-only" {
		t.Fatalf("mode = %q, want validated-only (honest degrade)", m.Mode)
	}
	if !strings.Contains(m.Detail, "unavailable") {
		t.Fatalf("degrade reason missing: %q", m.Detail)
	}
	if _, ok := out.Usage["disk"]; !ok {
		t.Fatalf("usage missing disk: %+v", out.Usage)
	}
}

// TestEnforceUnlimitedLeavesKernelMax — zero plan values mean unlimited:
// the kernel files must read "max", not 0.
func TestEnforceUnlimitedLeavesKernelMax(t *testing.T) {
	fx := newEnforceFixture(t)
	if _, err := fx.exec.EnforceLimits(context.Background(), EnforceJobPayload{
		WebsiteID: enforceTestWebsite, Plan: "custom",
	}); err != nil {
		t.Fatal(err)
	}
	slice := fx.siteSlice()
	if got := readString(t, filepath.Join(slice, "cpu.max")); got != "max 100000" {
		t.Errorf("cpu.max = %q, want max", got)
	}
	if got := readString(t, filepath.Join(slice, "memory.max")); got != "max" {
		t.Errorf("memory.max = %q, want max", got)
	}
	if got := readString(t, filepath.Join(slice, "pids.max")); got != "max" {
		t.Errorf("pids.max = %q, want max", got)
	}
	if _, err := os.Stat(filepath.Join(slice, "io.weight")); !os.IsNotExist(err) {
		t.Errorf("io.weight must not be written for 0 (kernel default): %v", err)
	}
}

// TestEnforceDiskQuotaDegradation — no quota flags/tooling on this
// filesystem: the disk resource must report accounted (usage-walk) with the
// honest reason, never "enforced".
func TestEnforceDiskQuotaDegradation(t *testing.T) {
	fx := newEnforceFixture(t)
	out, err := fx.exec.EnforceLimits(context.Background(), EnforceJobPayload{
		WebsiteID: enforceTestWebsite, Plan: "web", MemoryMB: 512, CPUPercent: 100,
		DiskMB: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	disk := findMechanism(out, "disk")
	if disk == nil {
		t.Fatal("missing disk mechanism report")
	}
	if disk.Mode != "accounted" {
		t.Fatalf("disk mode = %q, want accounted (quota unsupported here)", disk.Mode)
	}
	if disk.Detail == "" {
		t.Fatal("disk degrade must carry the reason")
	}
}

// TestDetectFSSupportParsesMounts — the quota detection decisions.
func TestDetectFSSupportParsesMounts(t *testing.T) {
	// xfs with prjquota → project quota supported.
	sup := detectFSSupportFromMounts(
		"/dev/sda1 / xfs rw,relatime,attr2,inode64,prjquota 0 0",
		filepath.Join(t.TempDir(), "site"))
	if !sup.Project || !sup.Quota || sup.Reason != "" {
		t.Fatalf("xfs prjquota: %+v", sup)
	}
	// ext4 without quota flags → unsupported with reason.
	sup = detectFSSupportFromMounts(
		"/dev/sda1 / ext4 rw,relatime 0 0",
		filepath.Join(t.TempDir(), "site"))
	if sup.Quota || sup.Project {
		t.Fatalf("ext4 noflags must be unsupported: %+v", sup)
	}
	if sup.Reason == "" {
		t.Fatal("unsupported fs must carry the reason")
	}
	// ext4 with usrquota → user quota possible.
	sup = detectFSSupportFromMounts(
		"/dev/sda1 / ext4 rw,usrquota 0 0",
		filepath.Join(t.TempDir(), "site"))
	if !sup.Quota {
		t.Fatalf("ext4 usrquota must be quota-capable: %+v", sup)
	}
	// tmpfs (virtual) → unsupported.
	sup = detectFSSupportFromMounts(
		"tmpfs /tmp tmpfs rw 0 0",
		filepath.Join("/tmp", "epicpanel-nonexistent-xyz"))
	if sup.Quota || sup.Project {
		t.Fatalf("tmpfs must be unsupported: %+v", sup)
	}
}

// TestEnforceBandwidthDegradation — nft binary absent: bandwidth must be
// accounted-only with the reason (never silently "enforced").
func TestEnforceBandwidthDegradation(t *testing.T) {
	fx := newEnforceFixture(t)
	out, err := fx.exec.EnforceLimits(context.Background(), EnforceJobPayload{
		WebsiteID: enforceTestWebsite, Plan: "web", MemoryMB: 512, CPUPercent: 100,
		BandwidthMB: 102400,
	})
	if err != nil {
		t.Fatal(err)
	}
	bw := findMechanism(out, "bandwidth")
	if bw == nil {
		t.Fatal("missing bandwidth mechanism report")
	}
	if bw.Mode != "accounted" {
		t.Fatalf("bandwidth mode = %q, want accounted", bw.Mode)
	}
	if bw.Detail == "" {
		t.Fatal("bandwidth degrade must carry the reason")
	}
}

// TestParseNFTCounter — the accounting counter parser.
func TestParseNFTCounter(t *testing.T) {
	out := []byte(`table inet epicpanel_limits {
	chain acct_ep_test {
		counter packets 1234 bytes 5678 comment "rx"
	}
}`)
	rx, tx := parseNFTCounter(out)
	if rx+tx != 5678 {
		t.Fatalf("counters rx=%d tx=%d, want sum 5678", rx, tx)
	}
	if rx, tx = parseNFTCounter([]byte("no counters here")); rx != 0 || tx != 0 {
		t.Fatal("no counters must parse to 0/0")
	}
}

// TestFPMPoolBoundEnforced — the PHP-workers bound patches the pool file
// (pm.max_children = plan value) with validate + restore semantics (no fpm
// binary on the node → patch applies without validation round-trip).
func TestFPMPoolBoundEnforced(t *testing.T) {
	fx := newEnforceFixture(t)
	poolFile := filepath.Join(phpEtcBase, "8.2", "fpm", "pool.d", "epicpanel-"+enforceTestWebsite+".conf")
	out, err := fx.exec.EnforceLimits(context.Background(), EnforceJobPayload{
		WebsiteID: enforceTestWebsite, Plan: "Starter", FpmMaxChildren: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readString(t, poolFile); !strings.Contains(got, "pm.max_children = 16") {
		t.Fatalf("pool not bound to plan: %s", got)
	}
	if m := findMechanism(out, "php_workers"); m == nil || m.Mode != "enforced" {
		t.Fatalf("php_workers mechanism = %+v, want enforced", m)
	}
}

// TestBreachEvaluationSuspendHook — over-budget disk/bandwidth produce a
// suspend breach report (the Phase 10 hook), kernel-capped resources don't.
func TestBreachEvaluationSuspendHook(t *testing.T) {
	p := EnforceJobPayload{WebsiteID: "w", MemoryMB: 2048, CPUPercent: 100,
		DiskMB: 100, BandwidthMB: 1000}
	usage := map[string]float64{
		"ram":       1999, // under: kernel memory.max handles the rest
		"cpu":       99,
		"disk":      150,  // over 100 → suspend
		"bandwidth": 1200, // over 1000 → suspend
	}
	breaches := evaluateBreaches(p, usage)
	got := map[string]string{}
	for _, b := range breaches {
		got[b.Resource] = b.Action
	}
	if got["disk"] != "suspend" || got["bandwidth"] != "suspend" {
		t.Fatalf("breaches = %+v", breaches)
	}
	if _, ok := got["ram"]; ok {
		t.Fatalf("ram under kernel cap must not report a breach: %+v", breaches)
	}
	if b := evaluateBreaches(p, map[string]float64{"disk": 50, "bandwidth": 100, "ram": 100, "cpu": 10}); len(b) != 0 {
		t.Fatalf("unexpected breaches: %+v", b)
	}
}

// TestEnforceOutcomeJSON — the outcome round-trips (jobs transport).
func TestEnforceOutcomeJSON(t *testing.T) {
	out := EnforceOutcome{
		WebsiteID: "w", Plan: "Minecraft 4GB",
		Mechanisms: []MechanismOutcome{{Resource: "ram", Mode: "enforced", Mechanism: "cgroup-v2"}},
		Usage:      map[string]float64{"ram": 1024},
		Breaches:   []BreachReport{{Resource: "bandwidth", Action: "suspend"}},
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var back EnforceOutcome
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Plan != "Minecraft 4GB" || len(back.Mechanisms) != 1 || len(back.Breaches) != 1 {
		t.Fatalf("round trip lost data: %+v", back)
	}
}

// TestDriftDisplaySourceEqualsEnforcementSource — THE drift test: the
// workload collector that feeds the UI (CollectSites → LiveStore → WS) and
// the enforcement arm must report the SAME limits and usage, because both
// read the same slice files through the same reader. A second measurement
// source would break this test.
func TestDriftDisplaySourceEqualsEnforcementSource(t *testing.T) {
	fx := newEnforceFixture(t)

	// 1. ENFORCEMENT: apply the Minecraft 4GB matrix node-side (pass one
	// creates the slice; the op is idempotent).
	payload := EnforceJobPayload{
		WebsiteID: enforceTestWebsite, Plan: "Minecraft 4GB",
		CPUPercent: 200, MemoryMB: 4096, PidsMax: 256, IOWeight: 200,
	}
	if _, err := fx.exec.EnforceLimits(context.Background(), payload); err != nil {
		t.Fatal(err)
	}

	// Live load in the slice (kernel state between the two passes).
	slice := fx.siteSlice()
	if err := os.WriteFile(filepath.Join(slice, "memory.current"), []byte("3221225472\n"), 0o644); err != nil { // 3 GiB
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slice, "cgroup.procs"), []byte("123\n456\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Enforce against that live slice: the outcome's usage echo must be the
	// kernel numbers (single layer).
	out, err := fx.exec.EnforceLimits(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}

	// 2. DISPLAY: the exact collector the Phase 3 stream sends to LiveStore.
	wc := newWorkloadCollector()
	sites := wc.CollectSites()
	var displayed *agentproto.SiteSample
	for i := range sites {
		if sites[i].UnixUser == fakeUnixUser {
			displayed = &sites[i]
			break
		}
	}
	if displayed == nil {
		t.Fatalf("display collector did not see the enforced slice: %+v", sites)
	}

	// 3. The numbers must agree — displayed limits == enforced limits == the
	// agent's own enforcement-readback.
	if displayed.MemoryLimit != int64(4096)*1024*1024 {
		t.Errorf("display memory limit = %d, enforced 4096MB — DRIFT", displayed.MemoryLimit)
	}
	if displayed.CPULimitCores != 2.0 {
		t.Errorf("display cpu limit = %.2f cores, enforced 200%% — DRIFT", displayed.CPULimitCores)
	}
	if displayed.PidsLimit != 256 {
		t.Errorf("display pids limit = %d, enforced 256 — DRIFT", displayed.PidsLimit)
	}
	if displayed.MemoryBytes != int64(3)*1024*1024*1024 {
		t.Errorf("display memory usage = %d, kernel reports 3GiB — DRIFT", displayed.MemoryBytes)
	}
	if displayed.Processes != 2 {
		t.Errorf("display processes = %d, kernel reports 2 — DRIFT", displayed.Processes)
	}
	// And the enforcement outcome's usage echo agrees too (single layer).
	if got := out.Usage["ram"]; got != 3072 {
		t.Errorf("enforcement usage echo ram = %v MB, want 3072 — DRIFT", got)
	}
	if got := out.Usage["processes"]; got != 2 {
		t.Errorf("enforcement usage echo processes = %v, want 2 — DRIFT", got)
	}
}

// TestReconcileCountsGuard — the agent-side reconciliation guard reports
// missing site trees and never mutates anything.
func TestReconcileCountsGuard(t *testing.T) {
	fx := newEnforceFixture(t) // keeps global handles sane for the siteBase paths
	// Guard with no count limits → no violations.
	if v := fx.exec.reconcileCounts(EnforceJobPayload{WebsiteID: enforceTestWebsite}); v != nil {
		t.Fatalf("ungoverned counts must not violate: %v", v)
	}
	// Site tree present → no violation even with limits set.
	if v := fx.exec.reconcileCounts(EnforceJobPayload{
		WebsiteID:   enforceTestWebsite,
		CountLimits: map[string]int{"databases": 2, "backups": 3, "ports": 1},
	}); v != nil {
		t.Fatalf("present site must reconcile clean: %v", v)
	}
	// Site tree missing → guard reports (control plane decides the action).
	missing := "99999999-9999-9999-9999-999999999999"
	v := fx.exec.reconcileCounts(EnforceJobPayload{WebsiteID: missing, CountLimits: map[string]int{"backups": 1}})
	if len(v) == 0 {
		t.Fatal("missing site tree must be reported")
	}
}

package resources

import "strconv"

// PlanSpec is a plan's resource matrix, expressed purely as data. The
// control plane maps a hosting_packages row onto this spec (see
// PlanInput/PlanFromInput); the agent receives the resolved numbers in the
// enforce_limits job payload. Adding a new plan = inserting a row.
type PlanSpec struct {
	Name string
	Kind WorkloadKind
	// Limits: resource name → quantity in the resource's single unit
	// (percent for CPU, MB for RAM/Disk/Bandwidth, count for the rest).
	// Missing entry = resource not governed by the plan.
	Limits map[string]float64
}

// MB helpers for readable seed data.
func mb(mib int64) float64 { return float64(mib) }

func gb(mib int64) float64 { return float64(mib) * 1024 }

func tb(gib int64) float64 { return float64(gib) * 1024 * 1024 }

// ============================================================================
// Seed data — the 7 verbatim plans. Mirrored 1:1 by migration 0027.
// Minecraft 4GB is verbatim from the master doc:
//   RAM: 4096 MB · CPU: 200% · Disk: 20 GB · Bandwidth: 2 TB · Ports: 1 ·
//   Backups: 3
// ============================================================================

// AllPlans returns the built-in plan registry (seed data). The runtime
// resolver reads hosting_packages first (so WHM plan edits land without a
// code change); this registry is the fallback + the migration's mirror.
func AllPlans() []PlanSpec {
	return []PlanSpec{
		{
			Name: "Starter", Kind: KindWeb,
			Limits: map[string]float64{
				ResCPU: 100, ResRAM: mb(512), ResDisk: mb(2048), ResBandwidth: mb(100 * 1024),
				ResIO: 100, ResProcesses: 64, ResPHPWorkers: 16, ResDatabases: 2, ResDomains: 5,
				ResEmail: 5, ResBackups: 2,
			},
		},
		{
			Name: "Pro", Kind: KindWeb,
			Limits: map[string]float64{
				ResCPU: 200, ResRAM: mb(1024), ResDisk: gb(20), ResBandwidth: gb(1024),
				ResIO: 200, ResProcesses: 128, ResPHPWorkers: 32, ResDatabases: 20, ResDomains: 45,
				ResEmail: 50, ResBackups: 3,
			},
		},
		{
			Name: "Business", Kind: KindWeb,
			Limits: map[string]float64{
				ResCPU: 400, ResRAM: mb(2048), ResDisk: gb(100), ResBandwidth: tb(5),
				ResIO: 300, ResProcesses: 256, ResPHPWorkers: 64, ResDatabases: 100, ResDomains: 450,
				ResEmail: 200, ResBackups: 7,
			},
		},
		{
			Name: "Minecraft 2GB", Kind: KindMinecraft,
			Limits: map[string]float64{
				ResCPU: 200, ResRAM: mb(2048), ResDisk: gb(20), ResBandwidth: tb(2),
				ResProcesses: 128, ResPorts: 1, ResBackups: 3,
			},
		},
		{
			Name: "Minecraft 4GB", Kind: KindMinecraft,
			Limits: map[string]float64{
				ResCPU: 200, ResRAM: mb(4096), ResDisk: gb(20), ResBandwidth: tb(2),
				ResProcesses: 256, ResPorts: 1, ResBackups: 3,
			},
		},
		{
			Name: "Discord Basic", Kind: KindDiscord,
			Limits: map[string]float64{
				ResCPU: 50, ResRAM: mb(512), ResDisk: gb(2), ResBandwidth: gb(512),
				ResProcesses: 16, ResBackups: 0,
			},
		},
		{
			Name: "Discord Pro", Kind: KindDiscord,
			Limits: map[string]float64{
				ResCPU: 100, ResRAM: mb(1024), ResDisk: gb(5), ResBandwidth: gb(1024),
				ResProcesses: 32, ResPorts: 1, ResBackups: 1,
			},
		},
	}
}

// PlanByName resolves the built-in registry (fallback for plans that have no
// hosting_packages row of their own, e.g. custom admin-created rows).
func PlanByName(name string) (PlanSpec, bool) {
	for _, p := range AllPlans() {
		if p.Name == name {
			return p, true
		}
	}
	return PlanSpec{}, false
}

// ============================================================================
// Resource sets (verbatim per workload kind from the master doc).
// ============================================================================

// WebResourceSet — verbatim: CPU · RAM · Disk · I/O · Processes · PHP
// workers · Databases · Domains · Email · Bandwidth.
func WebResourceSet() []string {
	return []string{ResCPU, ResRAM, ResDisk, ResIO, ResProcesses, ResPHPWorkers,
		ResDatabases, ResDomains, ResEmail, ResBandwidth}
}

// MinecraftResourceSet — the master-doc Resource model restricted to what a
// Minecraft workload consumes: CPU · RAM · Disk · Bandwidth · Processes ·
// Ports · Backups.
func MinecraftResourceSet() []string {
	return []string{ResCPU, ResRAM, ResDisk, ResBandwidth, ResProcesses, ResPorts, ResBackups}
}

// DiscordResourceSet — CPU · RAM · Disk · Bandwidth · Processes · Backups.
func DiscordResourceSet() []string {
	return []string{ResCPU, ResRAM, ResDisk, ResBandwidth, ResProcesses, ResBackups}
}

// ResourceSetFor returns the ordered resource list for a workload kind.
func ResourceSetFor(kind WorkloadKind) []string {
	switch kind {
	case KindMinecraft:
		return MinecraftResourceSet()
	case KindDiscord:
		return DiscordResourceSet()
	default:
		return WebResourceSet()
	}
}

// UnitFor is the single-unit rule: cpu percent-of-one-core; MB for
// ram/disk/bandwidth; count for everything else.
func UnitFor(name string) string {
	switch name {
	case ResCPU:
		return UnitPercent
	case ResRAM, ResDisk, ResBandwidth:
		return UnitMB
	default:
		return UnitCount
	}
}

// ============================================================================
// PlanInput — the hosting_packages columns the engine consumes. Declared
// here (not as packages.Package) so the agent binary can use the engine
// without linking the control plane's HTTP/store layers.
// ============================================================================

// PlanInput is the data-driven plan source (a hosting_packages row).
type PlanInput struct {
	Name             string
	Kind             WorkloadKind
	MaxWebsites      int
	MaxDatabases     int
	MaxDiskMB        int64
	MemoryLimitMB    int64
	CPUCores         float64
	MaxAddonDomains  int
	MaxSubdomains    int
	MaxBandwidthMB   int64 // 0 = unlimited
	IOWeight         int   // 0 = kernel default
	MaxProcesses     int   // 0 = unlimited (pids.max "max")
	MaxPorts         int
	MaxBackups       int
	MaxEmailAccounts int

	// LegacyRow marks pre-Phase-9 rows: their matrix covers only the legacy
	// columns; the new counts (ports/backups/email) stay ungoverned.
	LegacyRow bool
}

// PlanFromInput maps the row's columns onto the unified resource model.
// Counts (websites/databases/domains/ports/backups/email) come straight from
// the columns; node limits (cpu/ram/disk/bandwidth/io/processes) likewise —
// the plan matrix IS the row, so WHM edits are data-only.
func PlanFromInput(in PlanInput) PlanSpec {
	limits := map[string]float64{}
	limits[ResCPU] = in.CPUCores * 100
	limits[ResRAM] = float64(in.MemoryLimitMB)
	limits[ResDisk] = float64(in.MaxDiskMB)
	if in.MaxBandwidthMB > 0 {
		limits[ResBandwidth] = float64(in.MaxBandwidthMB)
	} else {
		limits[ResBandwidth] = 0 // 0 = unlimited (bandwidth cap optional)
	}
	if in.IOWeight > 0 {
		limits[ResIO] = float64(in.IOWeight)
	}
	if in.MaxProcesses > 0 {
		limits[ResProcesses] = float64(in.MaxProcesses)
	} else {
		limits[ResProcesses] = 0
	}
	limits[ResDatabases] = float64(in.MaxDatabases)
	limits[ResDomains] = float64(in.MaxAddonDomains + in.MaxSubdomains)
	if !in.LegacyRow {
		// Phase 9 matrix columns: counts of 0 are real caps ("zero allowed").
		limits[ResEmail] = float64(in.MaxEmailAccounts)
		limits[ResPorts] = float64(in.MaxPorts)
		limits[ResBackups] = float64(in.MaxBackups)
	}
	kind := in.Kind
	if kind == "" {
		kind = KindWeb
	}
	return PlanSpec{Name: in.Name, Kind: kind, Limits: limits}
}

// formatQty renders a quantity in its unit (display helper for WHM bars).
func formatQty(unit string, v float64) string {
	switch unit {
	case UnitPercent:
		return strconv.FormatFloat(v, 'f', -1, 64) + "%"
	case UnitMB:
		return formatMB(v)
	default:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
}

func formatMB(v float64) string {
	switch {
	case v >= 1024*1024:
		return strconv.FormatFloat(v/1024/1024, 'f', 0, 64) + " TB"
	case v >= 1024:
		return strconv.FormatFloat(v/1024, 'f', 0, 64) + " GB"
	default:
		return strconv.FormatFloat(v, 'f', 0, 64) + " MB"
	}
}

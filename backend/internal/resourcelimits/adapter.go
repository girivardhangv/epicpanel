// Package resourcelimits adapts the unified resource engine
// (internal/resources) to the control plane's stores. It is the ONE wiring
// point for limit gates and usage views; limit logic itself lives only in
// internal/resources. Existing endpoint response shapes are unchanged.
package resourcelimits

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/packages"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
)

// PlanRef carries a hosting_packages row's resource columns (the plan IS
// the row — data, not code).
type PlanRef struct {
	Name             string
	Kind             string
	MaxWebsites      int
	MaxDatabases     int
	MaxDiskMB        int64
	MemoryLimitMB    int64
	CPUCores         float64
	MaxAddonDomains  int
	MaxSubdomains    int
	MaxBandwidthMB   int64
	IOWeight         int
	MaxProcesses     int
	MaxPorts         int
	MaxBackups       int
	MaxEmailAccounts int

	// LegacyRow marks pre-Phase-9 rows (all new columns at their defaults).
	// Their governed counts are exactly the legacy columns; the Phase 9
	// counts (ports/backups/email) are ungoverned so existing packages keep
	// their pre-engine behavior until an admin edits the plan.
	LegacyRow bool
}

// ToPlanInput converts the row reference to the engine's PlanInput.
func (p PlanRef) ToPlanInput() resources.PlanInput {
	kind := resources.WorkloadKind(p.Kind)
	if kind == "" {
		kind = resources.KindWeb
	}
	return resources.PlanInput{
		Name: p.Name, Kind: kind, LegacyRow: p.LegacyRow,
		MaxWebsites: p.MaxWebsites, MaxDatabases: p.MaxDatabases,
		MaxDiskMB: p.MaxDiskMB, MemoryLimitMB: p.MemoryLimitMB, CPUCores: p.CPUCores,
		MaxAddonDomains: p.MaxAddonDomains, MaxSubdomains: p.MaxSubdomains,
		MaxBandwidthMB: p.MaxBandwidthMB, IOWeight: p.IOWeight, MaxProcesses: p.MaxProcesses,
		MaxPorts: p.MaxPorts, MaxBackups: p.MaxBackups, MaxEmailAccounts: p.MaxEmailAccounts,
	}
}

// PlanFromPackage adapts the legacy packages.Package columns (a strict
// subset of the matrix — always a legacy row).
func PlanFromPackage(p *packages.Package) PlanRef {
	if p == nil {
		return PlanRef{LegacyRow: true}
	}
	return PlanRef{
		Name: p.Name, Kind: "web", LegacyRow: true,
		MaxWebsites: p.MaxWebsites, MaxDatabases: p.MaxDatabases,
		MaxDiskMB:       int64(p.MaxDiskMB),
		MemoryLimitMB:   int64(p.MemoryLimitMB),
		CPUCores:        p.CPUCores,
		MaxAddonDomains: p.MaxAddonDomains, MaxSubdomains: p.MaxSubdomains,
	}
}

// Engine wraps the pure engine and resolves plans from the plan row.
type Engine struct {
	Pool     *pgxpool.Pool
	Packages *packages.Store
	res      *resources.Engine
}

// New builds the adapter. With a Pool the plan resolves from the full
// hosting_packages row (the matrix); without one it falls back to the
// legacy packages.Store columns.
func New(store *packages.Store, pool *pgxpool.Pool) *Engine {
	return &Engine{Pool: pool, Packages: store, res: resources.NewEngine()}
}

// planRowCols is the full resource matrix of a hosting_packages row.
const planRowCols = `hp.name, hp.kind, hp.max_websites, hp.max_databases, hp.max_disk_mb,
	hp.memory_limit_mb, hp.cpu_cores, hp.max_addon_domains, hp.max_subdomains,
	hp.max_bandwidth_mb, hp.io_weight, hp.max_processes, hp.max_ports,
	hp.max_backups, hp.max_email_accounts`

// isLegacyRowSQL: a pre-Phase-9 row has every new column at its default.
const isLegacyRowSQL = `(hp.max_bandwidth_mb = 0 AND hp.io_weight = 0 AND hp.php_max_children = 0
	AND hp.max_processes = 0 AND hp.max_ports = 0 AND hp.max_backups = 0 AND hp.max_email_accounts = 0)`

func scanPlanRow(row pgx.Row) (PlanRef, error) {
	var p PlanRef
	err := row.Scan(&p.Name, &p.Kind, &p.MaxWebsites, &p.MaxDatabases, &p.MaxDiskMB,
		&p.MemoryLimitMB, &p.CPUCores, &p.MaxAddonDomains, &p.MaxSubdomains,
		&p.MaxBandwidthMB, &p.IOWeight, &p.MaxProcesses, &p.MaxPorts,
		&p.MaxBackups, &p.MaxEmailAccounts, &p.LegacyRow)
	return p, err
}

// PlanForOrg resolves the org's effective plan as engine input: the
// assigned row, else the platform default package.
func (e *Engine) PlanForOrg(ctx context.Context, orgID uuid.UUID) (PlanRef, error) {
	if e.Pool != nil {
		row := e.Pool.QueryRow(ctx, `
			SELECT `+planRowCols+`, `+isLegacyRowSQL+`
			FROM organizations o
			JOIN hosting_packages hp ON hp.id = COALESCE(o.package_id,
				(SELECT id FROM hosting_packages WHERE is_default = TRUE ORDER BY created_at LIMIT 1))
			WHERE o.id = $1`, orgID)
		p, err := scanPlanRow(row)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return PlanRef{}, err
		}
	}
	// Fallback: legacy columns via the packages store.
	p, err := e.Packages.ForOrg(ctx, orgID)
	if err != nil {
		return PlanRef{}, err
	}
	return PlanFromPackage(p), nil
}

// LimitsForOrg returns the unified limit set for an org's plan.
func (e *Engine) LimitsForOrg(ctx context.Context, orgID uuid.UUID) (resources.Limits, error) {
	ref, err := e.PlanForOrg(ctx, orgID)
	if err != nil {
		return resources.Limits{}, err
	}
	return e.res.GetLimits(resources.Workload{
		Kind: resources.WorkloadKind(orWeb(ref.Kind)), Plan: ref.ToPlanInput(),
	}), nil
}

// EnforcePayloadFor builds the agent enforce_limits payload for one site.
func (e *Engine) EnforcePayloadFor(ctx context.Context, orgID, websiteID uuid.UUID) (resources.EnforcePayload, error) {
	ref, err := e.PlanForOrg(ctx, orgID)
	if err != nil {
		return resources.EnforcePayload{}, err
	}
	return e.res.EnforcePlan(resources.Workload{
		Kind:      resources.WorkloadKind(orWeb(ref.Kind)),
		WebsiteID: websiteID,
		Plan:      ref.ToPlanInput(),
	}), nil
}

// PerkPayload builds the agent enforce payload for the Free Perk resource
// set (the hosting_packages row with kind 'free_perk', admin-editable via
// the package CRUD). ok=false when no perk row exists.
func (e *Engine) PerkPayload(ctx context.Context) (resources.EnforcePayload, bool, error) {
	if e.Pool == nil {
		return resources.EnforcePayload{}, false, nil
	}
	row := e.Pool.QueryRow(ctx, `
		SELECT `+planRowCols+`
		FROM hosting_packages hp
		WHERE hp.kind = 'free_perk'
		ORDER BY hp.created_at
		LIMIT 1`)
	ref, err := scanPlanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return resources.EnforcePayload{}, false, nil
	}
	if err != nil {
		return resources.EnforcePayload{}, false, err
	}
	p := e.res.EnforcePlan(resources.Workload{Kind: resources.KindWeb, Plan: ref.ToPlanInput()})
	return p, true, nil
}

func orWeb(k string) string {
	if k == "" {
		return string(resources.KindWeb)
	}
	return k
}

// ============================================================================
// Create-time count gates (Databases / Domains / Ports / Backups / Email).
// Same engine, same mapping as the display path — no duplicated limit logic.
// Counters are injected so the adapter never reaches into other subsystems'
// stores directly; a nil counter for a governed resource fails closed.
// ============================================================================

// CountGate validates counted resources at create time.
type CountGate struct {
	Engine *Engine
	// Counter fills in the current count for one resource.
	Counter func(ctx context.Context, orgID uuid.UUID, resource string) (int, error)
}

// CheckCount validates one more unit of the counted resource under the org's
// plan. Mirrors the legacy gate semantics ("package limit reached: ...")
// with the limit number now coming from the unified engine.
func (g CountGate) CheckCount(ctx context.Context, orgID uuid.UUID, resource string) error {
	limits, err := g.Engine.LimitsForOrg(ctx, orgID)
	if err != nil {
		return errLimitUnavailable(resource)
	}
	if _, governed := limits.Resources[resource]; !governed {
		return nil // not part of this plan's matrix
	}
	cur, err := g.Counter(ctx, orgID, resource)
	if err != nil {
		return errLimitUnavailable(resource)
	}
	return resources.CheckCount(limits, resource, cur)
}

func errLimitUnavailable(resource string) error {
	return &LimitUnavailableError{Resource: resource}
}

// LimitUnavailableError is the fail-closed gate error (audit rule: a failed
// limit lookup denies the mutation rather than bypassing it).
type LimitUnavailableError struct{ Resource string }

func (e *LimitUnavailableError) Error() string {
	return "package limits unavailable; " + e.Resource + " creation blocked"
}

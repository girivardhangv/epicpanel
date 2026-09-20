package api

import (
	"context"
	"encoding/json"

	"log/slog"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// ============================================================================
// Phase 9 — control-plane wiring of the unified resource engine:
//   1. hourly enforce_limits convergence (desired state, like resyncCrontabs)
//   2. enforce_limits outcome fanout: usage accounting (workload_resource_
//      usage) + over-limit actions (throttle/kill/suspend hooks → jobs).
// Never silently corrupt workload state: over-limit suspensions go through
// the idempotent suspend_website job (the same hook Phase 10 consumes).
// ============================================================================

// enforceOutcome mirrors the agent's enforce_limits job result (see
// agent.EnforceOutcome). Declared here to keep the agent package out of the
// control-plane binary's import graph.
type enforceOutcome struct {
	WebsiteID       string             `json:"website_id"`
	Plan            string             `json:"plan"`
	AppliedAt       string             `json:"applied_at"`
	Mechanisms      []agentMechanism   `json:"mechanisms"`
	Usage           map[string]float64 `json:"usage"`
	Breaches        []agentBreach      `json:"breaches"`
	CountViolations []string           `json:"count_violations"`
}

type agentMechanism struct {
	Resource  string `json:"resource"`
	Mode      string `json:"mode"`
	Mechanism string `json:"mechanism"`
	Detail    string `json:"detail"`
}

type agentBreach struct {
	Resource string `json:"resource"`
	Usage    string `json:"usage"`
	Limit    string `json:"limit"`
	Action   string `json:"action"`
	Detail   string `json:"detail"`
}

// enqueueEnforceLimits re-enqueues the unified enforcement job for every
// ready website (idempotent: one pending job per site). This converges node
// state after plan changes, agent restarts or drift. Dynamic-resource sites
// converge to their CURRENT TIER (base × tier scaling, Free Perk overlay),
// not the raw plan — the payload builder is shared with the allocator so
// both writers produce identical numbers.
func (s *Server) enqueueEnforceLimits(ctx context.Context) {
	sites, err := s.Websites.ReadyForLimits(ctx, 200)
	if err != nil {
		slog.Warn("limits convergence scan failed", "err", err)
		return
	}
	for i := range sites {
		ws := &sites[i]
		payload, err := s.dynamicEffectivePayload(ctx, ws)
		if err != nil {
			slog.Warn("enforce payload build failed", "website", ws.ID, "err", err)
			continue
		}
		if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeEnforceLimits, payload, "enforce_limits_"+ws.ID.String()); err != nil {
			slog.Warn("enforce_limits enqueue failed", "website", ws.ID, "err", err)
		}
	}
}

// applyEnforceOutcome consumes a finished enforce_limits job: persists the
// period usage accounting and acts on breaches per policy.
func (s *Server) applyEnforceOutcome(ctx context.Context, job *jobs.Job, result json.RawMessage) {
	if job.Type != jobs.TypeEnforceLimits || job.Status != jobs.StatusSuccess || job.WebsiteID == nil {
		return
	}
	var o enforceOutcome
	if err := json.Unmarshal(result, &o); err != nil {
		return
	}
	ws, err := s.Websites.GetByIDAny(ctx, *job.WebsiteID)
	if err != nil || ws == nil {
		return
	}

	// Usage accounting (bandwidth period budget) — the same numbers the
	// agent measured while enforcing (outcome.usage, MB, RX+TX combined).
	if bw, ok := o.Usage["bandwidth"]; ok {
		s.accountBandwidth(ctx, ws, int64(bw), "agent-enforce")
	}

	// Over-limit actions.
	for _, b := range o.Breaches {
		s.publishBreach(ctx, ws, o.Plan, b)
		switch b.Action {
		case "suspend":
			s.suspendOverLimit(ctx, ws, b)
		case "throttle":
			// Kernel-level shaping is applied agent-side; re-assert.
			if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeEnforceLimits, map[string]any{
				"website_id": ws.ID.String(), "throttle": b.Resource,
			}, "throttle_"+b.Resource+"_"+ws.ID.String()); err != nil {
				slog.Warn("throttle enqueue failed", "website", ws.ID, "err", err)
			}
		}
	}
}

// suspendOverLimit enqueues the idempotent suspend_website job (Phase 10
// consumes the same hook) and records the system action in the audit log.
func (s *Server) suspendOverLimit(ctx context.Context, ws *websites.Website, b agentBreach) {
	if ws.Status != websites.StatusReady {
		return // never suspend a non-running workload from a breach report
	}
	key := "overlimit_suspend_" + b.Resource + "_" + ws.ID.String()
	if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeSuspendWebsite,
		websites.SuspendPayload{WebsiteID: ws.ID.String()}, key); err != nil {
		slog.Error("over-limit suspend enqueue failed", "website", ws.ID, "err", err)
		return
	}
	org := ws.Organization
	s.Audit.RecordBestEffort(ctx, audit.Entry{
		OrganizationID: &org,
		ActorType:      audit.ActorSystem,
		Action:         "website.overlimit_suspend",
		ResourceType:   "website",
		ResourceID:     ws.ID.String(),
		Metadata: map[string]any{
			"resource": b.Resource, "usage": b.Usage, "limit": b.Limit,
		},
	})
	slog.Warn("over-limit suspension enqueued", "website", ws.ID, "resource", b.Resource)
}

// publishBreach emits the limits.breach event (WS fanout + Phase 10 hooks).
func (s *Server) publishBreach(ctx context.Context, ws *websites.Website, plan string, b agentBreach) {
	if s.Events == nil {
		return
	}
	org := ws.Organization
	s.Events.Publish(ctx, events.Event{
		Type:         "limits.breach",
		Organization: &org,
		ActorType:    "system",
		ResourceType: "website",
		ResourceID:   ws.ID.String(),
		Payload: map[string]any{
			"plan": plan, "resource": b.Resource, "usage": b.Usage,
			"limit": b.Limit, "action": b.Action,
		},
	})
}

// accountBandwidth upserts the monthly RX+TX accounting row. Counters are
// cumulative per period; GREATEST keeps the high-water mark so agent-side
// counter resets (nft reload) can never undercount a paid period.
func (s *Server) accountBandwidth(ctx context.Context, ws *websites.Website, usedMB int64, measuredBy string) {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO workload_resource_usage
			(organization_id, website_id, resource, period_start, used, limit_value, unit, measured_by)
		VALUES ($1, $2, 'bandwidth', date_trunc('month', now()), $3,
		        COALESCE((SELECT max_bandwidth_mb FROM hosting_packages hp
		                  JOIN organizations o ON o.package_id = hp.id WHERE o.id = $1), 0),
		        'MB', $4)
		ON CONFLICT (website_id, resource, period_start) DO UPDATE SET
			used = GREATEST(workload_resource_usage.used, EXCLUDED.used),
			measured_by = EXCLUDED.measured_by,
			updated_at = now()
	`, ws.Organization, ws.ID, usedMB, measuredBy)
	if err != nil {
		slog.Warn("bandwidth accounting upsert failed", "website", ws.ID, "err", err)
	}
}

// countOrgResources fills the agent-side reconciliation guard's counts with
// the control-plane-observed numbers (databases/domains/ports/backups).
func (s *Server) countOrgResources(ctx context.Context, orgID uuid.UUID) map[string]int {
	counts := map[string]int{}
	for res, q := range map[string]string{
		"databases": `SELECT count(*) FROM databases WHERE organization_id = $1`,
		"backups":   `SELECT count(*) FROM backups WHERE organization_id = $1`,
		"domains": `SELECT count(*) FROM domains d JOIN websites w ON w.id = d.website_id
		             WHERE w.organization_id = $1 AND w.status NOT IN ('deleted','deleting')`,
	} {
		var n int
		if err := s.Pool.QueryRow(ctx, q, orgID).Scan(&n); err == nil {
			counts[res] = n
		}
	}
	var ports int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM websites WHERE organization_id = $1 AND backend_port > 0`, orgID).Scan(&ports); err == nil {
		counts["ports"] = ports
	}
	return counts
}

// CountResource implements the create-time count gate over the unified
// engine (databases / domains / ports / backups / email).
func (s *Server) CountResource(ctx context.Context, orgID uuid.UUID, resource string) (int, error) {
	if n, ok := s.countOrgResources(ctx, orgID)[resource]; ok {
		return n, nil
	}
	return 0, nil
}

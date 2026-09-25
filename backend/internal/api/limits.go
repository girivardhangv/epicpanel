package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
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
	// Fractional MB is kept (NUMERIC column): the resume guard compares
	// byte-exact quota boundaries, so truncating here could hide a
	// 9.9999 GB → 10 GB crossing.
	if bw, ok := o.Usage["bandwidth"]; ok {
		s.accountBandwidth(ctx, ws, bw, "agent-enforce")
	}

	// Over-limit actions.
	for _, b := range o.Breaches {
		s.publishBreach(ctx, ws, o.Plan, b)
		switch b.Action {
		case "suspend":
			usedMB := o.Usage[b.Resource] // agent-measured (RX+TX for bandwidth)
			s.suspendOverLimit(ctx, ws, b, usedMB)
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
// consumes the same hook) with the system-reserved bandwidth_exhausted
// reason + the quota metadata the agent needs to render the
// bandwidth-exhausted page (used/limit/period) without re-measuring, and
// records the system action in the audit log.
func (s *Server) suspendOverLimit(ctx context.Context, ws *websites.Website, b agentBreach, usedMB float64) {
	if ws.Status != websites.StatusReady {
		return // never suspend a non-running workload from a breach report
	}
	now := time.Now().UTC()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	resetsAt := periodStart.AddDate(0, 1, 0)
	// Byte surface for the stub page: the agent measures RX+TX bytes and the
	// outcome carries MB, so bytes are the rounded conversion (page display
	// precision only — the authoritative row stays the GREATEST high-water
	// upsert in workload_resource_usage).
	usedBytes := int64(math.Round(usedMB * 1024 * 1024))
	meta := map[string]any{
		"resource":     b.Resource,
		"usage":        b.Usage,
		"limit":        b.Limit,
		"used_bytes":   usedBytes,
		"period_start": periodStart.Format(time.RFC3339),
		"resets_at":    resetsAt.Format(time.RFC3339),
	}
	if limitMB, unlimited, err := s.effectiveBandwidthLimitMB(ctx, ws); err == nil && !unlimited && limitMB > 0 {
		meta["limit_bytes"] = limitMB * 1024 * 1024
	}
	payload := websites.SuspendPayload{
		WebsiteID: ws.ID.String(),
		Reason:    string(websites.ReasonBandwidthExhausted),
		Metadata:  meta,
	}
	key := "overlimit_suspend_" + b.Resource + "_" + ws.ID.String()
	if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeSuspendWebsite, payload, key); err != nil {
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
			"reason": string(websites.ReasonBandwidthExhausted),
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
// counter resets (nft reload) can never undercount a paid period. The
// row's limit_value is the site's EFFECTIVE limit (per-site override when
// set, else the plan) so display, page template and enforcement agree.
func (s *Server) accountBandwidth(ctx context.Context, ws *websites.Website, usedMB float64, measuredBy string) {
	limitMB, _, limitErr := s.effectiveBandwidthLimitMB(ctx, ws)
	if limitErr != nil {
		limitMB = 0
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO workload_resource_usage
			(organization_id, website_id, resource, period_start, used, limit_value, unit, measured_by)
		VALUES ($1, $2, 'bandwidth', date_trunc('month', now()), $3, $4, 'MB', $5)
		ON CONFLICT (website_id, resource, period_start) DO UPDATE SET
			used = GREATEST(workload_resource_usage.used, EXCLUDED.used),
			measured_by = EXCLUDED.measured_by,
			updated_at = now()
	`, ws.Organization, ws.ID, usedMB, limitMB, measuredBy)
	if err != nil {
		slog.Warn("bandwidth accounting upsert failed", "website", ws.ID, "err", err)
	}
}

// ============================================================================
// Effective quota resolution + the bandwidth resume guard (ADR-049 anti-drift:
// ONE resolution serves enforcement, the quota API, the resume guard and the
// suspension-page metadata).
// ============================================================================

// effectiveBandwidthLimitMB resolves the site's effective monthly bandwidth
// budget in MB: the per-site override (websites.bandwidth_limit_mb, migration
// 0050) when set — 0 = unlimited — else the hosting plan's max_bandwidth_mb
// through the unified engine (the SAME source the enforce payload uses; the
// dynamic tier scaling never touches bandwidth, per ADR-062).
// unlimited=true means "no cap" (skip every guard comparison).
func (s *Server) effectiveBandwidthLimitMB(ctx context.Context, ws *websites.Website) (limitMB int64, unlimited bool, err error) {
	if ws.BandwidthLimitMB != nil {
		if *ws.BandwidthLimitMB <= 0 {
			return 0, true, nil
		}
		return *ws.BandwidthLimitMB, false, nil
	}
	limits, err := s.ResourceLimits.LimitsForOrg(ctx, ws.Organization)
	if err != nil {
		return 0, false, err
	}
	r, ok := limits.Get(resources.ResBandwidth)
	if !ok || r.Limit <= 0 || r.LimitUnlimited {
		return 0, true, nil
	}
	return int64(r.Limit), false, nil
}

// currentMonthBandwidthUsedMB reads the period high-water row the enforce
// fanout persists. ok=false when nothing was accounted this month.
func (s *Server) currentMonthBandwidthUsedMB(ctx context.Context, websiteID uuid.UUID) (usedMB float64, ok bool, err error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT used FROM workload_resource_usage
		WHERE website_id = $1 AND resource = 'bandwidth'
		  AND period_start = date_trunc('month', now())
	`, websiteID)
	var used float64
	if err := row.Scan(&used); err != nil {
		return 0, false, err
	}
	return used, true, nil
}

// resumeBandwidthGuard is the websites.ResumeGuard implementation: a site
// suspended for bandwidth_exhausted cannot be resumed while its current-month
// usage is still at/over the effective limit (SPEC: quota exhausted + resume
// must NOT silently reactivate). Responses are 409 with the machine-readable
// code bandwidth_quota_exhausted unless the caller (this route is
// admin-gated) passes force=true — forced resumes are audited as
// website.resume_forced. Any other suspension reason resumes freely.
func (s *Server) resumeBandwidthGuard(ctx context.Context, ws *websites.Website, force bool) *httpapi.APIError {
	if ws.SuspensionReason == nil || *ws.SuspensionReason != string(websites.ReasonBandwidthExhausted) {
		return nil
	}
	limitMB, unlimited, err := s.effectiveBandwidthLimitMB(ctx, ws)
	if err != nil || unlimited || limitMB <= 0 {
		return nil // plan/override resolution failed open-free: resume allowed
	}
	used, ok, err := s.currentMonthBandwidthUsedMB(ctx, ws.ID)
	if err != nil || !ok {
		return nil // nothing accounted this month: the guard has no evidence
	}
	limitBytes := limitMB * 1024 * 1024
	usedBytes := int64(math.Round(used * 1024 * 1024))
	if usedBytes < limitBytes {
		return nil // quota was raised or the period rotated: resume allowed
	}
	if force {
		org := ws.Organization
		s.Audit.RecordBestEffort(ctx, audit.Entry{
			OrganizationID: &org,
			ActorType:      audit.ActorUser,
			Action:         "website.resume_forced",
			ResourceType:   "website",
			ResourceID:     ws.ID.String(),
			Metadata: map[string]any{
				"reason": string(websites.ReasonBandwidthExhausted),
				"usage":  usedBytes, "limit": limitBytes,
			},
		})
		return nil
	}
	return &httpapi.APIError{
		Status:  409,
		Code:    "bandwidth_quota_exhausted",
		Message: "the site cannot be resumed while its bandwidth quota remains exhausted (force: true to override)",
		Details: map[string]any{
			"used_bytes": usedBytes, "limit_bytes": limitBytes,
			"period": "monthly", "resets_at": nextMonthlyResetUTC().Format(time.RFC3339),
		},
	}
}

// nextMonthlyResetUTC is the start of the next month in UTC — the moment the
// agent-side counter rotation gives every site a fresh budget.
func nextMonthlyResetUTC() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
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

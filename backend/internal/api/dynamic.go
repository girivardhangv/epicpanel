package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	agentpkg "github.com/epicbyte/epicpanel/backend/internal/agent"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/packages"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
	"github.com/epicbyte/epicpanel/backend/internal/traffic"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// ============================================================================
// Dynamic resources — traffic-adaptive allocation + bot defense.
//
// Decision loop (30s): for every ready site opted in, read the traffic trend
// (multi-factor window scoring, internal/traffic) and drive the state
// machine:
//
//   active  — legit traffic: scale up on allocation pressure (cgroup usage
//             >= 80% of the current ceiling), scale down toward the floor
//             when idle. Suspect traffic for 2 windows -> busy.
//   busy    — protective mode: floor allocation (default 32MB), site still
//             served. M clean windows -> active.
//   suspended_attack — attack confirmed for 2 consecutive windows: site is
//             deallocated (floor) and DISABLED via the idempotent
//             suspend_website job (503 stub, no PHP, no proxy). Recovery is
//             manual (restore endpoint) unless dynamic_auto_resume_minutes
//             is set. A panel-wide or per-site disable also restores.
//
// Resource changes always converge through the SAME enforce_limits job the
// Phase 9 engine uses — one enforcement mechanism, no second control loop.
// ============================================================================

const (
	dynamicTickInterval    = 30 * time.Second
	dynamicScaleUpCooldown = 2 * time.Minute
	dynamicScaleDownCooldown = 5 * time.Minute
	dynamicIdleAfter         = 3 * time.Minute // no windows for this long = idle
)

// dynamicRuntime is per-process allocator memory (cooldowns). Tier/state
// truth lives in the websites table, so a control-plane restart converges.
type dynamicRuntime struct {
	mu        sync.Mutex
	lastScale map[uuid.UUID]time.Time
}

func (d *dynamicRuntime) canScale(id uuid.UUID, cooldown time.Duration, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastScale == nil {
		d.lastScale = map[uuid.UUID]time.Time{}
	}
	if t, ok := d.lastScale[id]; ok && now.Sub(t) < cooldown {
		return false
	}
	d.lastScale[id] = now
	return true
}

// trafficStore lazily builds the in-memory traffic store (nil-safe for
// tests that construct Server without it).
func (s *Server) trafficStore() *traffic.Store {
	s.dynInitOnce.Do(func() {
		if s.Traffic == nil {
			s.Traffic = traffic.NewStore()
		}
		s.dyn = &dynamicRuntime{}
	})
	return s.Traffic
}

// StartDynamicAllocator runs the decision loop until ctx is cancelled.
func (s *Server) StartDynamicAllocator(ctx context.Context) {
	s.trafficStore() // ensure runtime state exists
	ticker := time.NewTicker(dynamicTickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dynamicTick(ctx)
		}
	}
}

// ---------- panel configuration ----------

func (s *Server) dynEnabled(ctx context.Context) bool {
	if s.Settings == nil {
		return false
	}
	return s.Settings.GetBool(ctx, "dynamic_resources_enabled")
}

func (s *Server) dynFloorMB(ctx context.Context) int64 {
	if s.Settings == nil {
		return 32
	}
	v, err := s.Settings.Get(ctx, "dynamic_floor_memory_mb")
	if err != nil {
		return 32
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 16 || n > 1024 {
		return 32
	}
	return n
}

func (s *Server) dynAttackWindows(ctx context.Context) int { return s.dynIntSettingCtx(ctx, "dynamic_attack_windows", 2) }
func (s *Server) dynRecoverWindows(ctx context.Context) int { return s.dynIntSettingCtx(ctx, "dynamic_recover_windows", 4) }

func (s *Server) dynAutoResumeMinutes(ctx context.Context) int64 {
	return int64(s.dynIntSettingCtx(ctx, "dynamic_auto_resume_minutes", 0))
}

func (s *Server) dynIntSettingCtx(ctx context.Context, key string, def int) int {
	if s.Settings == nil {
		return def
	}
	v, err := s.Settings.Get(ctx, key)
	if err != nil {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// dynIntSetting is the ctx-less variant used inside handlers that already
// carry the request context (kept for call sites outside request scope).
func (s *Server) dynIntSetting(key string, def int) int {
	return s.dynIntSettingCtx(context.Background(), key, def)
}

// ---------- payloads: base (plan or perk) × tier ----------

// dynamicBasePayload resolves the tier-1 base payload for a site: the Free
// Perk overlay replaces the org plan when the site carries the perk.
func (s *Server) dynamicBasePayload(ctx context.Context, ws *websites.Website) (agentpkg.EnforceJobPayload, error) {
	if ws.FreePerk {
		p, ok, err := s.ResourceLimits.PerkPayload(ctx)
		if err != nil {
			return agentpkg.EnforceJobPayload{}, err
		}
		if ok {
			return s.enforceToAgentPayload(ctx, ws, p), nil
		}
		// No perk row (deleted): fall through to the org plan rather than
		// silently removing limits.
	}
	p, err := s.ResourceLimits.EnforcePayloadFor(ctx, ws.Organization, ws.ID)
	if err != nil {
		return agentpkg.EnforceJobPayload{}, err
	}
	return s.enforceToAgentPayload(ctx, ws, p), nil
}

// enforceToAgentPayload converts the engine payload and fills the agent-side
// reconciliation counts (same as every other enforce_limits enqueue site).
func (s *Server) enforceToAgentPayload(ctx context.Context, ws *websites.Website, p resources.EnforcePayload) agentpkg.EnforceJobPayload {
	return agentpkg.EnforceJobPayload{
		WebsiteID:      p.WebsiteID,
		Plan:           p.Plan,
		CPUPercent:     p.CPUPercent,
		MemoryMB:       p.MemoryMB,
		DiskMB:         p.DiskMB,
		BandwidthMB:    p.BandwidthMB,
		IOWeight:       resources.ClampIOWeight(p.IOWeight),
		PidsMax:        p.PidsMax,
		FpmMaxChildren: p.FpmMaxChildren,
		CountLimits:    p.CountLimits,
	}
}

// dynamicEffectivePayload is THE payload builder for dynamic-enabled sites:
// base limits scaled to the site's current tier when the feature is active
// (panel toggle on), unscaled base otherwise.
func (s *Server) dynamicEffectivePayload(ctx context.Context, ws *websites.Website) (agentpkg.EnforceJobPayload, error) {
	base, err := s.dynamicBasePayload(ctx, ws)
	if err != nil {
		return agentpkg.EnforceJobPayload{}, err
	}
	if !ws.DynamicEnabled || !s.dynEnabled(ctx) {
		return base, nil
	}
	tier := traffic.ClampTier(ws.DynamicTier)
	if ws.DynamicState != websites.DynStateActive {
		tier = 0 // busy/attacked sites always sit at the floor
	}
	if tier == 1 && ws.DynamicState == websites.DynStateActive {
		return base, nil
	}
	floor := s.dynFloorMB(ctx)
	scaled := traffic.Scale(traffic.BaseLimits{
		MemoryMB:    base.MemoryMB,
		CPUPercent:  base.CPUPercent,
		PidsMax:     base.PidsMax,
		FpmChildren: base.FpmMaxChildren,
	}, tier, floor)
	out := base
	out.MemoryMB = scaled.MemoryMB
	out.CPUPercent = scaled.CPUPercent
	out.PidsMax = scaled.PidsMax
	out.FpmMaxChildren = scaled.FpmChildren
	return out, nil
}

// enqueueDynamicEnforce converges the node to the site's current tier.
func (s *Server) enqueueDynamicEnforce(ctx context.Context, ws *websites.Website) {
	payload, err := s.dynamicEffectivePayload(ctx, ws)
	if err != nil {
		slog.Warn("dynamic enforce payload build failed", "website", ws.ID, "err", err)
		return
	}
	if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeEnforceLimits, payload,
		"enforce_limits_"+ws.ID.String()); err != nil {
		slog.Warn("dynamic enforce enqueue failed", "website", ws.ID, "err", err)
	}
}

// ---------- decision loop ----------

func (s *Server) dynamicTick(ctx context.Context) {
	if !s.dynEnabled(ctx) {
		s.dynamicSweepGlobalOff(ctx)
		return
	}
	sites, err := s.Websites.ListDynamicReady(ctx, 500)
	if err != nil {
		slog.Warn("dynamic allocator scan failed", "err", err)
		return
	}
	for i := range sites {
		s.dynamicEvaluate(ctx, &sites[i])
	}
}

// dynamicSweepGlobalOff restores every dynamic site to package base when the
// panel-wide toggle is off — including resuming attack-suspended sites, so
// turning the feature off returns the panel to its steady state.
func (s *Server) dynamicSweepGlobalOff(ctx context.Context) {
	sites, err := s.Websites.ListDynamicEnabled(ctx, 500)
	if err != nil {
		return
	}
	for i := range sites {
		ws := &sites[i]
		if ws.DynamicState == websites.DynStateActive && ws.DynamicTier == 1 {
			continue
		}
		if ws.DynamicState == websites.DynStateAttacked && ws.Status == websites.StatusSuspended {
			if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeResumeWebsite,
				websites.ResumePayload{WebsiteID: ws.ID.String()}, "dyn_restore_"+ws.ID.String()); err != nil {
				slog.Warn("dynamic off-sweep resume enqueue failed", "website", ws.ID, "err", err)
			}
		}
		if err := s.Websites.SetDynamicState(ctx, ws.ID, 1, websites.DynStateActive); err == nil {
			s.recordDynamicEvent(ctx, ws, "restore", ws.DynamicTier, 1, 0, "dynamic resources disabled panel-wide")
		}
	}
}

func (s *Server) dynamicEvaluate(ctx context.Context, ws *websites.Website) {
	floor := s.dynFloorMB(ctx)
	trend, seen := s.trafficStore().Trend(ws.ID)

	switch ws.DynamicState {
	case websites.DynStateAttacked:
		// Stay suspended until restored (API) or the auto-resume window
		// elapses (0 = manual only).
		if m := s.dynAutoResumeMinutes(ctx); m > 0 && seen && !trend.LastWindowAt.IsZero() {
			if since := time.Since(trend.LastWindowAt); since > time.Duration(m)*time.Minute {
				s.dynamicRestore(ctx, ws, fmt.Sprintf("auto-resume after %dm without attack traffic", m), audit.ActorSystem)
			}
		}
		return
	case websites.DynStateBusy:
		if seen && trend.CleanStreak >= s.dynRecoverWindows(ctx) {
			if err := s.Websites.SetDynamicState(ctx, ws.ID, 1, websites.DynStateActive); err == nil {
				s.recordDynamicEvent(ctx, ws, "scale_up", 0, 1, trend.LastVerdict.Score,
					fmt.Sprintf("traffic clean for %d windows; leaving protection mode", trend.CleanStreak))
				s.enqueueDynamicEnforce(ctx, ws)
			}
		}
		return
	}

	// --- active ---
	if seen && trend.AttackStreak >= s.dynAttackWindows(ctx) {
		s.dynamicSuspendUnderAttack(ctx, ws, trend)
		return
	}
	if seen && trend.BusyStreak >= 2 {
		if err := s.Websites.SetDynamicState(ctx, ws.ID, 0, websites.DynStateBusy); err == nil {
			s.recordDynamicEvent(ctx, ws, "busy", ws.DynamicTier, 0, trend.LastVerdict.Score,
				"suspect traffic: throttled to floor allocation (site still served)")
			ws.DynamicTier, ws.DynamicState = 0, websites.DynStateBusy
			s.enqueueDynamicEnforce(ctx, ws)
			s.publishDynamicEvent(ctx, ws, "website.dynamic_busy", map[string]any{
				"score": trend.LastVerdict.Score, "factors": trend.LastVerdict.Factors,
			})
		}
		return
	}
	s.dynamicAutoscale(ctx, ws, trend, seen, floor)
}

// dynamicSuspendUnderAttack deallocates the site (floor) and DISABLES it via
// the suspend_website job. Never touches non-ready sites.
func (s *Server) dynamicSuspendUnderAttack(ctx context.Context, ws *websites.Website, trend traffic.Trend) {
	if ws.Status != websites.StatusReady {
		return
	}
	reason := dynamicReason(trend)
	if err := s.Websites.SetDynamicState(ctx, ws.ID, 0, websites.DynStateAttacked); err != nil {
		return
	}
	ws.DynamicState = websites.DynStateAttacked
	s.recordDynamicEvent(ctx, ws, "suspend_attack", ws.DynamicTier, 0, trend.LastVerdict.Score, reason)

	// Deallocation: floor limits so the suspended site holds nothing.
	floorPayload, err := s.dynamicEffectivePayload(ctx, ws) // state=attacked → tier 0
	if err == nil {
		if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeEnforceLimits, floorPayload,
			"enforce_limits_"+ws.ID.String()); err != nil {
			slog.Warn("attack floor enforce enqueue failed", "website", ws.ID, "err", err)
		}
	}
	// Disable: the 503 stub vhost (same idempotent lifecycle job as manual
	// suspension — one suspension mechanism, no second control loop).
	if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeSuspendWebsite,
		websites.SuspendPayload{WebsiteID: ws.ID.String()}, "dyn_attack_suspend_"+ws.ID.String()); err != nil {
		slog.Error("attack suspension enqueue failed", "website", ws.ID, "err", err)
		return
	}
	org := ws.Organization
	s.Audit.RecordBestEffort(ctx, audit.Entry{
		OrganizationID: &org,
		ActorType:      audit.ActorSystem,
		Action:         "website.attack_suspended",
		ResourceType:   "website",
		ResourceID:     ws.ID.String(),
		Metadata:       map[string]any{"score": trend.LastVerdict.Score, "reason": reason, "streak": trend.AttackStreak},
	})
	s.publishDynamicEvent(ctx, ws, "website.attack_suspended", map[string]any{
		"score": trend.LastVerdict.Score, "reason": reason,
	})
	slog.Warn("dynamic: site suspended under attack", "website", ws.ID, "score", trend.LastVerdict.Score, "reason", reason)
}

// dynamicRestore returns a busy/attacked site to active at base tier.
func (s *Server) dynamicRestore(ctx context.Context, ws *websites.Website, reason, actor string) {
	if ws.DynamicState != websites.DynStateAttacked && ws.DynamicState != websites.DynStateBusy {
		return
	}
	if ws.Status == websites.StatusSuspended {
		if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeResumeWebsite,
			websites.ResumePayload{WebsiteID: ws.ID.String()}, "dyn_restore_"+ws.ID.String()); err != nil {
			slog.Warn("dynamic restore resume enqueue failed", "website", ws.ID, "err", err)
			return
		}
	}
	if err := s.Websites.SetDynamicState(ctx, ws.ID, 1, websites.DynStateActive); err != nil {
		return
	}
	s.recordDynamicEvent(ctx, ws, "restore", ws.DynamicTier, 1, 0, reason+" ("+actor+")")
	ws.DynamicState, ws.DynamicTier = websites.DynStateActive, 1
	s.enqueueDynamicEnforce(ctx, ws)
	if actor == audit.ActorSystem {
		s.publishDynamicEvent(ctx, ws, "website.dynamic_restored", map[string]any{"reason": reason})
	}
}

// dynamicAutoscale: legit traffic + allocation pressure → tier up; idle →
// tier down toward the floor. Only honest signals: cgroup usage against the
// CURRENT ceiling (same source the agent enforces) and window staleness.
func (s *Server) dynamicAutoscale(ctx context.Context, ws *websites.Website, trend traffic.Trend, seen bool, floor int64) {
	base, err := s.dynamicBasePayload(ctx, ws)
	if err != nil {
		return
	}
	tier := traffic.ClampTier(ws.DynamicTier)
	alloc := traffic.Scale(traffic.BaseLimits{
		MemoryMB:    base.MemoryMB,
		CPUPercent:  base.CPUPercent,
		PidsMax:     base.PidsMax,
		FpmChildren: base.FpmMaxChildren,
	}, tier, floor)

	cpu, mem := s.liveSiteUsage(ws)
	allocMemBytes := alloc.MemoryMB * 1024 * 1024
	pressure := (allocMemBytes > 0 && float64(mem) >= 0.8*float64(allocMemBytes)) ||
		(alloc.CPUPercent > 0 && cpu >= 0.8*alloc.CPUPercent)
	idle := (!seen || time.Since(trend.LastWindowAt) > dynamicIdleAfter) &&
		float64(mem) < 0.15*float64(allocMemBytes) && cpu < 15

	now := time.Now()
	switch {
	case tier < traffic.MaxTier && pressure && s.dyn.canScale(ws.ID, dynamicScaleUpCooldown, now):
		newTier := tier + 1
		if err := s.Websites.SetDynamicState(ctx, ws.ID, newTier, websites.DynStateActive); err != nil {
			return
		}
		ws.DynamicTier = newTier
		s.recordDynamicEvent(ctx, ws, "scale_up", tier, newTier, 0,
			fmt.Sprintf("allocation pressure (%d%% of memory ceiling, %.0f%% CPU)", pctOf(mem, allocMemBytes), cpu))
		s.enqueueDynamicEnforce(ctx, ws)
	case tier > 0 && idle && s.dyn.canScale(ws.ID, dynamicScaleDownCooldown, now):
		newTier := tier - 1
		if err := s.Websites.SetDynamicState(ctx, ws.ID, newTier, websites.DynStateActive); err != nil {
			return
		}
		ws.DynamicTier = newTier
		s.recordDynamicEvent(ctx, ws, "scale_down", tier, newTier, 0,
			fmt.Sprintf("idle (floor: %dMB)", floor))
		s.enqueueDynamicEnforce(ctx, ws)
	}
}

// liveSiteUsage reads the site's live cgroup usage from the metrics
// LiveStore (the same per-site samples the UI shows).
func (s *Server) liveSiteUsage(ws *websites.Website) (cpuPercent float64, memBytes int64) {
	if s.LiveStore == nil {
		return 0, 0
	}
	lv := s.LiveStore.Snapshot(ws.ServerID)
	if lv == nil {
		return 0, 0
	}
	site, ok := lv.Sites[ws.ID.String()]
	if !ok {
		return 0, 0
	}
	return site.CPUPercent, site.MemoryBytes
}

// dynamicReason renders the top weighted factors for audit/event payloads.
func dynamicReason(trend traffic.Trend) string {
	reason := "attack pattern: "
	for i, f := range trend.LastVerdict.Factors {
		if i > 0 {
			reason += "; "
		}
		reason += fmt.Sprintf("%s (%s)", f.Name, f.Detail)
		if i >= 3 {
			break
		}
	}
	return reason
}

func pctOf(v, total int64) int {
	if total <= 0 {
		return 0
	}
	return int(float64(v) / float64(total) * 100)
}

// ---------- events + audit ----------

func (s *Server) recordDynamicEvent(ctx context.Context, ws *websites.Website, kind string, fromTier, toTier int, score float64, reason string) {
	org := ws.Organization
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO dynamic_resource_events (website_id, organization_id, kind, from_tier, to_tier, score, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ws.ID, org, kind, fromTier, toTier, score, reason); err != nil {
		slog.Warn("dynamic event insert failed", "website", ws.ID, "err", err)
	}
}

func (s *Server) publishDynamicEvent(ctx context.Context, ws *websites.Website, eventType string, payload map[string]any) {
	if s.Events == nil {
		return
	}
	org := ws.Organization
	payload["website_id"] = ws.ID.String()
	payload["name"] = ws.Name
	s.Events.Publish(ctx, events.Event{
		Type:         eventType,
		Organization: &org,
		ActorType:    "system",
		ResourceType: "website",
		ResourceID:   ws.ID.String(),
		Payload:      payload,
	})
}

// ============================================================================
// HTTP API — dynamic resources + Free Perk.
// ============================================================================

// RegisterDynamicRoutes wires the endpoints. resolveOrg is the shared org
// resolver (servers.Handler.ResolveOrg) so the routes honor the same RBAC,
// token-scope and org-alias machinery as every other org-scoped surface.
func (s *Server) RegisterDynamicRoutes(mux *http.ServeMux, resolveOrg func(*http.Request, string, organizations.Role) (uuid.UUID, *httpapi.APIError)) {
	s.trafficStore()

	orgResolved := func(role organizations.Role, next func(http.ResponseWriter, *http.Request, uuid.UUID)) http.HandlerFunc {
		return httpapi.RequireUser(func(w http.ResponseWriter, r *http.Request) {
			orgID, apiErr := resolveOrg(r, r.PathValue("org_id"), role)
			if apiErr != nil {
				httpapi.RespondError(w, apiErr)
				return
			}
			next(w, r, orgID)
		})
	}
	siteResolved := func(role organizations.Role, next func(http.ResponseWriter, *http.Request, *websites.Website)) http.HandlerFunc {
		return orgResolved(role, func(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) {
			websiteID, err := uuid.Parse(r.PathValue("website_id"))
			if err != nil {
				httpapi.RespondError(w, httpapi.ErrValidation("invalid website id"))
				return
			}
			ws, err := s.Websites.GetByID(r.Context(), orgID, websiteID)
			if err == websites.ErrNotFound {
				httpapi.RespondError(w, httpapi.ErrNotFound("website not found"))
				return
			}
			if err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
			next(w, r, ws)
		})
	}

	// Site-scoped (developer+ to mutate; billing+ to read).
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/dynamic",
		siteResolved(organizations.RoleBilling, s.getDynamicStatus))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/websites/{website_id}/dynamic",
		siteResolved(organizations.RoleDeveloper, s.patchDynamic))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/dynamic/restore",
		siteResolved(organizations.RoleDeveloper, s.postDynamicRestore))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/free-perk",
		siteResolved(organizations.RoleDeveloper, s.postFreePerk))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/websites/{website_id}/free-perk",
		siteResolved(organizations.RoleAdmin, s.deleteFreePerk))
	mux.HandleFunc("GET /v1/organizations/{org_id}/free-perk",
		orgResolved(organizations.RoleBilling, s.getOrgFreePerk))

	// Panel-wide configuration + fleet overview (platform admin).
	mux.HandleFunc("GET /v1/admin/dynamic", httpapi.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		user, _ := httpapi.UserFrom(r.Context())
		if user.Role != "admin" {
			httpapi.RespondError(w, httpapi.ErrForbidden("platform administrator session required"))
			return
		}
		s.getAdminDynamic(w, r)
	}))
	mux.HandleFunc("PATCH /v1/admin/dynamic", httpapi.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		user, _ := httpapi.UserFrom(r.Context())
		if user.Role != "admin" {
			httpapi.RespondError(w, httpapi.ErrForbidden("platform administrator session required"))
			return
		}
		s.patchAdminDynamic(w, r)
	}))
}

// dynamicStatusResponse is the GET .../dynamic payload.
type dynamicStatusResponse struct {
	Enabled         bool                       `json:"enabled"`
	GlobalEnabled   bool                       `json:"global_enabled"`
	Tier            int                        `json:"tier"`
	State           string                     `json:"state"`
	FloorMemoryMB   int64                      `json:"floor_memory_mb"`
	FreePerk        bool                       `json:"free_perk"`
	EffectiveLimits map[string]any             `json:"effective_limits"`
	Trend           *traffic.Trend             `json:"trend,omitempty"`
	Windows         []agentprotoWindowSnapshot `json:"recent_windows,omitempty"`
	Events          []dynamicEventRow          `json:"recent_events,omitempty"`
}

type agentprotoWindowSnapshot struct {
	WindowS      int     `json:"window_s"`
	Requests     int64   `json:"requests"`
	UniqueIPs    int     `json:"unique_ips"`
	Top3Share    float64 `json:"top3_share"`
	Status4xx    int64   `json:"status_4xx"`
	UABadTool    int64   `json:"ua_bad_tool"`
	UAEmpty      int64   `json:"ua_empty"`
	UAGoodBot    int64   `json:"ua_good_bot"`
	NotFoundReqs int64   `json:"not_found_reqs"`
}

type dynamicEventRow struct {
	Kind      string    `json:"kind"`
	FromTier  *int      `json:"from_tier"`
	ToTier    *int      `json:"to_tier"`
	Score     float64   `json:"score"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) getDynamicStatus(w http.ResponseWriter, r *http.Request, ws *websites.Website) {
	ctx := r.Context()
	floor := s.dynFloorMB(ctx)
	base, err := s.dynamicBasePayload(ctx, ws)
	var effective map[string]any
	if err == nil {
		payload, err := s.dynamicEffectivePayload(ctx, ws)
		if err == nil {
			effective = map[string]any{
				"memory_mb": payload.MemoryMB, "cpu_percent": payload.CPUPercent,
				"pids_max": payload.PidsMax, "fpm_max_children": payload.FpmMaxChildren,
				"disk_mb": payload.DiskMB,
			}
		}
	} else {
		effective = map[string]any{"error": "limits unavailable"}
	}
	resp := dynamicStatusResponse{
		Enabled: ws.DynamicEnabled, GlobalEnabled: s.dynEnabled(ctx),
		Tier: traffic.ClampTier(ws.DynamicTier), State: ws.DynamicState,
		FloorMemoryMB: floor, FreePerk: ws.FreePerk, EffectiveLimits: effective,
	}
	if trend, ok := s.trafficStore().Trend(ws.ID); ok {
		resp.Trend = &trend
		resp.Windows = []agentprotoWindowSnapshot{}
		for _, win := range s.trafficStore().Recent(ws.ID) {
			resp.Windows = append(resp.Windows, agentprotoWindowSnapshot{
				WindowS: win.WindowS, Requests: win.Requests, UniqueIPs: win.UniqueIPs,
				Top3Share: win.Top3Share, Status4xx: win.Status4xx, UABadTool: win.UABadTool,
				UAEmpty: win.UAEmpty, UAGoodBot: win.UAGoodBot, NotFoundReqs: win.NotFoundReqs,
			})
		}
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT kind, from_tier, to_tier, score, reason, created_at
		FROM dynamic_resource_events WHERE website_id = $1
		ORDER BY created_at DESC LIMIT 20`, ws.ID)
	if err == nil {
		defer rows.Close()
		resp.Events = []dynamicEventRow{}
		for rows.Next() {
			var e dynamicEventRow
			var from, to *int
			if err := rows.Scan(&e.Kind, &from, &to, &e.Score, &e.Reason, &e.CreatedAt); err == nil {
				e.FromTier, e.ToTier = from, to
				resp.Events = append(resp.Events, e)
			}
		}
	}
	_ = base
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

// PATCH .../dynamic — {enabled: bool} per-site toggle. Disabling restores
// the site (resume from attack suspension) and converges to package base.
func (s *Server) patchDynamic(w http.ResponseWriter, r *http.Request, ws *websites.Website) {
	ctx := r.Context()
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if req.Enabled == nil {
		httpapi.RespondError(w, httpapi.ErrValidation("enabled is required"))
		return
	}
	user, _ := httpapi.UserFrom(ctx)
	var actorID *uuid.UUID
	if uid, err := uuid.Parse(user.ID); err == nil {
		actorID = &uid
	}
	enabled := *req.Enabled
	prev := ws.DynamicEnabled
	if err := s.Websites.SetDynamicEnabled(ctx, ws.ID, enabled); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	org := ws.Organization
	s.Audit.RecordBestEffort(ctx, audit.Entry{
		OrganizationID: &org, ActorType: audit.ActorUser, ActorUserID: actorID,
		Action: "website.dynamic_" + map[bool]string{true: "enabled", false: "disabled"}[enabled],
		ResourceType: "website", ResourceID: ws.ID.String(),
	})
	s.recordDynamicEvent(ctx, ws,
		map[bool]string{true: "enable", false: "disable"}[enabled],
		ws.DynamicTier, map[bool]int{true: ws.DynamicTier, false: 1}[enabled],
		0, "per-site toggle set to "+map[bool]string{true: "on", false: "off"}[enabled])

	if !enabled {
		// Feature off for this site: restore + base limits (package or perk).
		if ws.Status == websites.StatusSuspended && ws.DynamicState == websites.DynStateAttacked {
			if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeResumeWebsite,
				websites.ResumePayload{WebsiteID: ws.ID.String()}, "dyn_restore_"+ws.ID.String()); err != nil {
				slog.Warn("dynamic disable resume enqueue failed", "website", ws.ID, "err", err)
			}
		}
		fresh, err := s.Websites.GetByIDAny(ctx, ws.ID)
		if err == nil && fresh != nil {
			s.enqueueDynamicEnforce(ctx, fresh)
		}
	} else if !prev {
		// Fresh enable: start at base tier; the allocator takes over.
		_ = s.Websites.SetDynamicState(ctx, ws.ID, 1, websites.DynStateActive)
		fresh, err := s.Websites.GetByIDAny(ctx, ws.ID)
		if err == nil && fresh != nil {
			s.enqueueDynamicEnforce(ctx, fresh)
		}
	}
	ws.DynamicEnabled = enabled
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"enabled": enabled, "tier": traffic.ClampTier(ws.DynamicTier), "state": ws.DynamicState})
}

// POST .../dynamic/restore — manual recovery from attack suspension.
func (s *Server) postDynamicRestore(w http.ResponseWriter, r *http.Request, ws *websites.Website) {
	ctx := r.Context()
	if ws.DynamicState != websites.DynStateAttacked && ws.DynamicState != websites.DynStateBusy {
		httpapi.RespondError(w, httpapi.ErrConflict("site is not suspended or throttled by the allocator"))
		return
	}
	user, _ := httpapi.UserFrom(ctx)
	s.dynamicRestore(ctx, ws, "restored via API", user.ID)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"state": websites.DynStateActive, "tier": 1})
}

// POST .../free-perk — assign the Free Perk overlay to one site.
func (s *Server) postFreePerk(w http.ResponseWriter, r *http.Request, ws *websites.Website) {
	ctx := r.Context()
	if ws.FreePerk {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"free_perk": true})
		return
	}
	perkCap := s.dynIntSettingCtx(ctx, "free_perk_max_sites_per_user", 1)
	used, err := s.Websites.CountFreePerk(ctx, ws.Organization)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if used >= perkCap {
		httpapi.RespondError(w, httpapi.ErrConflict(fmt.Sprintf(
			"free perk limit reached (%d of %d sites); raise the limit in panel settings",
			used, perkCap)))
		return
	}
	pkg, ok, err := s.perkPackage(ctx)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(fmt.Errorf("free perk package row missing")))
		return
	}
	if err := s.Websites.SetFreePerk(ctx, ws.ID, true); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	user, _ := httpapi.UserFrom(ctx)
	var actorID *uuid.UUID
	if uid, err := uuid.Parse(user.ID); err == nil {
		actorID = &uid
	}
	org := ws.Organization
	s.Audit.RecordBestEffort(ctx, audit.Entry{
		OrganizationID: &org, ActorType: audit.ActorUser, ActorUserID: actorID,
		Action: "website.free_perk_assigned", ResourceType: "website", ResourceID: ws.ID.String(),
		Metadata: map[string]any{"package": pkg.Name},
	})
	s.recordDynamicEvent(ctx, ws, "perk_assigned", 0, 0, 0, "Free Perk assigned")
	fresh, err := s.Websites.GetByIDAny(ctx, ws.ID)
	if err == nil && fresh != nil {
		s.enqueueDynamicEnforce(ctx, fresh)
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"free_perk": true})
}

// DELETE .../free-perk — remove the overlay (back to the org plan).
func (s *Server) deleteFreePerk(w http.ResponseWriter, r *http.Request, ws *websites.Website) {
	ctx := r.Context()
	if !ws.FreePerk {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"free_perk": false})
		return
	}
	if err := s.Websites.SetFreePerk(ctx, ws.ID, false); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	user, _ := httpapi.UserFrom(ctx)
	var actorID *uuid.UUID
	if uid, err := uuid.Parse(user.ID); err == nil {
		actorID = &uid
	}
	org := ws.Organization
	s.Audit.RecordBestEffort(ctx, audit.Entry{
		OrganizationID: &org, ActorType: audit.ActorUser, ActorUserID: actorID,
		Action: "website.free_perk_removed", ResourceType: "website", ResourceID: ws.ID.String(),
	})
	s.recordDynamicEvent(ctx, ws, "perk_removed", 0, 0, 0, "Free Perk removed")
	fresh, err := s.Websites.GetByIDAny(ctx, ws.ID)
	if err == nil && fresh != nil {
		s.enqueueDynamicEnforce(ctx, fresh)
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"free_perk": false})
}

// GET /v1/organizations/{org_id}/free-perk — org view of the perk.
func (s *Server) getOrgFreePerk(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) {
	ctx := r.Context()
	perkCap := s.dynIntSettingCtx(ctx, "free_perk_max_sites_per_user", 1)
	used, err := s.Websites.CountFreePerk(ctx, orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	resp := map[string]any{"max_sites": perkCap, "used": used, "remaining": perkCap - used}
	if pkg, ok, err := s.perkPackage(ctx); err == nil && ok {
		resp["package"] = map[string]any{
			"id": pkg.ID, "name": pkg.Name, "memory_limit_mb": pkg.MemoryLimitMB,
			"max_disk_mb": pkg.MaxDiskMB, "cpu_cores": pkg.CPUCores,
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

// perkPackage returns the free_perk hosting_packages row (nil-safe).
func (s *Server) perkPackage(ctx context.Context) (*packages.Package, bool, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
			max_addon_domains, max_subdomains, allowed_runtimes, max_bandwidth_mb, io_weight, max_processes,
			max_ports, max_backups, price_monthly_cents, is_default, created_at
		FROM hosting_packages WHERE kind = 'free_perk' ORDER BY created_at LIMIT 1`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, false, nil
	}
	var p packages.Package
	if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.MaxWebsites, &p.MaxDatabases, &p.MaxDiskMB, &p.MemoryLimitMB,
		&p.CPUCores, &p.MaxAddonDomains, &p.MaxSubdomains, &p.AllowedRuntimes,
		&p.MaxBandwidthMB, &p.IOWeight, &p.MaxProcesses, &p.MaxPorts, &p.MaxBackups,
		&p.PriceMonthly, &p.IsDefault, &p.CreatedAt); err != nil {
		return nil, false, err
	}
	return &p, true, nil
}

// ---------- admin panel-wide config ----------

func (s *Server) getAdminDynamic(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := map[string]any{
		"enabled":               s.dynEnabled(ctx),
		"floor_memory_mb":       s.dynFloorMB(ctx),
		"attack_windows":        s.dynAttackWindows(ctx),
		"recover_windows":       s.dynRecoverWindows(ctx),
		"auto_resume_minutes":   s.dynAutoResumeMinutes(ctx),
		"free_perk_max_per_org": s.dynIntSettingCtx(ctx, "free_perk_max_sites_per_user", 1),
	}
	if pkg, ok, err := s.perkPackage(ctx); err == nil && ok {
		resp["free_perk_package"] = map[string]any{
			"id": pkg.ID, "name": pkg.Name, "memory_limit_mb": pkg.MemoryLimitMB,
			"max_disk_mb": pkg.MaxDiskMB, "cpu_cores": pkg.CPUCores,
		}
	}
	sites := []map[string]any{}
	rows, err := s.Pool.Query(ctx, `
		SELECT w.id, w.name, w.primary_domain, w.organization_id, o.name,
		       w.dynamic_enabled, w.dynamic_tier, w.dynamic_state, w.free_perk
		FROM websites w LEFT JOIN organizations o ON o.id = w.organization_id
		WHERE (w.dynamic_enabled OR w.free_perk OR w.dynamic_state <> 'active')
		  AND w.status NOT IN ('deleted', 'deleting')
		ORDER BY w.updated_at DESC LIMIT 500`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, orgID uuid.UUID
			var name, domain string
			var orgName *string
			var enabled, perk bool
			var tier int
			var state string
			if err := rows.Scan(&id, &name, &domain, &orgID, &orgName, &enabled, &tier, &state, &perk); err == nil {
				row := map[string]any{
					"id": id, "name": name, "primary_domain": domain, "organization_id": orgID,
					"organization_name": orgName, "dynamic_enabled": enabled,
					"tier": traffic.ClampTier(tier), "state": state, "free_perk": perk,
				}
				if trend, ok := s.trafficStore().Trend(id); ok {
					row["last_score"] = trend.LastVerdict.Score
					row["last_class"] = trend.LastVerdict.Class
					row["last_window_requests"] = trend.LastWindow
				}
				sites = append(sites, row)
			}
		}
	}
	resp["sites"] = sites
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) patchAdminDynamic(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		Enabled            *bool `json:"enabled"`
		FloorMemoryMB      *int64 `json:"floor_memory_mb"`
		AttackWindows      *int   `json:"attack_windows"`
		RecoverWindows     *int   `json:"recover_windows"`
		AutoResumeMinutes  *int64 `json:"auto_resume_minutes"`
		FreePerkMaxPerOrg  *int   `json:"free_perk_max_per_org"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	user, _ := httpapi.UserFrom(ctx)
	var actorID *uuid.UUID
	if uid, err := uuid.Parse(user.ID); err == nil {
		actorID = &uid
	}
	set := func(key string, val string) {
		if err := s.Settings.Set(ctx, key, val); err != nil {
			slog.Warn("dynamic setting write failed", "key", key, "err", err)
		}
	}
	if req.Enabled != nil {
		set("dynamic_resources_enabled", map[bool]string{true: "true", false: "false"}[*req.Enabled])
	}
	if req.FloorMemoryMB != nil {
		floor := *req.FloorMemoryMB
		if floor < 16 || floor > 1024 {
			httpapi.RespondError(w, httpapi.ErrValidation("floor_memory_mb must be between 16 and 1024"))
			return
		}
		set("dynamic_floor_memory_mb", strconv.FormatInt(floor, 10))
	}
	if req.AttackWindows != nil {
		if *req.AttackWindows < 1 || *req.AttackWindows > 60 {
			httpapi.RespondError(w, httpapi.ErrValidation("attack_windows must be between 1 and 60"))
			return
		}
		set("dynamic_attack_windows", strconv.Itoa(*req.AttackWindows))
	}
	if req.RecoverWindows != nil {
		if *req.RecoverWindows < 1 || *req.RecoverWindows > 120 {
			httpapi.RespondError(w, httpapi.ErrValidation("recover_windows must be between 1 and 120"))
			return
		}
		set("dynamic_recover_windows", strconv.Itoa(*req.RecoverWindows))
	}
	if req.AutoResumeMinutes != nil {
		if *req.AutoResumeMinutes < 0 || *req.AutoResumeMinutes > 60*24*30 {
			httpapi.RespondError(w, httpapi.ErrValidation("auto_resume_minutes out of range"))
			return
		}
		set("dynamic_auto_resume_minutes", strconv.FormatInt(*req.AutoResumeMinutes, 10))
	}
	if req.FreePerkMaxPerOrg != nil {
		if *req.FreePerkMaxPerOrg < 0 || *req.FreePerkMaxPerOrg > 1000 {
			httpapi.RespondError(w, httpapi.ErrValidation("free_perk_max_per_org out of range"))
			return
		}
		set("free_perk_max_sites_per_user", strconv.Itoa(*req.FreePerkMaxPerOrg))
	}
	s.Audit.RecordBestEffort(ctx, audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: actorID,
		Action: "settings.dynamic_resources_updated", ResourceType: "settings",
		Metadata: map[string]any{"request": req},
	})
	s.getAdminDynamic(w, r)
}

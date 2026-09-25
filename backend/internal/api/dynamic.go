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
	"github.com/epicbyte/epicpanel/backend/internal/dynres"
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
// Two INDEPENDENT analyzers combine only at the state-machine layer:
//
//   SECURITY (60s windows, internal/traffic): multi-factor scoring of
//   access-log windows drives the site state — legit / busy (protective
//   floor) / suspended_attack (suspend_website job). It never moves tiers.
//
//   RESOURCES (15s decisions, internal/dynres): per-site pressure
//   max(cpu, memory, pids, fpm) against the CURRENT ceiling, smoothed with
//   an EWMA (α=0.25), with asymmetric hysteresis:
//     scale up   — pressure ≥80% sustained ~30s, or ≥70% and rising fast
//                  (prediction window); one tier at a time, 60s cooldown
//     scale down — pressure <25% sustained 4 min; one tier, 5min cooldown
//   Every scale-up passes the safety governor (node RAM reserve, fleet
//   allocation cap, global scale-up rate limit, per-site ceiling).
//
// State machine (state in the websites table — restart-convergent):
//
//   active  — resource engine scales tiers 1..maxTier
//   busy    — suspect traffic: floored, still served; N clean windows -> active
//   suspended_attack — attack confirmed: floored + DISABLED via the
//             idempotent suspend_website job. Recovery is manual (restore
//             endpoint) unless dynamic_auto_resume_minutes is set.
//
// Resource changes always converge through the SAME enforce_limits job the
// Phase 9 engine uses — one enforcement mechanism, no second control loop.
// Live visibility: every tick pushes throttled website.resource_update
// snapshots on the WS bus; tier/state changes publish immediately.
// ============================================================================

const (
	dynamicTickInterval = 15 * time.Second // resource decision cadence

	dynamicPushMinInterval = 5 * time.Second  // resource_update throttle
	dynamicPushHeartbeat   = 30 * time.Second // periodic snapshot even when steady
)

// dynamicRuntime is per-process allocator memory: engines (EWMA + cooldown
// state), the fleet scale-up rate limiter and push throttles. Tier/state
// truth lives in the websites table, so a control-plane restart converges.
type dynamicRuntime struct {
	mu             sync.Mutex
	engines        map[uuid.UUID]*dynres.Engine
	scaleUps       []time.Time             // global rate limiter (per minute)
	lastPush       map[uuid.UUID]time.Time // resource_update throttle
	lastPushedMax  map[uuid.UUID]float64
}

func (d *dynamicRuntime) engineFor(id uuid.UUID, tier int) *dynres.Engine {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.engines == nil {
		d.engines = map[uuid.UUID]*dynres.Engine{}
	}
	e, ok := d.engines[id]
	if !ok || e.Tier != tier {
		// First sight (or tier changed outside the engine): re-seed at the
		// stored tier. Restart/tier-drift converges here.
		e = dynres.New(tier)
		d.engines[id] = e
	}
	return e
}

func (d *dynamicRuntime) scaleUpsLastMinute(now time.Time) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	kept := d.scaleUps[:0]
	for _, t := range d.scaleUps {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	d.scaleUps = kept
	return len(d.scaleUps)
}

func (d *dynamicRuntime) recordScaleUp(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scaleUps = append(d.scaleUps, now)
}

// peekEngine returns the site's engine without creating one (read paths).
func (d *dynamicRuntime) peekEngine(id uuid.UUID) *dynres.Engine {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.engines[id]
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

// dynScaledInt reads an int setting, clamped to [min,max]; out-of-range or
// unparseable values fall back to def (never silently disable the engine).
func (s *Server) dynScaledInt(ctx context.Context, key string, def, min, max int) int {
	n := s.dynIntSettingCtx(ctx, key, def)
	if n < min || n > max {
		return def
	}
	return n
}

func (s *Server) dynMaxTier(ctx context.Context) int {
	return s.dynScaledInt(ctx, "dynamic_max_tier", traffic.MaxTier, 1, traffic.MaxTier)
}

func (s *Server) dynUpCooldown(ctx context.Context) time.Duration {
	return time.Duration(s.dynScaledInt(ctx, "dynamic_scale_up_cooldown_s", 60, 30, 600)) * time.Second
}

func (s *Server) dynDownCooldown(ctx context.Context) time.Duration {
	return time.Duration(s.dynScaledInt(ctx, "dynamic_scale_down_cooldown_s", 300, 60, 3600)) * time.Second
}

func (s *Server) dynGlobalCapPercent(ctx context.Context) int {
	return s.dynScaledInt(ctx, "dynamic_global_cap_percent", 75, 0, 100)
}

func (s *Server) dynMaxScaleUpsPerMinute(ctx context.Context) int {
	return s.dynScaledInt(ctx, "dynamic_max_scaleups_per_minute", 10, 0, 1000)
}

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
	// Per-site bandwidth override (migration 0050) rides the shared payload
	// so the agent-side budget, the quota API, the resume guard and the
	// suspension-page metadata all resolve the SAME number (ADR-049
	// anti-drift). Bandwidth is never tier-scaled (traffic.Scale does not
	// touch it), so injecting here covers every downstream path.
	if ws.BandwidthLimitMB != nil {
		base.BandwidthMB = *ws.BandwidthLimitMB
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
			return
		}
		// No fresh traffic evidence at all (agent restart, quiet site whose
		// only hits were excluded self-traffic): a busy site nobody is
		// touching has no reason to stay throttled. Do not wait on a
		// clean-window streak that may never be delivered.
		if !seen || time.Since(trend.LastWindowAt) > 6*time.Minute {
			if err := s.Websites.SetDynamicState(ctx, ws.ID, 1, websites.DynStateActive); err == nil {
				s.recordDynamicEvent(ctx, ws, "scale_up", 0, 1, 0, "no traffic evidence while throttled; leaving protection mode")
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
	s.dynamicEngineStep(ctx, ws)
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
	// suspension — one suspension mechanism, no second control loop). The
	// 'attack' reason rides the payload (migration 0050) so the persisted
	// suspension reason and the stub page match the dynamic_state machinery.
	if _, err := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeSuspendWebsite,
		websites.SuspendPayload{WebsiteID: ws.ID.String(), Reason: string(websites.ReasonAttack)},
		"dyn_attack_suspend_"+ws.ID.String()); err != nil {
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
	// A manual restore is an operator decision: clear stale busy/attack
	// streaks so the site doesn't bounce straight back on the next tick.
	s.trafficStore().ResetStreaks(ws.ID)
	if ws.Status == websites.StatusSuspended {
		// Resume guard (migration 0050): never auto-restore a site that is
		// actually suspended for bandwidth_exhausted with the quota still
		// exhausted — an API outage or stale dynamic state must not bring a
		// quota-exhausted site back (agent retains the last enforced state).
		if apiErr := s.resumeBandwidthGuard(ctx, ws, false); apiErr != nil {
			slog.Warn("dynamic restore blocked: bandwidth quota still exhausted", "website", ws.ID)
			return
		}
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

// dynamicEngineStep feeds the site's freshest live sample to the resource
// engine and, on a decision, passes it through the safety governor before
// anything touches the node. Called only for ACTIVE sites (busy/attacked
// sites are floored by the security path, never scaled here).
func (s *Server) dynamicEngineStep(ctx context.Context, ws *websites.Website) {
	now := time.Now()
	e := s.dyn.engineFor(ws.ID, traffic.ClampTier(ws.DynamicTier))
	// Convergence: the pre-ADR-062a engine let ACTIVE sites idle down to
	// tier 0; active sites now floor at tier 1 (tier 0 is the busy/attacked
	// floor state). Heal legacy rows on first sight.
	if ws.DynamicTier < 1 {
		if err := s.Websites.SetDynamicState(ctx, ws.ID, 1, websites.DynStateActive); err == nil {
			ws.DynamicTier = 1
			s.recordDynamicEvent(ctx, ws, "scale_up", 0, 1, 0, "active sites floor at tier 1 (engine upgrade convergence)")
			s.enqueueDynamicEnforce(ctx, ws)
		}
	}

	sample := s.liveSiteSample(ws)
	pressures := e.Observe(sample.Observation())
	d := e.Decide(now, s.dynMaxTier(ctx), s.dynUpCooldown(ctx), s.dynDownCooldown(ctx))
	if d.Action == dynres.ActionNone {
		s.publishResourceUpdate(ctx, ws, e, sample)
		return
	}

	base, err := s.dynamicBasePayload(ctx, ws)
	if err != nil {
		return
	}
	floor := s.dynFloorMB(ctx)
	scaleAt := func(tier int) traffic.BaseLimits {
		return traffic.Scale(traffic.BaseLimits{
			MemoryMB:    base.MemoryMB,
			CPUPercent:  base.CPUPercent,
			PidsMax:     base.PidsMax,
			FpmChildren: base.FpmMaxChildren,
		}, tier, floor)
	}
	cur, des := scaleAt(d.FromTier), scaleAt(d.ToTier)
	nodeTotal, nodeAvail := s.nodeMemory(ws.ServerID)
	globalAllocMB := s.dynamicFleetAllocatedMB(ctx, ws, floor)

	v := dynres.Govern(dynres.GovernorInput{
		Tier: d.FromTier, DesiredTier: d.ToTier,
		CurrentMemBytes: cur.MemoryMB * 1024 * 1024, DesiredMemBytes: des.MemoryMB * 1024 * 1024,
		NodeMemTotalBytes: nodeTotal, NodeMemAvailableBytes: nodeAvail,
		GlobalDynamicAllocatedBytes: globalAllocMB * 1024 * 1024,
		GlobalCapPercent:            s.dynGlobalCapPercent(ctx),
		ScaleUpsLastMinute:          s.dyn.scaleUpsLastMinute(now),
		MaxScaleUpsPerMinute:        s.dynMaxScaleUpsPerMinute(ctx),
		MaxTier:                     s.dynMaxTier(ctx),
	})
	if !v.Allowed {
		// Governor denial is an event, not an error: record it (score-free,
		// throttled by the push path) so the UI can show WHY the site did
		// not scale.
		s.recordDynamicEvent(ctx, ws, "governor_denied", d.FromTier, d.FromTier, 0,
			fmt.Sprintf("%s blocked: %s", d.Action, v.Reason))
		s.publishResourceUpdate(ctx, ws, e, sample)
		return
	}

	if err := s.Websites.SetDynamicState(ctx, ws.ID, v.EffectiveTier, websites.DynStateActive); err != nil {
		return
	}
	if d.Action == dynres.ActionScaleUp {
		s.dyn.recordScaleUp(now)
	}
	ws.DynamicTier = v.EffectiveTier
	// Sync the engine's own tier BEFORE the next engineFor: a mismatch would
	// re-seed a fresh engine and wipe the EWMA history + cooldown state.
	e.Tier = v.EffectiveTier
	reason := d.Reason
	if v.EffectiveTier != d.ToTier {
		reason += fmt.Sprintf(" (capped at tier %d by the governor)", v.EffectiveTier)
	}
	s.recordDynamicEvent(ctx, ws, string(d.Action), d.FromTier, v.EffectiveTier, 0, reason)
	s.publishDynamicEvent(ctx, ws, "website.tier_changed", map[string]any{
		"action": string(d.Action), "from_tier": d.FromTier, "to_tier": v.EffectiveTier,
		"reason": reason, "pressure": d.Pressure, "bottleneck": pressures.MaxName,
	})
	s.enqueueDynamicEnforce(ctx, ws)
	s.publishResourceUpdate(ctx, ws, e, sample)
}

// dynamicFleetAllocatedMB sums the current-tier memory allocation of every
// OTHER dynamic site on the same node — the governor's fleet-wide denominator.
func (s *Server) dynamicFleetAllocatedMB(ctx context.Context, ws *websites.Website, floor int64) int64 {
	sites, err := s.Websites.ListDynamicReady(ctx, 500)
	if err != nil {
		return 0
	}
	var total int64
	for i := range sites {
		w := &sites[i]
		if w.ID == ws.ID || w.ServerID != ws.ServerID {
			continue
		}
		base, err := s.dynamicBasePayload(ctx, w)
		if err != nil {
			continue
		}
		alloc := traffic.Scale(traffic.BaseLimits{
			MemoryMB: base.MemoryMB, CPUPercent: base.CPUPercent,
			PidsMax: base.PidsMax, FpmChildren: base.FpmMaxChildren,
		}, traffic.ClampTier(w.DynamicTier), floor)
		total += alloc.MemoryMB
	}
	return total
}

// nodeMemory returns the node's total/available RAM from the freshest agent
// sample (0,0 when unknown — the governor then skips the capacity checks
// rather than inventing limits).
func (s *Server) nodeMemory(serverID uuid.UUID) (total, avail int64) {
	if s.LiveStore == nil {
		return 0, 0
	}
	lv := s.LiveStore.Snapshot(serverID)
	if lv == nil || lv.Sample == nil {
		return 0, 0
	}
	return int64(lv.Sample.Node.MemoryTotal), int64(lv.Sample.Node.MemoryAvailable)
}

// publishResourceUpdate pushes a throttled live snapshot for the site onto
// the WS bus: on any meaningful pressure/tier change, else as a 30s
// heartbeat. The UI renders live bars from these instead of polling.
func (s *Server) publishResourceUpdate(ctx context.Context, ws *websites.Website, e *dynres.Engine, sample liveSample) {
	now := time.Now()
	s.dyn.mu.Lock()
	// Throttle maps are lazily created: dynamicRuntime starts empty and the
	// first tick of a fresh process lands here (nil-map panic otherwise).
	if s.dyn.lastPush == nil {
		s.dyn.lastPush = map[uuid.UUID]time.Time{}
	}
	if s.dyn.lastPushedMax == nil {
		s.dyn.lastPushedMax = map[uuid.UUID]float64{}
	}
	last := s.dyn.lastPush[ws.ID]
	lastMax := s.dyn.lastPushedMax[ws.ID]
	changed := sample.found && absf(e.Smoothed()-lastMax) >= 0.02
	due := now.Sub(last) >= dynamicPushHeartbeat || (changed && now.Sub(last) >= dynamicPushMinInterval)
	if !due {
		s.dyn.mu.Unlock()
		return
	}
	s.dyn.lastPush[ws.ID] = now
	s.dyn.lastPushedMax[ws.ID] = e.Smoothed()
	s.dyn.mu.Unlock()

	p := e.LastPressures()
	payload := map[string]any{
		"tier": traffic.ClampTier(ws.DynamicTier), "state": ws.DynamicState,
		"pressure": map[string]any{
			"cpu": p.CPU, "memory": p.Memory, "pids": p.Pids, "fpm": p.FPM,
			"max": p.Max, "bottleneck": p.MaxName, "smoothed": e.Smoothed(),
		},
		"usage": map[string]any{
			"cpu_percent": sample.cpuPercent, "memory_mb": sample.memBytes / (1024 * 1024),
			"processes": sample.pids, "fpm_active": sample.fpmActive, "fpm_queue": sample.fpmQueue,
		},
		"limits": map[string]any{
			"cpu_percent": sample.cpuLimitPercent, "memory_mb": sample.memLimit / (1024 * 1024),
			"pids_max": sample.pidsLimit, "fpm_max_children": sample.fpmChildren,
		},
	}
	if trend, ok := s.trafficStore().Trend(ws.ID); ok && trend.LastWindow > 0 {
		windowS := int64(60) // defaultTrafficWindow; refined from the recent ring below
		if recent := s.trafficStore().Recent(ws.ID); len(recent) > 0 && recent[len(recent)-1].WindowS > 0 {
			windowS = int64(recent[len(recent)-1].WindowS)
		}
		payload["requests_per_sec"] = float64(trend.LastWindow) / float64(windowS)
	}
	s.publishDynamicEvent(ctx, ws, "website.resource_update", payload)
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
// FPM numbers from the agent stream, with enforced limits as denominators
// (the anti-drift rule — display source == enforcement source).
func (s *Server) liveSiteSample(ws *websites.Website) liveSample {
	var ls liveSample
	if s.LiveStore == nil {
		return ls
	}
	lv := s.LiveStore.Snapshot(ws.ServerID)
	if lv == nil {
		return ls
	}
	if lv.Sample != nil {
		ls.nodeTotal = int64(lv.Sample.Node.MemoryTotal)
		ls.nodeAvail = int64(lv.Sample.Node.MemoryAvailable)
	}
	site, ok := lv.Sites[ws.UnixUser]
	if !ok {
		// Newer sites may key by website UUID (agent slice-name evolution);
		// try both before giving up.
		site, ok = lv.Sites[ws.ID.String()]
	}
	if !ok {
		return ls
	}
	ls.found = true
	ls.cpuPercent = site.CPUPercent
	ls.cpuLimitPercent = site.CPULimitCores * 100
	ls.memBytes = site.MemoryBytes
	ls.memLimit = site.MemoryLimit
	ls.pids = int64(site.Processes)
	ls.pidsLimit = site.PidsLimit
	ls.fpmActive = site.FpmActive
	ls.fpmChildren = site.FpmMaxChildren
	ls.fpmQueue = site.FpmQueue
	return ls
}

// liveSample carries the engine's inputs plus the node view for the governor.
type liveSample struct {
	found                          bool
	cpuPercent, cpuLimitPercent    float64
	memBytes, memLimit             int64
	pids, pidsLimit                int64
	fpmActive, fpmChildren, fpmQueue int
	nodeTotal, nodeAvail           int64
}

// Observation reduces the sample to engine units. When the agent has no
// live sample (agent down, site idle), all pressures read zero — the
// engine simply sees a very idle site and never scales on missing data.
func (l liveSample) Observation() dynres.Observation {
	return dynres.Observation{
		CPUPercent: l.cpuPercent, CPULimitPercent: l.cpuLimitPercent,
		MemBytes: l.memBytes, MemLimitBytes: l.memLimit,
		Pids: l.pids, PidsLimit: l.pidsLimit,
		FPMActive: l.fpmActive, FPMChildren: l.fpmChildren, FPMQueue: l.fpmQueue,
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
	Pressure        map[string]any             `json:"pressure,omitempty"`
	Usage           map[string]any             `json:"usage,omitempty"`
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
	// Live pressure from the resource engine (absent until the first tick).
	if e := s.dyn.peekEngine(ws.ID); e != nil {
		p := e.LastPressures()
		sample := s.liveSiteSample(ws)
		resp.Pressure = map[string]any{
			"cpu": p.CPU, "memory": p.Memory, "pids": p.Pids, "fpm": p.FPM,
			"max": p.Max, "bottleneck": p.MaxName, "smoothed": e.Smoothed(),
		}
		resp.Usage = map[string]any{
			"cpu_percent": sample.cpuPercent, "memory_mb": sample.memBytes / (1024 * 1024),
			"processes": sample.pids, "fpm_active": sample.fpmActive, "fpm_queue": sample.fpmQueue,
		}
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
		// Resource engine + safety governor knobs.
		"max_tier":                   s.dynMaxTier(ctx),
		"scale_up_cooldown_s":        s.dynUpCooldown(ctx) / time.Second,
		"scale_down_cooldown_s":      s.dynDownCooldown(ctx) / time.Second,
		"global_cap_percent":         s.dynGlobalCapPercent(ctx),
		"max_scaleups_per_minute":    s.dynMaxScaleUpsPerMinute(ctx),
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
		MaxTier            *int   `json:"max_tier"`
		ScaleUpCooldownS   *int   `json:"scale_up_cooldown_s"`
		ScaleDownCooldownS *int   `json:"scale_down_cooldown_s"`
		GlobalCapPercent   *int   `json:"global_cap_percent"`
		MaxScaleUpsPerMin  *int   `json:"max_scaleups_per_minute"`
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
	if req.MaxTier != nil {
		if *req.MaxTier < 1 || *req.MaxTier > traffic.MaxTier {
			httpapi.RespondError(w, httpapi.ErrValidation(fmt.Sprintf("max_tier must be between 1 and %d", traffic.MaxTier)))
			return
		}
		set("dynamic_max_tier", strconv.Itoa(*req.MaxTier))
	}
	if req.ScaleUpCooldownS != nil {
		if *req.ScaleUpCooldownS < 30 || *req.ScaleUpCooldownS > 600 {
			httpapi.RespondError(w, httpapi.ErrValidation("scale_up_cooldown_s must be between 30 and 600"))
			return
		}
		set("dynamic_scale_up_cooldown_s", strconv.Itoa(*req.ScaleUpCooldownS))
	}
	if req.ScaleDownCooldownS != nil {
		if *req.ScaleDownCooldownS < 60 || *req.ScaleDownCooldownS > 3600 {
			httpapi.RespondError(w, httpapi.ErrValidation("scale_down_cooldown_s must be between 60 and 3600"))
			return
		}
		set("dynamic_scale_down_cooldown_s", strconv.Itoa(*req.ScaleDownCooldownS))
	}
	if req.GlobalCapPercent != nil {
		if *req.GlobalCapPercent < 0 || *req.GlobalCapPercent > 100 {
			httpapi.RespondError(w, httpapi.ErrValidation("global_cap_percent must be between 0 and 100"))
			return
		}
		set("dynamic_global_cap_percent", strconv.Itoa(*req.GlobalCapPercent))
	}
	if req.MaxScaleUpsPerMin != nil {
		if *req.MaxScaleUpsPerMin < 0 || *req.MaxScaleUpsPerMin > 1000 {
			httpapi.RespondError(w, httpapi.ErrValidation("max_scaleups_per_minute out of range"))
			return
		}
		set("dynamic_max_scaleups_per_minute", strconv.Itoa(*req.MaxScaleUpsPerMin))
	}
	s.Audit.RecordBestEffort(ctx, audit.Entry{
		ActorType: audit.ActorUser, ActorUserID: actorID,
		Action: "settings.dynamic_resources_updated", ResourceType: "settings",
		Metadata: map[string]any{"request": req},
	})
	s.getAdminDynamic(w, r)
}

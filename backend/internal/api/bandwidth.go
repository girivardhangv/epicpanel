package api

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// ============================================================================
// Bandwidth + quota endpoints (migration 0050 era).
//
// The authoritative monthly quota number is the workload_resource_usage
// high-water row the enforce fanout persists (nft per-uid direct egress +
// access-log egress composition). History reads the bucket tables the
// bandwidthhistory store fills from the agent's completed access-log
// windows. Live rates derive from the in-memory traffic store.
// ============================================================================

// RegisterBandwidthRoutes mounts the bandwidth summary / history and quota
// read/update endpoints. Mirrors RegisterDynamicRoutes' resolution pattern.
func (s *Server) RegisterBandwidthRoutes(mux *http.ServeMux, resolveOrg func(*http.Request, string, organizations.Role) (uuid.UUID, *httpapi.APIError)) {
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

	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/bandwidth",
		siteResolved(organizations.RoleBilling, s.getBandwidth))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/bandwidth/history",
		siteResolved(organizations.RoleBilling, s.getBandwidthHistory))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/quota",
		siteResolved(organizations.RoleBilling, s.getSiteQuota))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/websites/{website_id}/quota",
		siteResolved(organizations.RoleAdmin, s.patchSiteQuota))
}

// bandwidthQuotaBlock is the quota surface shared by the bandwidth summary
// and the quota endpoints. remaining/percentage are null when the limit is
// unlimited (limit_bytes 0).
type bandwidthQuotaBlock struct {
	LimitBytes     int64    `json:"limit_bytes"`
	Period         string   `json:"period"`
	UsedBytes      int64    `json:"used_bytes"`
	RemainingBytes *int64   `json:"remaining_bytes"`
	Percentage     *float64 `json:"percentage"`
	ResetsAt       string   `json:"resets_at"`
	Source         string   `json:"source"` // "site" (per-site override) | "plan"
}

// bandwidthQuotaBlockFor resolves the block from the SAME seams the resume
// guard and the suspension page metadata use (one resolution, no drift).
func (s *Server) bandwidthQuotaBlockFor(ctx context.Context, ws *websites.Website) bandwidthQuotaBlock {
	block := bandwidthQuotaBlock{
		Period:   "monthly",
		ResetsAt: nextMonthlyResetUTC().Format(time.RFC3339),
		Source:   "plan",
	}
	limitMB, unlimited, err := s.effectiveBandwidthLimitMB(ctx, ws)
	if err != nil {
		slog.Warn("bandwidth quota resolution failed", "website", ws.ID, "err", err)
		unlimited = true // fail open for DISPLAY; the enforce path re-derives
	}
	if !unlimited {
		block.LimitBytes = limitMB * 1024 * 1024
		if ws.BandwidthLimitMB != nil {
			block.Source = "site"
		}
	}
	if usedMB, ok, err := s.currentMonthBandwidthUsedMB(ctx, ws.ID); err == nil && ok {
		block.UsedBytes = int64(math.Round(usedMB * 1024 * 1024))
	}
	if !unlimited {
		rem := block.LimitBytes - block.UsedBytes
		if rem < 0 {
			rem = 0
		}
		block.RemainingBytes = &rem
		pct := math.Round(float64(block.UsedBytes)/float64(block.LimitBytes)*10000) / 100
		block.Percentage = &pct
	}
	return block
}

// getBandwidth handles GET .../bandwidth — the site's current bandwidth
// state for the dashboard card: quota surface, period usage and the live
// transfer rate from the in-memory traffic store.
func (s *Server) getBandwidth(w http.ResponseWriter, r *http.Request, ws *websites.Website) {
	block := s.bandwidthQuotaBlockFor(r.Context(), ws)
	// traffic: the period usage is egress-dominated by construction (the
	// access-log view has no per-site ingress; nft contributes direct
	// egress) — rx_bytes is reported honestly as 0 today.
	resp := map[string]any{
		"site_id":   ws.ID,
		"status":    ws.Status,
		"quota":     block,
		"traffic":   map[string]any{"rx_bytes": 0, "tx_bytes": block.UsedBytes, "total_bytes": block.UsedBytes},
		"rate":      map[string]any{"rx_bps": 0, "tx_bps": s.liveEgressBPS(ws.ID), "total_bps": s.liveEgressBPS(ws.ID)},
	}
	if ws.Status == websites.StatusSuspended {
		susp := map[string]any{"reason": "manual"}
		if ws.SuspensionReason != nil {
			susp["reason"] = *ws.SuspensionReason
		}
		if ws.SuspendedAt != nil {
			susp["suspended_at"] = ws.SuspendedAt.Format(time.RFC3339)
		}
		resp["suspension"] = susp
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

// liveEgressBPS computes the site's current egress rate from the most
// recent completed traffic windows (mean over up to 3 windows; 0 when
// idle or the site has no nginx access log).
func (s *Server) liveEgressBPS(siteID uuid.UUID) float64 {
	recent := s.trafficStore().Recent(siteID)
	if len(recent) > 3 {
		recent = recent[len(recent)-3:]
	}
	var bytes int64
	secs := 0
	for _, f := range recent {
		bytes += f.Bytes
		secs += f.WindowS
	}
	if secs <= 0 {
		return 0
	}
	return math.Round(float64(bytes)/float64(secs)*100) / 100
}

// getBandwidthHistory handles GET .../bandwidth/history?from&to&interval —
// bucketed usage for graphs. interval=hour reads the 90d hourly buckets,
// interval=day the indefinite daily rollups. Nothing finer than the stored
// level is ever synthesized.
func (s *Server) getBandwidthHistory(w http.ResponseWriter, r *http.Request, ws *websites.Website) {
	q := r.URL.Query()
	interval := q.Get("interval")
	if interval == "" {
		interval = "hour"
	}
	if interval != "hour" && interval != "day" {
		httpapi.RespondError(w, httpapi.ErrValidationDetails(
			"interval must be hour or day",
			map[string]any{"allowed": []string{"hour", "day"}}))
		return
	}
	to := time.Now().UTC()
	if v := q.Get("to"); v != "" {
		parsed, err := parseHistoryTime(v, false)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid to time (RFC3339 or YYYY-MM-DD)"))
			return
		}
		to = parsed
	}
	from := to.Add(-24 * time.Hour)
	if v := q.Get("from"); v != "" {
		parsed, err := parseHistoryTime(v, true)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid from time (RFC3339 or YYYY-MM-DD)"))
			return
		}
		from = parsed
	}
	if !from.Before(to) {
		httpapi.RespondError(w, httpapi.ErrValidation("from must be before to"))
		return
	}
	if to.Sub(from) > 400*24*time.Hour {
		httpapi.RespondError(w, httpapi.ErrValidation("range must be <= 400 days"))
		return
	}
	var (
		query string
		args  []any
	)
	if interval == "day" {
		// DATE column: compute the inclusive UTC day bounds in Go so the
		// comparison never depends on the session timezone (to is an
		// exclusive end; a bare `to` date covers that whole day).
		dayFrom := from.UTC().Format("2006-01-02")
		dayTo := to.Add(-time.Second).UTC().Format("2006-01-02")
		query = `SELECT day, rx_bytes, tx_bytes, requests FROM website_bandwidth_daily
		 WHERE website_id = $1 AND day >= $2::date AND day <= $3::date
		 ORDER BY day`
		args = []any{ws.ID, dayFrom, dayTo}
	} else {
		query = `SELECT hour_bucket, rx_bytes, tx_bytes, requests FROM website_bandwidth_samples
		 WHERE website_id = $1 AND hour_bucket >= $2 AND hour_bucket < $3
		 ORDER BY hour_bucket`
		args = []any{ws.ID, from, to}
	}
	rows, err := s.Pool.Query(r.Context(), query, args...)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	defer rows.Close()
	data := []map[string]any{}
	for rows.Next() {
		var bucket time.Time
		var rx, tx, reqs int64
		if err := rows.Scan(&bucket, &rx, &tx, &reqs); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		data = append(data, map[string]any{
			"timestamp":   bucket.Format(time.RFC3339),
			"rx_bytes":    rx,
			"tx_bytes":    tx,
			"total_bytes": rx + tx,
			"requests":    reqs,
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"site_id":  ws.ID,
		"from":     from.Format(time.RFC3339),
		"to":       to.Format(time.RFC3339),
		"interval": interval,
		"data":     data,
	})
}

// parseHistoryTime accepts RFC3339 timestamps and bare dates. A bare from
// date is that day's UTC midnight; a bare to date is the EXCLUSIVE end of
// that day so `to=2026-09-30` covers the whole of Sep 30.
func parseHistoryTime(v string, isFrom bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		if isFrom {
			return t.UTC(), nil
		}
		return t.UTC().Add(24 * time.Hour), nil
	}
	return time.Time{}, strconv.ErrSyntax
}

// getSiteQuota handles GET .../quota — the configured monthly bandwidth
// budget with its current usage surface.
func (s *Server) getSiteQuota(w http.ResponseWriter, r *http.Request, ws *websites.Website) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"site_id":   ws.ID,
		"bandwidth": s.bandwidthQuotaBlockFor(r.Context(), ws),
	})
}

// patchSiteQuota handles PATCH .../quota — sets or clears the per-site
// monthly bandwidth override: {"bandwidth_limit_mb": <int>} (0 = unlimited,
// null = follow the hosting plan). Admin-gated; audited; the node converges
// via the shared enforce payload (which now carries the override).
func (s *Server) patchSiteQuota(w http.ResponseWriter, r *http.Request, ws *websites.Website) {
	var req struct {
		BandwidthLimitMB *int64 `json:"bandwidth_limit_mb"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if req.BandwidthLimitMB != nil && *req.BandwidthLimitMB < 0 {
		httpapi.RespondError(w, httpapi.ErrValidationDetails(
			"bandwidth_limit_mb must be >= 0 (0 = unlimited, null = plan)",
			map[string]any{"field": "bandwidth_limit_mb"}))
		return
	}
	if err := s.Websites.SetBandwidthLimitMB(r.Context(), ws.ID, req.BandwidthLimitMB); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	meta := map[string]any{"bandwidth_limit_mb": req.BandwidthLimitMB}
	if user, ok := httpapi.UserFrom(r.Context()); ok {
		if uid, err := uuid.Parse(user.ID); err == nil {
			meta["actor"] = uid
		}
	}
	if s.Audit != nil {
		org := ws.Organization
		s.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &org,
			ActorType:      audit.ActorUser,
			Action:         "website.quota_updated",
			ResourceType:   "website",
			ResourceID:     ws.ID.String(),
			Metadata:       meta,
		})
	}
	// Converge the node budget to the new effective limit (shared payload
	// builder injects the override; idempotent job).
	s.enqueueDynamicEnforce(r.Context(), ws)
	updated, err := s.Websites.GetByID(r.Context(), ws.Organization, ws.ID)
	if err != nil {
		updated = ws
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"site_id":   ws.ID,
		"bandwidth": s.bandwidthQuotaBlockFor(r.Context(), updated),
	})
}

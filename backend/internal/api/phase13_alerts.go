package api

// Phase 13 — alerts API + panel self-telemetry. Coordinator wires
// registerPhase13 into server.go. Alert evaluation is async (alerts.Engine
// sweeper + bus subscriptions), never in request handlers.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/alerts"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/metrics"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
)

// requestCounters is the process-local Prometheus counter set (no client lib
// — hand-rolled text exposition per wave contract).
var requestCounters struct {
	total   map[string]*atomic.Int64 // route class -> count
	latency map[string]*atomic.Int64 // route class -> cumulative ms
	wsConns atomic.Int64
	ready   atomic.Bool
}

func initRequestCounters() {
	if requestCounters.ready.Load() {
		return
	}
	requestCounters.total = map[string]*atomic.Int64{
		"auth": {}, "org": {}, "admin": {}, "agent": {}, "public": {}, "other": {},
	}
	requestCounters.latency = map[string]*atomic.Int64{
		"auth": {}, "org": {}, "admin": {}, "agent": {}, "public": {}, "other": {},
	}
	requestCounters.ready.Store(true)
}

// ObserveRequest records one request for /metrics (called by the coordinator
// from the main middleware chain when wired).
func ObserveRequest(routeClass string, ms int64) {
	if !requestCounters.ready.Load() {
		initRequestCounters()
	}
	if c, ok := requestCounters.total[routeClass]; ok {
		c.Add(1)
		requestCounters.latency[routeClass].Add(ms)
	} else {
		requestCounters.total["other"].Add(1)
		requestCounters.latency["other"].Add(ms)
	}
}

// registerPhase13 mounts the alerts API + /metrics.
func registerPhase13(s *Server, mux *http.ServeMux) {
	initRequestCounters()
	st := &alerts.Store{Pool: s.Pool}
	engine := alerts.NewEngine(st, s.Events)
	h := &alertsHandler{srv: s, st: st, engine: engine}

	// Admin alert surface (WHM).
	mux.HandleFunc("GET /v1/admin/alerts", h.requireAdmin(h.listAlerts))
	mux.HandleFunc("POST /v1/admin/alerts/{alert_id}/ack", h.requireAdmin(h.ackAlert))
	mux.HandleFunc("POST /v1/admin/alerts/{alert_id}/resolve", h.requireAdmin(h.resolveAlert))
	mux.HandleFunc("GET /v1/admin/alert-rules", h.requireAdmin(h.listRules))
	mux.HandleFunc("POST /v1/admin/alert-rules", h.requireAdmin(h.createRule))
	mux.HandleFunc("PATCH /v1/admin/alert-rules/{rule_id}", h.requireAdmin(h.updateRule))
	mux.HandleFunc("DELETE /v1/admin/alert-rules/{rule_id}", h.requireAdmin(h.deleteRule))

	// Observability drill-downs (verbatim overview tree).
	mux.HandleFunc("GET /v1/admin/observability/nodes", h.requireAdmin(h.obsNodes))
	mux.HandleFunc("GET /v1/admin/observability/services", h.requireAdmin(h.obsServices))
	mux.HandleFunc("GET /v1/admin/observability/customers", h.requireAdmin(h.obsCustomers))
	mux.HandleFunc("GET /v1/admin/observability/workloads", h.requireAdmin(h.obsWorkloads))

	// Panel self-telemetry (Prometheus text format).
	mux.HandleFunc("GET /metrics", h.metrics)

	// Async evaluation: periodic sweeper (SSL windows) — thresholds/states
	// arrive via the coordinator's live-sample hook (EvaluateNode) which is
	// also async. Nothing here blocks request handling.
	engine.StartSweeper(s.shutdownCtx(), 5*time.Minute)
}

type alertsHandler struct {
	srv    *Server
	st     *alerts.Store
	engine *alerts.Engine
}

func (h *alertsHandler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := httpapi.UserFrom(r.Context())
		if !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if user.Role != "admin" || httpapi.IsAPIToken(r.Context()) {
			httpapi.RespondError(w, httpapi.ErrForbidden("platform admin session required"))
			return
		}
		next(w, r)
	}
}

func (h *alertsHandler) listAlerts(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.st.ListAlerts(r.Context(), state, limit)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"alerts": list})
}

func (h *alertsHandler) ackAlert(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("alert_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid alert id"))
		return
	}
	user, _ := httpapi.UserFrom(r.Context())
	uid, _ := uuid.Parse(user.ID)
	if err := h.st.Acknowledge(r.Context(), id, uid); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, "alert.ack", id.String())
	w.WriteHeader(http.StatusNoContent)
}

func (h *alertsHandler) resolveAlert(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("alert_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid alert id"))
		return
	}
	user, _ := httpapi.UserFrom(r.Context())
	uid, _ := uuid.Parse(user.ID)
	if err := h.st.Resolve(r.Context(), id, uid); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, "alert.resolve", id.String())
	w.WriteHeader(http.StatusNoContent)
}

func (h *alertsHandler) listRules(w http.ResponseWriter, r *http.Request) {
	list, err := h.st.ListRules(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"rules": list})
}

type createRuleReq struct {
	Name            string  `json:"name"`
	Description     string  `json:"description"`
	RuleClass       string  `json:"rule_class"`
	Metric          string  `json:"metric"`
	Scope           string  `json:"scope"`
	ScopeID         string  `json:"scope_id"`
	Comparison      string  `json:"comparison"`
	Threshold       float64 `json:"threshold"`
	DurationSeconds int     `json:"duration_seconds"`
	RecoveryMargin  float64 `json:"recovery_margin"`
	WindowDays      int     `json:"window_days"`
	Severity        string  `json:"severity"`
}

func (h *alertsHandler) createRule(w http.ResponseWriter, r *http.Request) {
	var req createRuleReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid body"))
		return
	}
	if !validRuleClass(req.RuleClass) || !validMetric(req.RuleClass, req.Metric) {
		httpapi.RespondError(w, httpapi.ErrValidation("unknown rule class/metric"))
		return
	}
	if req.Severity == "" {
		req.Severity = "warning"
	}
	if req.Severity != "info" && req.Severity != "warning" && req.Severity != "critical" {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid severity"))
		return
	}
	rl := &alerts.Rule{
		Name: req.Name, Description: req.Description, Class: req.RuleClass,
		Metric: req.Metric, Scope: req.Scope, ScopeID: req.ScopeID,
		Comparison: req.Comparison, Threshold: req.Threshold,
		DurationSeconds: req.DurationSeconds, RecoveryMargin: req.RecoveryMargin,
		WindowDays: req.WindowDays, Severity: req.Severity, Enabled: true,
	}
	if req.Comparison == "" {
		rl.Comparison = "gt"
	}
	out, err := h.st.CreateRule(r.Context(), rl)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, "alert_rule.create", out.ID.String())
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

func (h *alertsHandler) updateRule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("rule_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid rule id"))
		return
	}
	var req createRuleReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid body"))
		return
	}
	rl := &alerts.Rule{
		Description: req.Description, Comparison: req.Comparison, Threshold: req.Threshold,
		DurationSeconds: req.DurationSeconds, RecoveryMargin: req.RecoveryMargin,
		WindowDays: req.WindowDays, Severity: req.Severity, Enabled: true,
	}
	out, err := h.st.UpdateRule(r.Context(), id, rl)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, "alert_rule.update", id.String())
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (h *alertsHandler) deleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("rule_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid rule id"))
		return
	}
	if err := h.st.DeleteRule(r.Context(), id); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, "alert_rule.delete", id.String())
	w.WriteHeader(http.StatusNoContent)
}

func validRuleClass(c string) bool {
	return c == "threshold" || c == "state" || c == "time"
}

// validMetric: verbatim alert list + overview-tree workload metrics.
func validMetric(class, metric string) bool {
	switch class {
	case "threshold":
		switch metric {
		case "cpu", "ram", "disk", "mc_tps", "mc_mspt", "mc_players", "discord_cpu", "discord_ram", "discord_uptime":
			return true
		}
	case "state":
		switch metric {
		case "node_offline", "service_down", "container_crashed", "backup_failed", "provisioning_failed":
			return true
		}
	case "time":
		return metric == "ssl_expiration"
	}
	return false
}

// ---------------------------------------------------------------------------
// Observability drill-downs (verbatim tree; data from LiveStore only —
// the live path never reads the historical DB, Phase 3 rule).
// ---------------------------------------------------------------------------

func (h *alertsHandler) obsNodes(w http.ResponseWriter, r *http.Request) {
	type nodeRow struct {
		ServerID   string  `json:"server_id"`
		Name       string  `json:"name"`
		State      string  `json:"state"`
		CPUPct     float64 `json:"cpu_percent"`
		RAMPct     float64 `json:"ram_percent"`
		DiskPct    float64 `json:"disk_percent"`
		NetRxBPS   float64 `json:"net_rx_bps"`
		NetTxBPS   float64 `json:"net_tx_bps"`
		AgeSeconds float64 `json:"age_seconds"`
	}
	now := time.Now()
	out := []nodeRow{}
	for _, sv := range h.listServers(r) {
		row := nodeRow{ServerID: sv.ID.String(), Name: sv.Name, State: metrics.NodeOffline}
		if h.srv.LiveStore != nil {
			if lv := h.srv.LiveStore.Snapshot(sv.ID); lv != nil {
				row.State = lv.NodeState(now)
				row.AgeSeconds = now.Sub(lv.LastFrame).Seconds()
				if s := lv.Sample; s != nil {
					row.CPUPct = s.Node.CPUPercent

					if s.Node.MemoryTotal > 0 {
						row.RAMPct = 100 * float64(s.Node.MemoryUsed) / float64(s.Node.MemoryTotal)
					}
				}
			}
		}
		out = append(out, row)
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

func (h *alertsHandler) obsServices(w http.ResponseWriter, r *http.Request) {
	type svcRow struct {
		ServerID string `json:"server_id"`
		Service  string `json:"service"`
		Up       bool   `json:"up"`
		State    string `json:"node_state"`
	}
	out := []svcRow{}
	svcNames := []string{"nginx", "apache", "ols", "php-fpm", "mariadb", "docker"}
	now := time.Now()
	for _, sv := range h.listServers(r) {
		state := metrics.NodeOffline
		var services map[string]bool
		if h.srv.LiveStore != nil {
			if lv := h.srv.LiveStore.Snapshot(sv.ID); lv != nil {
				state = lv.NodeState(now)
				if s := lv.Sample; s != nil {
					services = map[string]bool{}
					for _, svc := range s.Node.Services {
						services[svc.Name] = svc.State == "active"
					}
				}
			}
		}
		for _, svc := range svcNames {
			out = append(out, svcRow{ServerID: sv.ID.String(), Service: svc, Up: services[svc], State: state})
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"services": out})
}

func (h *alertsHandler) obsCustomers(w http.ResponseWriter, r *http.Request) {
	type custRow struct {
		WebsiteID string  `json:"website_id"`
		ServerID  string  `json:"server_id"`
		State     string  `json:"state"`
		CPUPct    float64 `json:"cpu_percent"`
		RAMPct    float64 `json:"ram_percent"`
		DiskUsed  int64   `json:"disk_used_mb"`
		Bandwidth float64 `json:"bandwidth_bps"`
	}
	byServer := map[string][]custRow{}
	if h.srv.LiveStore != nil {
		for _, f := range h.srv.LiveStore.Frames() {
			state := f.NodeState
			for _, site := range f.Sample.Sites {
				row := custRow{WebsiteID: site.WebsiteID, ServerID: f.ServerID, State: state,
					CPUPct: site.CPUPercent, DiskUsed: site.DiskUsedMB, Bandwidth: site.BandwidthBPS}
				if site.MemoryLimit > 0 {
					row.RAMPct = 100 * float64(site.MemoryBytes) / float64(site.MemoryLimit)
				}
				byServer[f.ServerID] = append(byServer[f.ServerID], row)
			}
		}
	}
	out := []custRow{}
	for _, rows := range byServer {
		out = append(out, rows...)
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"customers": out})
}

func (h *alertsHandler) obsWorkloads(w http.ResponseWriter, r *http.Request) {
	type wlRow struct {
		Ref      string  `json:"ref"`
		Kind     string  `json:"kind"`
		State    string  `json:"state"`
		TPS      float64 `json:"tps"`
		MSPT     float64 `json:"mspt"`
		Players  int     `json:"players"`
		CPUPct   float64 `json:"cpu_percent"`
		RAMPct   float64 `json:"ram_percent"`
		UptimeS  int64   `json:"uptime_s"`
		Restarts int     `json:"restarts"`
	}
	out := []wlRow{}
	if h.srv.LiveStore != nil {
		for _, f := range h.srv.LiveStore.Frames() {
			state := f.NodeState
			for _, a := range f.Apps {
				out = append(out, wlRow{
					Ref: a.Kind + ":" + a.WebsiteID, Kind: a.Kind, State: state,
					TPS: a.TPS, MSPT: a.MSPT, Players: a.Players,
					CPUPct: a.CPUPercent, RAMPct: 0,
					UptimeS: a.UptimeS, Restarts: a.RestartCount,
				})
			}
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"workloads": out})
}

func pct(used, limit int64) float64 {
	if limit <= 0 {
		return 0
	}
	return 100 * float64(used) / float64(limit)
}

// ---------------------------------------------------------------------------
// /metrics — dependency-free Prometheus text exposition.
// ---------------------------------------------------------------------------

func (h *alertsHandler) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	var b []byte
	append := func(s string) { b = append(b, s...) }

	append("# HELP epicpanel_http_requests_total HTTP requests by route class.\n")
	append("# TYPE epicpanel_http_requests_total counter\n")
	for _, class := range sortedKeys(requestCounters.total) {
		append(fmt.Sprintf("epicpanel_http_requests_total{class=%q} %d\n", class, requestCounters.total[class].Load()))
	}
	append("# HELP epicpanel_http_latency_ms_total Cumulative request latency (ms) by route class.\n")
	append("# TYPE epicpanel_http_latency_ms_total counter\n")
	for _, class := range sortedKeys(requestCounters.latency) {
		append(fmt.Sprintf("epicpanel_http_latency_ms_total{class=%q} %d\n", class, requestCounters.latency[class].Load()))
	}
	append("# HELP epicpanel_ws_connections Current WebSocket connections.\n")
	append("# TYPE epicpanel_ws_connections gauge\n")
	append(fmt.Sprintf("epicpanel_ws_connections %d\n", requestCounters.wsConns.Load()))
	append("# HELP epicpanel_jobs_pending Pending jobs in the queue.\n")
	append("# TYPE epicpanel_jobs_pending gauge\n")
	append(fmt.Sprintf("epicpanel_jobs_pending %d\n", h.pendingJobs(r)))
	w.Write(b)
}

func (h *alertsHandler) pendingJobs(r *http.Request) int {
	var n int
	_ = h.srv.Pool.QueryRow(r.Context(), `SELECT COUNT(*) FROM jobs WHERE status='pending'`).Scan(&n)
	return n
}

func sortedKeys(m map[string]*atomic.Int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// plumbing
// ---------------------------------------------------------------------------

type serverLite struct {
	ID   uuid.UUID
	Name string
}

func (h *alertsHandler) listServers(r *http.Request) []serverLite {
	list, err := (&servers.Store{Pool: h.srv.Pool}).ListAll(r.Context())
	if err != nil {
		return nil
	}
	out := make([]serverLite, 0, len(list))
	for _, sv := range list {
		out = append(out, serverLite{ID: sv.ID, Name: sv.Name})
	}
	return out
}

type websiteLite struct {
	ID   uuid.UUID
	Name string
}

func (h *alertsHandler) listWebsites(r *http.Request) []websiteLite {
	rows, err := h.srv.Pool.Query(r.Context(), `SELECT id, name FROM websites LIMIT 5000`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []websiteLite
	for rows.Next() {
		var ws websiteLite
		if err := rows.Scan(&ws.ID, &ws.Name); err != nil {
			continue
		}
		out = append(out, ws)
	}
	return out
}

func (h *alertsHandler) audit(r *http.Request, action, resourceID string) {
	user, ok := httpapi.UserFrom(r.Context())
	if !ok || h.srv.Audit == nil {
		return
	}
	uid, _ := uuid.Parse(user.ID)
	orgRef := uuid.Nil
	h.srv.Audit.RecordBestEffort(r.Context(), audit.Entry{
		ActorUserID: &uid, ActorType: audit.ActorUser, Action: action,
		ResourceType: "alert", ResourceID: resourceID,
	})
	_ = orgRef
}

// shutdownCtx returns the lifecycle context for async loops. The Server does
// not carry one; the sweeper is a detached daemon (stopped on process exit).
func (s *Server) shutdownCtx() context.Context {
	return context.Background()
}

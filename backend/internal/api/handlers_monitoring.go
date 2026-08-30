package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/monitoring"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/go-chi/chi/v5"
)

// handleInstallNginx triggers an explicit operator-requested Nginx install
// on the agent. Never automatic — the user asks for it (§14).
func (s *Server) handleInstallNginx(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := s.deps.Servers.OpsTarget(r.Context(), id)
	if err != nil {
		Error(w, r, err)
		return
	}
	if !target.Manageable {
		Error(w, r, apierror.New(409, "SERVER_NOT_MANAGEABLE",
			"server has no management channel; re-enroll the agent"))
		return
	}
	if err := s.deps.Agent.InstallNginx(r.Context(), target.AgentURL, target.OpsToken); err != nil {
		Error(w, r, err)
		return
	}
	s.auditWithIdentity(r, "servers.install_nginx", "server", id, nil)
	JSON(w, r, http.StatusOK, map[string]any{"installed": true})
}

// handleInstallPHP triggers an explicit operator-requested PHP install.
func (s *Server) handleInstallPHP(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Version string `json:"version"`
	}
	if r.ContentLength > 0 {
		if err := Decode(r, &req); err != nil {
			Error(w, r, err)
			return
		}
	}
	target, err := s.deps.Servers.OpsTarget(r.Context(), id)
	if err != nil {
		Error(w, r, err)
		return
	}
	if !target.Manageable {
		Error(w, r, apierror.New(409, "SERVER_NOT_MANAGEABLE",
			"server has no management channel; re-enroll the agent"))
		return
	}
	if err := s.deps.Agent.InstallPHP(r.Context(), target.AgentURL, target.OpsToken, req.Version); err != nil {
		Error(w, r, err)
		return
	}
	s.auditWithIdentity(r, "servers.install_php", "server", id, map[string]any{"version": req.Version})
	JSON(w, r, http.StatusOK, map[string]any{"installed": true})
}

func (s *Server) offlineThresholdSeconds(r *http.Request) int {
	return s.deps.Settings.Int(r.Context(), settings.KeyServerOfflineMinutes, 5, 1, 10080) * 60
}

// handleTelemetryIngest accepts agent telemetry batches. Authentication is
// the standard agent bearer-token middleware — no second auth system.
func (s *Server) handleTelemetryIngest(w http.ResponseWriter, r *http.Request) {
	token := agentTokenFromContext(r)
	if token == "" {
		unauthorizedAgent(w)
		return
	}
	var batch monitoring.TelemetryBatch
	if err := Decode(r, &batch); err != nil {
		Error(w, r, err)
		return
	}
	// Resolve the server id from the token (same store used by heartbeats).
	serverID, err := s.deps.Servers.IDForAgentToken(r.Context(), token)
	if err != nil {
		Error(w, r, err)
		return
	}
	res, err := s.deps.MonitoringIngest.Ingest(r.Context(), serverID, batch)
	if err != nil {
		Error(w, r, err)
		return
	}
	s.log.Debug("telemetry ingested",
		"server", serverID,
		"accepted", res.Accepted, "duplicates", res.Duplicates, "rejected", res.Rejected,
		"samples", len(batch.Samples))
	s.deps.InternalMetrics.IncCounter("telemetry_samples", "outcome", "accepted")
	JSON(w, r, http.StatusAccepted, res)
}

func (s *Server) handleMetricsCurrent(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.MonitoringQuery.Current(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, view)
}

func (s *Server) handleMetricsHistory(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.MonitoringQuery.History(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("range"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, view)
}

func (s *Server) handleMetricsNetwork(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.MonitoringQuery.NetworkSeries(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("range"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, view)
}

func (s *Server) handleMetricsDisk(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.MonitoringQuery.DiskSeries(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("range"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, view)
}

func (s *Server) handleMetricsServices(w http.ResponseWriter, r *http.Request) {
	view, err := s.deps.MonitoringQuery.LatestServices(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, view)
}

func (s *Server) handleMetricsProcesses(w http.ResponseWriter, r *http.Request) {
	// Granular check inside the endpoint; route already required
	// monitoring.server.view. Denials are audited.
	if !s.identityHasPermission(r, "monitoring.processes.view") {
		Error(w, r, apierror.Forbidden)
		return
	}
	view, err := s.deps.MonitoringQuery.LatestProcesses(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, view)
}

// handleFleetOverview powers the global dashboard's per-server table.
func (s *Server) handleFleetOverview(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.MonitoringQuery.Fleet(r.Context(), s.offlineThresholdSeconds(r)/60)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"servers": out})
}

// ---------------------------------------------------------------------------
// Alerts
// ---------------------------------------------------------------------------

func (s *Server) handleAlertsList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Alerts.List(r.Context(),
		r.URL.Query().Get("status"), r.URL.Query().Get("server_id"), 200)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"alerts": out})
}

func (s *Server) handleAlertsRulesList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Alerts.ListRules(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"rules": out})
}

type patchAlertRuleRequest struct {
	Threshold       *float64 `json:"threshold"`
	DurationSeconds *int     `json:"duration_seconds"`
	Enabled         *bool    `json:"enabled"`
}

func (s *Server) handleAlertsRuleUpdate(w http.ResponseWriter, r *http.Request) {
	var req patchAlertRuleRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	rule, err := s.deps.Alerts.UpdateRule(r.Context(), chi.URLParam(r, "id"),
		req.Threshold, req.DurationSeconds, req.Enabled)
	if err != nil {
		Error(w, r, err)
		return
	}
	s.auditWithIdentity(r, "alerts.rule_updated", "alert_rule", rule.ID, map[string]any{
		"name": rule.Name, "enabled": rule.Enabled,
	})
	JSON(w, r, http.StatusOK, rule)
}

func (s *Server) handleAlertsAcknowledge(w http.ResponseWriter, r *http.Request) {
	idt := auth.IdentityFrom(r.Context())
	actorID, actorLabel := "", "unknown"
	if idt != nil {
		actorID, actorLabel = idt.UserID, idt.Username
	}
	if err := s.deps.Alerts.Acknowledge(r.Context(), chi.URLParam(r, "id"), actorID, actorLabel); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"acknowledged": true})
}

// handleInternalMetrics exposes panel-internal metrics to operators only
// (foundation for §42; not part of the customer dashboard).
func (s *Server) handleInternalMetrics(w http.ResponseWriter, r *http.Request) {
	JSON(w, r, http.StatusOK, s.deps.InternalMetrics.Snapshot())
}
func unauthorizedAgent(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":{"code":"AGENT_AUTH_REQUIRED","message":"Agent authorization required"}}`))
}

package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
)

func (s *Server) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	offlineMin := s.deps.Settings.Int(r.Context(), settings.KeyServerOfflineMinutes, 5, 1, 10080)
	sum, err := s.deps.Servers.DashboardSummary(r.Context(), offlineMin)
	if err != nil {
		Error(w, r, err)
		return
	}
	licInfo, lerr := s.deps.Licensing.Status(r.Context())
	license := map[string]any{"status": "inactive"}
	if lerr == nil && licInfo != nil {
		license = map[string]any{
			"status": licInfo.Status,
			"plan":   licInfo.Plan,
			"expires_at": licInfo.ExpiresAt,
		}
	}

	// Recent security-relevant events straight from the audit trail.
	rows, aerr := s.deps.Audit.Recent(r.Context(), 10,
		[]string{
			"auth.login.success", "auth.login.failure", "auth.logout",
			"auth.password.changed", "servers.registered", "servers.enroll_rejected",
			"license.activated", "installer.completed",
		})
	if aerr != nil {
		Error(w, r, aerr)
		return
	}
	eventsList := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		eventsList = append(eventsList, map[string]any{
			"id": e.ID, "actor_type": e.ActorType, "actor_label": e.ActorLabel,
			"action": e.Action, "resource": e.Resource, "resource_id": e.ResourceID,
			"ip": e.IPAddress, "created_at": e.CreatedAt,
		})
	}

	JSON(w, r, http.StatusOK, map[string]any{
		"servers_total":   sum.ServersTotal,
		"servers_online":  sum.ServersOnline,
		"users_count":     sum.UsersCount,
		"sessions_active": sum.SessionsActive,
		"license":         license,
		"recent_events":   eventsList,
	})
}

func (s *Server) handleLicenseStatus(w http.ResponseWriter, r *http.Request) {
	info, err := s.deps.Licensing.Status(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, info)
}

func (s *Server) handleLicenseRefresh(w http.ResponseWriter, r *http.Request) {
	info, err := s.deps.Licensing.Refresh(r.Context())
	if err != nil {
		if info != nil {
			JSON(w, r, http.StatusOK, map[string]any{"license": info, "error_message": err.Error()})
			return
		}
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"license": info})
}

func (s *Server) handleLicenseDeactivate(w http.ResponseWriter, r *http.Request) {
	id := auth.IdentityFrom(r.Context())
	var actorLabel string
	if id != nil {
		actorLabel = id.Username
	}
	if err := s.deps.Licensing.Deactivate(r.Context()); err != nil {
		Error(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), audit.Entry{
		ActorType: "user", Label: actorLabel, Action: audit.ActionLicenseDeactivated,
		Resource: "license", ResourceID: "",
	})
	JSON(w, r, http.StatusOK, map[string]any{"deactivated": true})
}

func (s *Server) handleRolesList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.RBAC.ListRoles(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"roles": out})
}

func (s *Server) handlePermissionsList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.RBAC.ListPermissions(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"permissions": out})
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	JSON(w, r, http.StatusOK, map[string]any{
		"product":     "EpicPanel",
		"version":     s.deps.Version,
		"environment": s.cfg.Server.Environment,
	})
}

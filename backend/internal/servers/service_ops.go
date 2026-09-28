package servers

import (
	"net/http"
	"regexp"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// serviceUnitRe is the STRICT allowlist of systemd units a service restart
// may target (WHM "Restart Services"). Everything else is refused — the
// payload must never become an arbitrary systemctl argument.
var serviceUnitRe = regexp.MustCompile(`^(nginx|apache2|lsws|openlitespeed|php[0-9]+\.[0-9]+-fpm|mysql|mariadb|postgresql)$`)

// RegisterServiceOps mounts the WHM-style service operations (platform-admin
// only: restarting a shared service affects every site on the node).
func (h *Handler) RegisterServiceOps(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/organizations/{org_id}/servers/{server_id}/services/{service}/restart", h.requirePlatformAdmin(h.RestartService))
}

// POST .../servers/{server_id}/services/{service}/restart
// Enqueues a restart_service agent job for an allowlisted unit. The agent
// runs `systemctl restart <unit>` and verifies is-active afterwards.
func (h *Handler) RestartService(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srv, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	service := r.PathValue("service")
	if !serviceUnitRe.MatchString(service) {
		httpapi.RespondError(w, httpapi.ErrValidation(
			"unknown service (allowed: nginx, apache2, lsws, openlitespeed, php<version>-fpm, mysql, mariadb, postgresql)"))
		return
	}
	job, err := h.Jobs.Enqueue(r.Context(), srv.ID, nil, jobs.TypeRestartService, map[string]string{"service": service})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "server.service_restart_requested", "server", srv.ID.String(), map[string]any{"service": service})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "service": service})
}

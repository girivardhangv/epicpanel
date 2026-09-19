package websites

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// laravelResult mirrors the agent's LaravelOutcome (subset).
type laravelResult struct {
	DocumentRoot string `json:"document_root"`
	Version      string `json:"version"`
}

// InstallLaravel enqueues the one-click Laravel flow for a ready PHP site:
// the agent runs `composer create-project laravel/laravel` under the site
// user with the site's SELECTED PHP version, then serving re-points to
// <site>/app/public and the vhost reconciles automatically.
func (h *Handler) InstallLaravel(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if ws.Status != StatusReady {
		httpapi.RespondError(w, httpapi.ErrConflict("website must be ready before installing Laravel"))
		return
	}
	if ws.Runtime != RuntimePHP {
		httpapi.RespondError(w, httpapi.ErrValidation("Laravel requires a PHP site"))
		return
	}
	if strings.TrimSpace(ws.RuntimeVersion) == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("website has no PHP version selected"))
		return
	}

	job, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeInstallLaravel, map[string]any{
		"website_id":      ws.ID.String(),
		"unix_user":       ws.UnixUser,
		"runtime_version": ws.RuntimeVersion,
		"site_url":        "http://" + ws.PrimaryDomain,
	})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	if h.Audit != nil {
		usr, _ := httpapi.UserFrom(r.Context())
		var actorID *uuid.UUID
		if usr != nil {
			if uid, err := uuid.Parse(usr.ID); err == nil {
				actorID = &uid
			}
		}
		h.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &orgID,
			ActorUserID:    actorID,
			ActorType:      audit.ActorUser,
			Action:         "laravel.install_requested",
			ResourceType:   "website",
			ResourceID:     ws.ID.String(),
			Metadata:       map[string]any{"php": ws.RuntimeVersion},
		})
	}

	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{
		"job_id": job.ID,
		"php":    ws.RuntimeVersion,
		"note":   "composer create-project is running with PHP " + ws.RuntimeVersion + "; serving moves to app/public when it finishes",
	})
}

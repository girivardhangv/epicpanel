package websites

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// GetPHPSettings returns the site's php.ini overrides plus the editable
// directive catalog the editor UI renders.
func (h *Handler) GetPHPSettings(w http.ResponseWriter, r *http.Request) {
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
	if ws.Runtime != RuntimePHP {
		httpapi.RespondError(w, httpapi.ErrValidation("PHP settings are only available for PHP websites"))
		return
	}
	settings := map[string]string{}
	if h.PHPSettings != nil {
		s, err := h.PHPSettings.Get(r.Context(), ws.ID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		settings = s
	}
	httpapi.WriteJSON(w, http.StatusOK, PHPSettingsView{
		WebsiteID: ws.ID,
		Settings:  settings,
		Catalog:   PHPSettingsCatalog(),
	})
}

// SetPHPSettings validates and persists php.ini overrides, then re-enqueues the
// provision job so the agent rewrites the FPM pool with the new directives.
func (h *Handler) SetPHPSettings(w http.ResponseWriter, r *http.Request) {
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
	if ws.Runtime != RuntimePHP {
		httpapi.RespondError(w, httpapi.ErrValidation("PHP settings are only available for PHP websites"))
		return
	}
	var req struct {
		Settings map[string]string `json:"settings"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if h.PHPSettings == nil {
		httpapi.RespondError(w, httpapi.ErrInternal(errStr("php settings store unavailable")))
		return
	}
	norm, err := h.PHPSettings.Set(r.Context(), ws.ID, req.Settings)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	h.auditUser(r, &orgID, "website.php_settings_updated", "website", ws.ID.String(), nil)

	// Reconcile the FPM pool so the new INI values go live (ready/failed only).
	if ws.Status == StatusReady || ws.Status == StatusFailed {
		payload, apiErr := h.buildDesiredPayload(r.Context(), ws, orgID, ws.UnixUser, ws.RuntimeVersion)
		if apiErr == nil {
			_, _ = h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeProvisionWebsite, payload)
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, PHPSettingsView{
		WebsiteID: ws.ID,
		Settings:  norm,
		Catalog:   PHPSettingsCatalog(),
	})
}
package domains

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// redirectsHandler owns the redirect endpoints; it reuses the domains
// handler's org gate, audit and reconcile callback. Registered by
// Handler.Register (domains/handler.go).
type redirectsHandler struct {
	h *Handler
}

func (rh *redirectsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/redirects", rh.h.requireOrg(organizations.RoleBilling, rh.List))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/redirects", rh.h.requireOrg(organizations.RoleDeveloper, rh.Create))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/redirects/{redirect_id}", rh.h.requireOrg(organizations.RoleDeveloper, rh.Update))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/redirects/{redirect_id}", rh.h.requireOrg(organizations.RoleAdmin, rh.Delete))
}

// GET .../websites/{website_id}/redirects
func (rh *redirectsHandler) List(w http.ResponseWriter, r *http.Request) {
	h := rh.h
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
	list, err := h.Domains.ListRedirects(r.Context(), orgID, ws.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Redirect{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"redirects": list})
}

// POST .../websites/{website_id}/redirects
// {"from_domain":"www.example.com","to_url":"https://example.com","status_code":301}
func (rh *redirectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	h := rh.h
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
	if ws.Status == websites.StatusDeleting || ws.Status == websites.StatusDeleted {
		httpapi.RespondError(w, httpapi.ErrConflict("website is being deleted"))
		return
	}
	var req struct {
		FromDomain string `json:"from_domain"`
		ToURL      string `json:"to_url"`
		StatusCode int    `json:"status_code"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	red, err := h.Domains.CreateRedirect(r.Context(), orgID, ws.ID, req.FromDomain, req.ToURL, req.StatusCode)
	if err != nil {
		respondRedirectError(w, err)
		return
	}
	h.audit(r, &orgID, "redirect.created", "redirect", red.ID.String(), map[string]any{"from_domain": red.FromDomain, "to_url": red.ToURL, "status_code": red.StatusCode})
	// vhost reconcile: the agent payload now carries the redirect.
	h.notifyChanged(orgID, ws)
	httpapi.WriteJSON(w, http.StatusCreated, red)
}

// PATCH .../redirects/{redirect_id} {"enabled":false,"to_url":"...","status_code":308}
func (rh *redirectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	h := rh.h
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	redirectID, err := uuid.Parse(r.PathValue("redirect_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid redirect id"))
		return
	}
	red, err := h.Domains.GetRedirect(r.Context(), orgID, redirectID)
	if err == ErrRedirectNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("redirect not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	var req struct {
		Enabled    *bool   `json:"enabled"`
		ToURL      *string `json:"to_url"`
		StatusCode *int    `json:"status_code"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	updated, err := h.Domains.UpdateRedirect(r.Context(), red, UpdateRedirectPatch{Enabled: req.Enabled, ToURL: req.ToURL, StatusCode: req.StatusCode})
	if err != nil {
		respondRedirectError(w, err)
		return
	}
	h.audit(r, &orgID, "redirect.updated", "redirect", red.ID.String(), map[string]any{"enabled": updated.Enabled, "to_url": updated.ToURL, "status_code": updated.StatusCode})
	// Toggling enabled changes serving even though only the flag moved.
	if ws, err := h.Websites.GetByIDAny(r.Context(), updated.WebsiteID); err == nil {
		h.notifyChanged(orgID, ws)
	}
	httpapi.WriteJSON(w, http.StatusOK, updated)
}

// DELETE .../redirects/{redirect_id}
func (rh *redirectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	h := rh.h
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	redirectID, err := uuid.Parse(r.PathValue("redirect_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid redirect id"))
		return
	}
	red, err := h.Domains.GetRedirect(r.Context(), orgID, redirectID)
	if err == ErrRedirectNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("redirect not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.Domains.DeleteRedirect(r.Context(), orgID, redirectID); err != nil {
		if err == ErrRedirectNotFound {
			httpapi.RespondError(w, httpapi.ErrNotFound("redirect not found"))
			return
		}
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "redirect.deleted", "redirect", red.ID.String(), map[string]any{"from_domain": red.FromDomain})
	if ws, err := h.Websites.GetByIDAny(r.Context(), red.WebsiteID); err == nil {
		h.notifyChanged(orgID, ws)
	}
	w.WriteHeader(http.StatusNoContent)
}

func respondRedirectError(w http.ResponseWriter, err error) {
	switch err {
	case ErrRedirectTaken:
		httpapi.RespondError(w, httpapi.ErrConflict("a redirect for this source domain already exists"))
	case ErrRedirectTooMany:
		httpapi.RespondError(w, httpapi.ErrConflict("redirect limit reached (20 per website)"))
	case ErrRedirectNotFound:
		httpapi.RespondError(w, httpapi.ErrNotFound("redirect not found"))
	default:
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
	}
}

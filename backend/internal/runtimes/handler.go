package runtimes

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
)

type Handler struct {
	Runtimes   *Store
	Extensions *ExtensionStore
	Jobs       *jobs.Store
	Orgs       *organizations.Store
	Servers    *servers.Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/servers/{server_id}/runtimes", h.requireOrg(organizations.RoleBilling, h.List))
	mux.HandleFunc("POST /v1/organizations/{org_id}/servers/{server_id}/runtimes", h.requireOrg(organizations.RoleAdmin, h.Install))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/servers/{server_id}/runtimes/{runtime_id}", h.requireOrg(organizations.RoleAdmin, h.Remove))
	mux.HandleFunc("GET /v1/organizations/{org_id}/servers/{server_id}/runtimes/{runtime_id}/extensions", h.requireOrg(organizations.RoleBilling, h.ListExtensions))
	mux.HandleFunc("POST /v1/organizations/{org_id}/servers/{server_id}/runtimes/{runtime_id}/extensions", h.requireOrg(organizations.RoleDeveloper, h.ManageExtension))
	mux.HandleFunc("POST /v1/organizations/{org_id}/servers/{server_id}/detect-software", h.requireOrg(organizations.RoleAdmin, h.DetectSoftware))
}

func (h *Handler) requireOrg(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		orgID, apiErr := h.RequireOrg(r, r.PathValue("org_id"), min)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r.WithContext(withOrgID(r.Context(), orgID)))
	}
}

type orgIDCtxKey struct{}

func withOrgID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, orgIDCtxKey{}, id)
}

func OrgIDFromRequest(r *http.Request) (uuid.UUID, bool) {
	id, ok := r.Context().Value(orgIDCtxKey{}).(uuid.UUID)
	return id, ok
}

func (h *Handler) auditUser(r *http.Request, orgID *uuid.UUID, action, resourceType, resourceID string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	var actorID *uuid.UUID
	if usr, ok := httpapi.UserFrom(r.Context()); ok {
		if uid, err := uuid.Parse(usr.ID); err == nil {
			actorID = &uid
		}
	}
	h.Audit.RecordBestEffort(r.Context(), audit.Entry{
		OrganizationID: orgID,
		ActorUserID:    actorID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		Metadata:       meta,
		IP:             clientIP(r),
	})
}

func clientIP(r *http.Request) string { return httpapi.ClientIP(r) }

var errOrgContext = errStr("org id missing from request context")

type errStr string

func (e errStr) Error() string { return string(e) }

// GET .../runtimes
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srvID, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	list, err := h.Runtimes.ListForServer(r.Context(), srvID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Runtime{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"runtimes": list})
}

// POST .../runtimes  {"type": "php", "version": "8.3"}  -> 202 + runtime row
func (h *Handler) Install(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srvID, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Type    string `json:"type"`
		Version string `json:"version"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if !ValidType(req.Type) {
		httpapi.RespondError(w, httpapi.ErrValidation("type must be one of: php, node, python, go"))
		return
	}
	req.Version = strings.TrimSpace(req.Version)
	if !ValidVersionForType(Type(req.Type), req.Version) {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid version for "+req.Type+": node wants a major (e.g. 22), php/python/go want major.minor (e.g. 8.3, 3.12, 1.22)"))
		return
	}

	user, _ := httpapi.UserFrom(r.Context())
	createdBy, err := uuid.Parse(user.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	rt, err := h.Runtimes.Create(r.Context(), srvID, createdBy, Type(req.Type), req.Version)
	if err == ErrDuplicate {
		httpapi.RespondError(w, httpapi.ErrConflict("this runtime version is already installed or being installed; failed installs can be retried"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	payload := InstallPayload{RuntimeID: rt.ID, Type: string(rt.Type), Version: rt.Version}
	if _, err := h.Jobs.Enqueue(r.Context(), srvID, nil, jobs.TypeInstallRuntime, payload); err != nil {
		_ = h.Runtimes.SetStatus(r.Context(), rt.ID, StatusFailed, "enqueue failed: "+err.Error())
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	h.auditUser(r, &orgID, "runtime.install_requested", "runtime", rt.ID.String(), map[string]any{"type": req.Type, "version": req.Version})
	httpapi.WriteJSON(w, http.StatusAccepted, rt)
}

// DELETE .../runtimes/{runtime_id} — refused while websites still use it.
func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srvID, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	rtID, err := uuid.Parse(r.PathValue("runtime_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid runtime id"))
		return
	}
	rt, err := h.Runtimes.GetByID(r.Context(), srvID, rtID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("runtime not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if rt.Status == StatusInstalling || rt.Status == StatusRemoving {
		httpapi.RespondError(w, httpapi.ErrConflict("runtime operation already in progress"))
		return
	}

	inUse, err := h.Runtimes.CountWebsitesUsing(r.Context(), srvID, rt.Type, rt.Version)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if inUse > 0 {
		httpapi.RespondError(w, httpapi.ErrConflict("runtime is in use by websites; reassign them first"))
		return
	}

	if _, err := h.Jobs.Enqueue(r.Context(), srvID, nil, jobs.TypeRemoveRuntime, InstallPayload{RuntimeID: rt.ID, Type: string(rt.Type), Version: rt.Version}); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.Runtimes.SetStatus(r.Context(), rt.ID, StatusRemoving, ""); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "runtime.remove_requested", "runtime", rt.ID.String(), map[string]any{"type": string(rt.Type), "version": rt.Version})
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) serverFromPath(r *http.Request, orgID uuid.UUID) (uuid.UUID, *httpapi.APIError) {
	serverID, err := uuid.Parse(r.PathValue("server_id"))
	if err != nil {
		return uuid.Nil, httpapi.ErrValidation("invalid server id")
	}
	if _, err := h.Servers.GetByID(r.Context(), serverID); err == servers.ErrNotFound {
		return uuid.Nil, httpapi.ErrNotFound("server not found")
	} else if err != nil {
		return uuid.Nil, httpapi.ErrInternal(err)
	}
	return serverID, nil
}

type InstallPayload struct {
	RuntimeID uuid.UUID `json:"runtime_id"`
	Type      string    `json:"type"`
	Version   string    `json:"version"`
}

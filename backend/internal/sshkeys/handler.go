package sshkeys

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

type Handler struct {
	Keys       *Store
	Jobs       *jobs.Store
	Websites   *websites.Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

// List/Create are admin+ (Phase 5): SSH keys are an admin-only tool in the
// customer cPanel; the server-side gate matches the hidden navigation.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/ssh-keys", h.requireOrg(organizations.RoleAdmin, h.List))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/ssh-keys", h.requireOrg(organizations.RoleAdmin, h.Create))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/ssh-keys/{key_id}", h.requireOrg(organizations.RoleAdmin, h.Delete))
}

func (h *Handler) requireOrg(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if _, apiErr := h.RequireOrg(r, r.PathValue("org_id"), min); apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r)
	}
}

func (h *Handler) audit(r *http.Request, orgID *uuid.UUID, action, resourceID string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	user, _ := httpapi.UserFrom(r.Context())
	var actorID *uuid.UUID
	if user != nil {
		if uid, err := uuid.Parse(user.ID); err == nil {
			actorID = &uid
		}
	}
	h.Audit.RecordBestEffort(r.Context(), audit.Entry{
		OrganizationID: orgID,
		ActorUserID:    actorID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   "ssh_key",
		ResourceID:     resourceID,
		Metadata:       meta,
	})
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	// Tenant scoping: the website must belong to the path org (audit S4 —
	// the previous version returned any site's keys to any org member).
	if _, apiErr := h.websiteFromPath(r, orgID); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	wsID, err := uuid.Parse(r.PathValue("website_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid website id"))
		return
	}
	keys, err := h.Keys.ListForWebsite(r.Context(), wsID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if keys == nil {
		keys = []SSHKey{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"ssh_keys": keys})
}

// Create validates and stores the key, then enqueues an authorized_keys sync.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) < 1 || len(req.Name) > 60 {
		httpapi.RespondError(w, httpapi.ErrValidation("name must be 1-60 characters"))
		return
	}
	normalized, fingerprint, err := ParseAndFingerprint(req.PublicKey)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	user, _ := httpapi.UserFrom(r.Context())
	userID, parseErr := uuid.Parse(user.ID)
	if parseErr != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(parseErr))
		return
	}
	k, err := h.Keys.Create(r.Context(), userID, orgID, ws.ID, req.Name, normalized, fingerprint)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
		return
	}
	h.syncKeys(r, ws)
	h.audit(r, &orgID, "ssh_key.added", k.ID.String(), map[string]any{"fingerprint": fingerprint})
	httpapi.WriteJSON(w, http.StatusCreated, k)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	keyID, err := uuid.Parse(r.PathValue("key_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid key id"))
		return
	}
	// resolve the site to re-sync its authorized_keys after removal
	wsID, resErr := h.Keys.WebsiteForKey(r.Context(), orgID, keyID)
	if resErr != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("ssh key not found"))
		return
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, wsID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.Keys.Delete(r.Context(), orgID, keyID); err != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("ssh key not found"))
		return
	}
	h.syncKeys(r, ws)
	h.audit(r, &orgID, "ssh_key.removed", keyID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// syncKeys enqueues the authorized_keys sync for the site's server with the
// full desired key list.
func (h *Handler) syncKeys(r *http.Request, ws *websites.Website) {
	if h.Jobs == nil {
		return
	}
	keys, err := h.Keys.KeysForWebsite(r.Context(), ws.ID)
	if err != nil {
		keys = nil
	}
	_, _ = h.Jobs.EnqueueForWebsite(r.Context(), ws.ID, "sync_ssh_keys", map[string]any{
		"website_id": ws.ID.String(),
		"keys":       keys,
	})
}

func (h *Handler) websiteFromPath(r *http.Request, orgID uuid.UUID) (*websites.Website, *httpapi.APIError) {
	websiteID, err := uuid.Parse(r.PathValue("website_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid website id")
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, websiteID)
	if err == websites.ErrNotFound {
		return nil, httpapi.ErrNotFound("website not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return ws, nil
}

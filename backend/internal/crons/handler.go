package crons

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
	Crons      *Store
	Jobs       *jobs.Store
	Websites   *websites.Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/crons", h.requireOrg(organizations.RoleDeveloper, h.List))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/crons", h.requireOrg(organizations.RoleDeveloper, h.Create))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/crons/{cron_id}", h.requireOrg(organizations.RoleDeveloper, h.Toggle))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/crons/{cron_id}", h.requireOrg(organizations.RoleAdmin, h.Delete))
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
		ResourceType:   "cron",
		ResourceID:     resourceID,
		Metadata:       meta,
	})
}

// enqueueSync asks the agent to rewrite the site's crontab from the DB.
func (h *Handler) enqueueSync(r *http.Request, ws *websites.Website) {
	entries, _ := h.Crons.EntriesForWebsite(r.Context(), ws.ID)
	payload := map[string]any{
		"website_id": ws.ID.String(),
		"entries":    entries,
	}
	if _, err := h.Jobs.EnqueueForWebsite(r.Context(), ws.ID, "sync_crontab", payload); err != nil {
		// sync failure shouldn't fail the API call; the scheduler re-syncs hourly
		_ = err
	}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
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
	list, err := h.Crons.ListForWebsite(r.Context(), ws.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []CronJob{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"crons": list})
}

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
		Schedule string `json:"schedule"`
		Command  string `json:"command"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Schedule = strings.TrimSpace(req.Schedule)
	req.Command = strings.TrimSpace(req.Command)
	if !ValidSchedule(req.Schedule) {
		httpapi.RespondError(w, httpapi.ErrValidation("schedule must be 5 cron fields, e.g. '*/5 * * * *'"))
		return
	}
	if !ValidCommand(req.Command) {
		httpapi.RespondError(w, httpapi.ErrValidation("command contains forbidden characters (no chaining, pipes, backticks, sudo)"))
		return
	}
	c, err := h.Crons.Create(r.Context(), orgID, ws.ID, req.Schedule, req.Command)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "cron.created", c.ID.String(), map[string]any{"schedule": req.Schedule, "command": req.Command})
	h.enqueueSync(r, ws)
	httpapi.WriteJSON(w, http.StatusCreated, c)
}

func (h *Handler) Toggle(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	cronID, err := uuid.Parse(r.PathValue("cron_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid cron id"))
		return
	}
	c, err := h.Crons.GetByID(r.Context(), orgID, cronID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("cron job not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	newStatus := "paused"
	if c.Status == "paused" {
		newStatus = "active"
	}
	if err := h.Crons.SetStatus(r.Context(), orgID, cronID, newStatus); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, c.WebsiteID)
	if err == nil {
		h.enqueueSync(r, ws)
	}
	h.audit(r, &orgID, "cron."+newStatus, cronID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	cronID, err := uuid.Parse(r.PathValue("cron_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid cron id"))
		return
	}
	c, err := h.Crons.GetByID(r.Context(), orgID, cronID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("cron job not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.Crons.Delete(r.Context(), orgID, cronID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, c.WebsiteID)
	if err == nil {
		h.enqueueSync(r, ws)
	}
	h.audit(r, &orgID, "cron.deleted", cronID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

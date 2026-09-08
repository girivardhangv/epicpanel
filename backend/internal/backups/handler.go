package backups

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

type Handler struct {
	Backups    *Store
	Jobs       *jobs.Store
	Websites   *websites.Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
	// DatabasesForWebsite returns the attached database list (engine, name, user)
	// for backup/restore payloads; implemented by the api layer.
	DatabasesForWebsite func(ctx context.Context, websiteID uuid.UUID) ([]DBRef, error)
	// Limits is the create-time count gate over the unified resource engine
	// (Phase 9): plan Backups cap validated before a backup row is created.
	Limits CountLimiter
}

// CountLimiter validates counted resources at create time (implemented by
// the api layer over internal/resourcelimits).
type CountLimiter interface {
	CheckCount(ctx context.Context, orgID uuid.UUID, resource string) error
}

// DBRef identifies a database attached to a website.
type DBRef struct {
	Engine string
	Name   string
	User   string
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/backups", h.requireOrg(organizations.RoleBilling, h.List))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/backups", h.requireOrg(organizations.RoleDeveloper, h.Create))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/websites/{website_id}/backup-config", h.requireOrg(organizations.RoleAdmin, h.UpdateConfig))
	mux.HandleFunc("POST /v1/organizations/{org_id}/backups/{backup_id}/restore", h.requireOrg(organizations.RoleAdmin, h.Restore))
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

func (h *Handler) audit(r *http.Request, orgID *uuid.UUID, action, resourceType, resourceID string, meta map[string]any) {
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

// POST .../backups — manual backup.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
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
	user, _ := httpapi.UserFrom(r.Context())
	createdBy, _ := uuid.Parse(user.ID)

	// Plan count gate: backups per org (Phase 9 unified engine).
	if h.Limits != nil {
		if err := h.Limits.CheckCount(r.Context(), orgID, "backups"); err != nil {
			httpapi.RespondError(w, httpapi.ErrForbidden(err.Error()))
			return
		}
	}

	b, err := h.Backups.CreateWebsiteLegacy(r.Context(), orgID, ws.ID, createdBy, "manual")
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	payload := BackupJobPayload{BackupID: b.ID.String(), WebsiteID: ws.ID.String()}
	if h.DatabasesForWebsite != nil {
		if refs, err := h.DatabasesForWebsite(r.Context(), ws.ID); err == nil {
			for _, ref := range refs {
				payload.Databases = append(payload.Databases, DBClonePayload{Engine: ref.Engine, SourceName: ref.Name, TargetName: ref.Name, User: ref.User})
			}
		}
	}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, "create_backup", payload); err != nil {
		_ = h.Backups.MarkFailed(r.Context(), b.ID, "enqueue failed")
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "backup.created", "backup", b.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, b)
}

// PATCH .../backup-config {"schedule": "off|daily|weekly", "retention": 1..30}
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		Schedule  string `json:"schedule"`
		Retention int    `json:"retention"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	switch req.Schedule {
	case "off", "daily", "weekly":
	default:
		httpapi.RespondError(w, httpapi.ErrValidation("schedule must be one of: off, daily, weekly"))
		return
	}
	if req.Retention <= 0 {
		req.Retention = 5
	}
	if req.Retention > 30 {
		req.Retention = 30
	}
	if err := h.Websites.SetBackupSchedule(r.Context(), ws.ID, req.Schedule, req.Retention); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "website.backup_config_updated", "website", ws.ID.String(), map[string]any{"schedule": req.Schedule, "retention": req.Retention})
	w.WriteHeader(http.StatusNoContent)
}

// POST .../backups/{backup_id}/restore (admin+).
func (h *Handler) Restore(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	backupID, err := uuid.Parse(r.PathValue("backup_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid backup id"))
		return
	}
	b, err := h.Backups.GetByID(r.Context(), orgID, backupID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("backup not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if b.Status != StatusSuccessful {
		httpapi.RespondError(w, httpapi.ErrConflict("only successful backups can be restored"))
		return
	}
	if b.WebsiteID == nil {
		httpapi.RespondError(w, httpapi.ErrConflict("backup has no website scope"))
		return
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, *b.WebsiteID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	payload := RestoreJobPayload{BackupID: b.ID.String(), WebsiteID: ws.ID.String(), Runtime: string(ws.Runtime), RuntimeVersion: ws.RuntimeVersion}
	if h.DatabasesForWebsite != nil {
		if refs, err := h.DatabasesForWebsite(r.Context(), ws.ID); err == nil {
			for _, ref := range refs {
				payload.Databases = append(payload.Databases, DBClonePayload{Engine: ref.Engine, SourceName: ref.Name, TargetName: ref.Name, User: ref.User})
			}
		}
	}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, "restore_backup", payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "backup.restore_requested", "backup", b.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "restore queued"})
}

// GET .../backups
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
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
	list, err := h.Backups.ListForWebsite(r.Context(), ws.ID, 20)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Backup{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"backups": list})
}

// ApplyJobOutcome advances backup rows for finished backup jobs.
func (h *Handler) ApplyJobOutcome(ctx context.Context, job *jobs.Job, result json.RawMessage) {
	switch job.Type {
	case "create_backup", "restore_backup":
	default:
		return
	}
	var p BackupJobPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return
	}
	backupID, err := uuid.Parse(p.BackupID)
	if err != nil {
		return
	}
	switch job.Type {
	case "create_backup":
		switch job.Status {
		case "success":
			var outcome struct {
				SizeBytes int64    `json:"size_bytes"`
				Databases []string `json:"databases"`
			}
			_ = json.Unmarshal(result, &outcome)
			if err := h.Backups.MarkSuccessful(ctx, backupID, outcome.SizeBytes, outcome.SizeBytes, "", outcome.Databases); err != nil && err != ErrNotFound {
				slogError("backup mark successful failed", backupID, err)
				return
			}
			if b, err := h.Backups.GetAny(ctx, backupID); err == nil && b.WebsiteID != nil {
				_ = h.Websites.TouchLastBackup(ctx, *b.WebsiteID)
				// Retention: delete rows beyond retention (agent archives remain
				// until website deletion; disk-bound cleanup is a later phase).
				if ws, err := h.Websites.GetByIDAny(ctx, *b.WebsiteID); err == nil {
					if pruned, err := h.Backups.PruneExcessForWebsite(ctx, ws.ID, ws.BackupRetention); err == nil && len(pruned) > 0 {
						ids := make([]uuid.UUID, 0, len(pruned))
						for _, pr := range pruned {
							ids = append(ids, pr.ID)
						}
						h.PruneArchives(ctx, ws.ServerID, ids)
					}
				}
			}
		case "failed":
			if err := h.Backups.MarkFailed(ctx, backupID, job.Error); err != nil && err != ErrNotFound {
				slogError("backup mark failed failed", backupID, err)
			}
		}
	case "restore_backup":
		// Restore success is reflected in audit; backup row untouched.
	}
}

func slogError(msg string, id uuid.UUID, err error) {
	slog.Error(msg, "id", id, "err", err)
}

// PruneArchives is set by the api layer to ask the agent to remove archives.
var PruneArchivesHandler func(ctx context.Context, serverID uuid.UUID, backupIDs []uuid.UUID)

func (h *Handler) PruneArchives(ctx context.Context, serverID uuid.UUID, backupIDs []uuid.UUID) {
	if PruneArchivesHandler == nil || len(backupIDs) == 0 {
		return
	}
	PruneArchivesHandler(ctx, serverID, backupIDs)
}

// BackupJobPayload matches the agent's BackupJobPayload wire shape.
type BackupJobPayload struct {
	BackupID  string           `json:"backup_id"`
	WebsiteID string           `json:"website_id"`
	Databases []DBClonePayload `json:"databases,omitempty"`
}

// RestoreJobPayload matches the agent's RestoreJobPayload wire shape.
type RestoreJobPayload struct {
	BackupID       string           `json:"backup_id"`
	WebsiteID      string           `json:"website_id"`
	Databases      []DBClonePayload `json:"databases,omitempty"`
	Runtime        string           `json:"runtime,omitempty"`
	RuntimeVersion string           `json:"runtime_version,omitempty"`
}

// DBClonePayload is the wire form of one database pair.
type DBClonePayload struct {
	Engine     string `json:"engine"`
	SourceName string `json:"source_name"`
	TargetName string `json:"target_name"`
	User       string `json:"user"`
}

// MarkJobClaimed flips the backup row to running when its job is claimed.
func (h *Handler) MarkJobClaimed(ctx context.Context, job *jobs.Job) {
	var p BackupJobPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return
	}
	id, err := uuid.Parse(p.BackupID)
	if err != nil {
		return
	}
	if err := h.Backups.MarkRunning(ctx, id); err != nil && err != ErrNotFound {
		slog.Error("backup mark running failed", "backup", id, "err", err)
	}
}

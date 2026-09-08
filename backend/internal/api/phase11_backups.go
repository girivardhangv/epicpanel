package api

// Phase 11 — unified backups API. Coordinator wires registerPhase11 into
// server.go (wave contract). Routes:
//   targets CRUD (org), unified backup list/create/restore/verify, schedules.
// Authz: org roles via ResolveOrg (minimum RoleBilling for reads, RoleAdmin
// for mutations); cross-tenant access is 404. Every mutation audited.

import (
	"context"
	"encoding/json"
		"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/epicbyte/epicpanel/backend/internal/backups"
	"github.com/epicbyte/epicpanel/backend/internal/backups/sink"
	"github.com/epicbyte/epicpanel/backend/internal/discord"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/minecraft"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

// registerPhase11 mounts the unified backup engine routes.
func registerPhase11(s *Server, mux *http.ServeMux) {
	h := &phase11Handler{
		srv:  s,
		mc:   &minecraft.Store{Pool: s.Pool},
		bots: &discord.Store{Pool: s.Pool},
		requireOrg: (&servers.Handler{Orgs: s.Orgs}).ResolveOrg,
	}
	// Targets.
	mux.HandleFunc("GET /v1/organizations/{org_id}/backup-targets", h.wrapOrg(organizations.RoleBilling, h.listTargets))
	mux.HandleFunc("POST /v1/organizations/{org_id}/backup-targets", h.wrapOrg(organizations.RoleAdmin, h.createTarget))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/backup-targets/{target_id}", h.wrapOrg(organizations.RoleAdmin, h.deleteTarget))
	// Unified backups.
	mux.HandleFunc("GET /v1/organizations/{org_id}/backups2", h.wrapOrg(organizations.RoleBilling, h.listBackups))
	mux.HandleFunc("POST /v1/organizations/{org_id}/backups2", h.wrapOrg(organizations.RoleAdmin, h.createBackup))
	mux.HandleFunc("POST /v1/organizations/{org_id}/backups2/{backup_id}/restore", h.wrapOrg(organizations.RoleAdmin, h.restoreBackup))
	mux.HandleFunc("POST /v1/organizations/{org_id}/backups2/{backup_id}/verify", h.wrapOrg(organizations.RoleAdmin, h.verifyBackup))
	// Schedules.
	mux.HandleFunc("GET /v1/organizations/{org_id}/backup-schedules", h.wrapOrg(organizations.RoleBilling, h.listSchedules))
	mux.HandleFunc("POST /v1/organizations/{org_id}/backup-schedules", h.wrapOrg(organizations.RoleAdmin, h.createSchedule))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/backup-schedules/{schedule_id}", h.wrapOrg(organizations.RoleAdmin, h.deleteSchedule))
}

type phase11Handler struct {
	srv        *Server
	mc         *minecraft.Store
	bots       *discord.Store
	requireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *phase11Handler) wrapOrg(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		orgID, apiErr := h.requireOrg(r, r.PathValue("org_id"), min)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r.WithContext(withOrgIDValue(r.Context(), orgID)))
	}
}

type orgIDCtx struct{}

func withOrgIDValue(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, orgIDCtx{}, id)
}

func orgFromCtx(ctx context.Context) uuid.UUID {
	if v, ok := ctx.Value(orgIDCtx{}).(uuid.UUID); ok {
		return v
	}
	return uuid.Nil
}

// ---------------------------------------------------------------------------
// Targets
// ---------------------------------------------------------------------------

func (h *phase11Handler) listTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := h.srv.Backups.ListTargets(r.Context(), orgFromCtx(r.Context()))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"targets": targets})
}

type createTargetReq struct {
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Config    json.RawMessage `json:"config"`
	CredsEnc  string          `json:"creds_enc,omitempty"`
	IsDefault bool            `json:"is_default,omitempty"`
}

func (h *phase11Handler) createTarget(w http.ResponseWriter, r *http.Request) {
	orgID := orgFromCtx(r.Context())
	var req createTargetReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid body"))
		return
	}
	// Shape-validate through the sink layer (no connection).
	cfg := sink.Config{Kind: req.Kind}
	if len(req.Config) > 0 {
		if err := json.Unmarshal(req.Config, &cfg); err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid target config"))
			return
		}
	}
	cfg.Kind = req.Kind
	if _, err := sink.Open(cfg); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid target config: "+err.Error()))
		return
	}
	if req.Kind != sink.KindLocal && req.CredsEnc == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("creds_enc required for non-local targets"))
		return
	}
	cfg.CredsEnc = "" // public config: creds ride the dedicated sealed column
	cfgJSON, _ := json.Marshal(cfg)
	t, err := h.srv.Backups.CreateTargetInput(r.Context(), backups.TargetInput{
		OrgID: orgID, Name: req.Name, Kind: req.Kind,
		Config: cfgJSON, CredsEnc: req.CredsEnc, IsDefault: req.IsDefault,
	})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "backup_target.create", t.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusCreated, t)
}

func (h *phase11Handler) deleteTarget(w http.ResponseWriter, r *http.Request) {
	orgID := orgFromCtx(r.Context())
	targetID, err := uuid.Parse(r.PathValue("target_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid target id"))
		return
	}
	if err := h.srv.Backups.DeleteTarget(r.Context(), orgID, targetID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "backup_target.delete", targetID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Unified backups
// ---------------------------------------------------------------------------

func (h *phase11Handler) listBackups(w http.ResponseWriter, r *http.Request) {
	orgID := orgFromCtx(r.Context())
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	var (
		list []backups.Backup
		err  error
	)
	switch {
	case q.Get("instance_id") != "":
		id, perr := uuid.Parse(q.Get("instance_id"))
		if perr != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid instance_id"))
			return
		}
		list, err = h.srv.Backups.ListForInstance(r.Context(), id, limit)
	case q.Get("bot_id") != "":
		id, perr := uuid.Parse(q.Get("bot_id"))
		if perr != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid bot_id"))
			return
		}
		list, err = h.srv.Backups.ListForBot(r.Context(), id, limit)
	case q.Get("website_id") != "":
		id, perr := uuid.Parse(q.Get("website_id"))
		if perr != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid website_id"))
			return
		}
		list, err = h.srv.Backups.ListForWebsite(r.Context(), id, limit)
	default:
		list, err = h.srv.Backups.ListForOrg(r.Context(), orgID, limit)
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"backups": list})
}

type createBackupReq struct {
	Type       string `json:"type"`
	WebsiteID  string `json:"website_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	BotID      string `json:"bot_id,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
	Encrypt    bool   `json:"encrypt,omitempty"`
	Verify     bool   `json:"verify,omitempty"`
}

// resolveWorkload locates the backing server for a typed backup + verifies
// org ownership (cross-tenant = 404, never existence leaks).
func (h *phase11Handler) resolveWorkload(r *http.Request, orgID uuid.UUID, req createBackupReq) (serverID uuid.UUID, websiteID, instanceID, botID *uuid.UUID, apiErr *httpapi.APIError) {
	switch req.Type {
	case backups.TypeWebsite, backups.TypeWebsiteFiles, backups.TypeAccount, backups.TypeDatabase:
		wid, err := uuid.Parse(req.WebsiteID)
		if err != nil {
			return uuid.Nil, nil, nil, nil, httpapi.ErrValidation("invalid website_id")
		}
		ws, err := h.srv.Websites.GetByID(r.Context(), orgID, wid)
		if err != nil {
			return uuid.Nil, nil, nil, nil, httpapi.ErrNotFound("website not found")
		}
		return ws.ServerID, &ws.ID, nil, nil, nil
	case backups.TypeWorld:
		iid, err := uuid.Parse(req.InstanceID)
		if err != nil {
			return uuid.Nil, nil, nil, nil, httpapi.ErrValidation("invalid instance_id")
		}
		inst, err := h.mc.GetByID(r.Context(), orgID, iid)
		if err != nil {
			return uuid.Nil, nil, nil, nil, httpapi.ErrNotFound("instance not found")
		}
		return inst.ServerID, nil, &inst.ID, nil, nil
	case backups.TypeBot, backups.TypeFull:
		bid, err := uuid.Parse(req.BotID)
		if err != nil {
			return uuid.Nil, nil, nil, nil, httpapi.ErrValidation("invalid bot_id")
		}
		bot, err := h.bots.GetByID(r.Context(), orgID, bid)
		if err != nil {
			return uuid.Nil, nil, nil, nil, httpapi.ErrNotFound("bot not found")
		}
		return bot.ServerID, nil, nil, &bot.ID, nil
	default:
		return uuid.Nil, nil, nil, nil, httpapi.ErrValidation("unsupported backup type")
	}
}

// serverForWorkload is the admin-side resolver used by restore/verify.
func (h *phase11Handler) serverForWorkload(r *http.Request, orgID uuid.UUID, b *backups.Backup) (uuid.UUID, *uuid.UUID, *httpapi.APIError) {
	switch b.Type {
	case backups.TypeWebsite, backups.TypeWebsiteFiles, backups.TypeAccount, backups.TypeDatabase:
		if b.WebsiteID == nil {
			return uuid.Nil, nil, httpapi.ErrConflict("backup has no website scope")
		}
		ws, err := h.srv.Websites.GetByID(r.Context(), orgID, *b.WebsiteID)
		if err != nil {
			return uuid.Nil, nil, httpapi.ErrNotFound("website not found")
		}
		return ws.ServerID, &ws.ID, nil
	case backups.TypeWorld:
		if b.InstanceID == nil {
			return uuid.Nil, nil, httpapi.ErrConflict("backup has no instance scope")
		}
		inst, err := h.mc.GetByID(r.Context(), orgID, *b.InstanceID)
		if err != nil {
			return uuid.Nil, nil, httpapi.ErrNotFound("instance not found")
		}
		return inst.ServerID, nil, nil
	case backups.TypeBot, backups.TypeFull:
		if b.BotID == nil {
			return uuid.Nil, nil, httpapi.ErrConflict("backup has no bot scope")
		}
		bot, err := h.bots.GetByID(r.Context(), orgID, *b.BotID)
		if err != nil {
			return uuid.Nil, nil, httpapi.ErrNotFound("bot not found")
		}
		return bot.ServerID, nil, nil
	}
	return uuid.Nil, nil, httpapi.ErrValidation("unsupported backup type")
}

// targetConfigFor loads + seals the sink config for a backup's target.
// sinkWire converts a sink.Config to the job-payload wire shape.
func sinkWire(cfg *sink.Config) *backups.SinkConfigWire {
	if cfg == nil {
		return nil
	}
	w := &backups.SinkConfigWire{Kind: cfg.Kind, LocalDir: cfg.LocalDir, CredsEnc: cfg.CredsEnc}
	if cfg.S3 != nil {
		w.S3 = &backups.SinkS3Wire{Endpoint: cfg.S3.Endpoint, Region: cfg.S3.Region, Bucket: cfg.S3.Bucket, Prefix: cfg.S3.Prefix, PathStyle: cfg.S3.PathStyle}
	}
	if cfg.Remote != nil {
		w.Remote = &backups.SinkRemoteWire{Host: cfg.Remote.Host, Port: cfg.Remote.Port, User: cfg.Remote.User, Path: cfg.Remote.Path}
	}
	return w
}

func (h *phase11Handler) targetConfigFor(r *http.Request, orgID uuid.UUID, b *backups.Backup) (*sink.Config, error) {
	if b.TargetID == nil {
		return nil, nil
	}
	row, err := h.srv.Backups.GetTarget(r.Context(), orgID, *b.TargetID)
	if err != nil {
		return nil, err
	}
	var cfg sink.Config
	if err := json.Unmarshal(row.Config, &cfg); err != nil {
		return nil, err
	}
	cfg.CredsEnc = row.CredsEnc
	return &cfg, nil
}

func (h *phase11Handler) createBackup(w http.ResponseWriter, r *http.Request) {
	orgID := orgFromCtx(r.Context())
	var req createBackupReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid body"))
		return
	}
	if !backups.ValidType(req.Type) {
		httpapi.RespondError(w, httpapi.ErrValidation("unsupported backup type"))
		return
	}
	serverID, websiteID, instanceID, botID, apiErr := h.resolveWorkload(r, orgID, req)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	var targetCfg *sink.Config
	if req.TargetID != "" {
		tid, err := uuid.Parse(req.TargetID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid target_id"))
			return
		}
		row, err := h.srv.Backups.GetTarget(r.Context(), orgID, tid)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("unknown target"))
			return
		}
		var cfg sink.Config
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		cfg.CredsEnc = row.CredsEnc
		targetCfg = &cfg
	}
	_ = targetCfg

	// Encryption: generate + wrap a per-backup data key at rest.
	keyEnc := ""
	if req.Encrypt {
		key, err := sink.NewDataKey()
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if keyEnc, err = sink.WrapKey(key); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}

	b, err := h.srv.Backups.Create(r.Context(), &backups.Backup{
		Organization: orgID, WebsiteID: websiteID, InstanceID: instanceID, BotID: botID,
		Type: req.Type, Status: backups.StatusPending, TriggerType: "manual",
		Encrypted: req.Encrypt, Verification: backups.VerifyPending,
		Databases: []string{},
	})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if keyEnc != "" {
		if err := h.srv.Backups.SetKeyEnc(r.Context(), b.ID, keyEnc); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}

	payload := backups.BackupRunPayload{
		BackupID: b.ID.String(), Type: req.Type,
		WebsiteID: req.WebsiteID, InstanceID: req.InstanceID, BotID: req.BotID,
		Target: sinkWire(targetCfg), Encrypt: req.Encrypt, KeyEnc: keyEnc, Verify: req.Verify,
	}
	if _, err := h.srv.Jobs.Enqueue(r.Context(), serverID, websiteID, jobs.Type("backup_run"), payload); err != nil {
		_ = h.srv.Backups.MarkFailed(r.Context(), b.ID, "enqueue failed")
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "backup.create", b.ID.String(), map[string]any{"type": req.Type, "encrypted": req.Encrypt})
	httpapi.WriteJSON(w, http.StatusAccepted, b)
}

func (h *phase11Handler) restoreBackup(w http.ResponseWriter, r *http.Request) {
	orgID := orgFromCtx(r.Context())
	backupID, err := uuid.Parse(r.PathValue("backup_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid backup id"))
		return
	}
	b, err := h.srv.Backups.GetAny(r.Context(), backupID)
	if err != nil || b.Organization != orgID {
		httpapi.RespondError(w, httpapi.ErrNotFound("backup not found"))
		return
	}
	if b.Status != backups.StatusSuccessful {
		httpapi.RespondError(w, httpapi.ErrConflict("only successful backups can be restored"))
		return
	}
	serverID, websiteID, apiErr := h.serverForWorkload(r, orgID, b)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	targetCfg, err := h.targetConfigFor(r, orgID, b)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	keyEnc, _ := h.srv.Backups.KeyEncFor(r.Context(), backupID)
	payload := backups.BackupRestorePayload{
		BackupID: b.ID.String(), Type: b.Type,
		WebsiteID:  idStr(b.WebsiteID),
		InstanceID: idStr(b.InstanceID),
		BotID:      idStr(b.BotID),
		Target:     sinkWire(targetCfg), KeyEnc: keyEnc, RefOverride: refOverrideOf(b),
	}
	if _, err := h.srv.Jobs.Enqueue(r.Context(), serverID, websiteID, jobs.Type("backup_restore"), payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "backup.restore", b.ID.String(), nil)
	w.WriteHeader(http.StatusAccepted)
}

func (h *phase11Handler) verifyBackup(w http.ResponseWriter, r *http.Request) {
	orgID := orgFromCtx(r.Context())
	backupID, err := uuid.Parse(r.PathValue("backup_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid backup id"))
		return
	}
	b, err := h.srv.Backups.GetAny(r.Context(), backupID)
	if err != nil || b.Organization != orgID {
		httpapi.RespondError(w, httpapi.ErrNotFound("backup not found"))
		return
	}
	serverID, websiteID, apiErr := h.serverForWorkload(r, orgID, b)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	targetCfg, err := h.targetConfigFor(r, orgID, b)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	keyEnc, _ := h.srv.Backups.KeyEncFor(r.Context(), backupID)
	payload := backups.BackupVerifyPayload{BackupID: b.ID.String(), Target: sinkWire(targetCfg), KeyEnc: keyEnc, SHA256: b.SHA256, RefOverride: refOverrideOf(b)}
	if _, err := h.srv.Jobs.Enqueue(r.Context(), serverID, websiteID, jobs.Type("backup_verify"), payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "backup.verify", b.ID.String(), nil)
	w.WriteHeader(http.StatusAccepted)
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

func (h *phase11Handler) listSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := h.srv.Backups.ListSchedules(r.Context(), orgFromCtx(r.Context()))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"schedules": list})
}

type createScheduleReq struct {
	Type      string `json:"type"`
	WebsiteID string `json:"website_id,omitempty"`
	BotID     string `json:"bot_id,omitempty"`
	Cron      string `json:"cron"`
}

func (h *phase11Handler) createSchedule(w http.ResponseWriter, r *http.Request) {
	orgID := orgFromCtx(r.Context())
	var req createScheduleReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid body"))
		return
	}
	if !backups.ValidType(req.Type) {
		httpapi.RespondError(w, httpapi.ErrValidation("unsupported backup type"))
		return
	}
	if len(req.Cron) == 0 || len(req.Cron) > 64 || cronFieldCount(req.Cron) != 5 {
		httpapi.RespondError(w, httpapi.ErrValidation("cron must be a 5-field expression"))
		return
	}
	sc := &backups.Schedule{OrgID: orgID, Type: req.Type, Cron: req.Cron, Enabled: true, NextRunAt: nextCronRun(req.Cron)}
	if req.WebsiteID != "" {
		id, err := uuid.Parse(req.WebsiteID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid website_id"))
			return
		}
		sc.WebsiteID = &id
	}
	if req.BotID != "" {
		id, err := uuid.Parse(req.BotID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid bot_id"))
			return
		}
		sc.BotID = &id
	}
	if sc.WebsiteID == nil && sc.BotID == nil {
		httpapi.RespondError(w, httpapi.ErrValidation("website_id or bot_id required"))
		return
	}
	out, err := h.srv.Backups.CreateSchedule(r.Context(), sc)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "backup_schedule.create", out.ID.String(), map[string]any{"cron": req.Cron})
	httpapi.WriteJSON(w, http.StatusCreated, out)
}

func (h *phase11Handler) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	orgID := orgFromCtx(r.Context())
	id, err := uuid.Parse(r.PathValue("schedule_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid schedule id"))
		return
	}
	if err := h.srv.Backups.DeleteSchedule(r.Context(), orgID, id); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "backup_schedule.delete", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Shared plumbing
// ---------------------------------------------------------------------------

// refOverrideOf returns the row's stored sink ref (opaque JSON) for the agent.
func refOverrideOf(b *backups.Backup) string { return b.SinkRef }

func idStr(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// cronFieldCount validates the 5-field shape (the platform convention).
func cronFieldCount(c string) int {
	n := 0
	prevSpace := true
	for _, ch := range c {
		space := ch == ' ' || ch == '\t'
		if !space && prevSpace {
			n++
		}
		prevSpace = space
	}
	return n
}

// nextCronRun is a coarse first-fire estimate for display; the scheduler
// recomputes authoritatively when firing.
func nextCronRun(cron string) time.Time {
	fields := strings.Fields(cron)
	if len(fields) == 5 && fields[0] == "*" {
		return time.Now().Add(24 * time.Hour)
	}
	return time.Now().Add(15 * time.Minute)
}

func (h *phase11Handler) audit(r *http.Request, orgID uuid.UUID, action, resourceID string, meta map[string]any) {
	if h.srv.Audit == nil {
		return
	}
	actor := audit.ActorSystem
	if user, ok := httpapi.UserFrom(r.Context()); ok {
		actor = audit.ActorUser
		uid, _ := uuid.Parse(user.ID)
		h.srv.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &orgID,
			ActorUserID:    &uid,
			ActorType:      actor,
			Action:         action,
			ResourceType:   "backup",
			ResourceID:     resourceID,
			Metadata:       meta,
		})
		return
	}
	orgRef := orgID
	h.srv.Audit.RecordBestEffort(r.Context(), audit.Entry{
		OrganizationID: &orgRef,
		ActorType:      actor,
		Action:         action,
		ResourceType:   "backup",
		ResourceID:     resourceID,
		Metadata:       meta,
	})
}



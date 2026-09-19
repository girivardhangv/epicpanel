// ============================================================================
// Phase 6 — Admin WHM API (registered via registerPhase6).
//
// Every route is platform-admin-session-only (API tokens never inherit
// platform-admin — the Phase 1 audit finding). Cross-organization reads are
// the point of this surface; the admin role IS the authorization. All
// mutations are audited and publish bus events so Phase 13 alerting can
// subscribe. Live metrics never touch the historical DB: the read model
// aggregates from the in-memory LiveStore (internal/adminview).
// ============================================================================

package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/adminview"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// registerPhase6 mounts the admin WHM routes. The coordinator adds the
// single call line in server.go.
func registerPhase6(s *Server, mux *http.ServeMux) {
	svc := &adminview.Service{Pool: s.Pool, Live: s.LiveStore}
	h := &adminViewHandler{srv: s, svc: svc, requireAdmin: requireAdminSession}

	reads := func(mux *http.ServeMux) {
		mux.HandleFunc("GET /v1/adminview/overview", h.requireAdmin(h.overview))
		mux.HandleFunc("GET /v1/adminview/servers", h.requireAdmin(h.servers))
		mux.HandleFunc("GET /v1/adminview/accounts", h.requireAdmin(h.accounts))
		mux.HandleFunc("GET /v1/adminview/organizations", h.requireAdmin(h.organizations))
		mux.HandleFunc("GET /v1/adminview/domains", h.requireAdmin(h.domains))
		mux.HandleFunc("GET /v1/adminview/databases", h.requireAdmin(h.databases))
		mux.HandleFunc("GET /v1/adminview/backups", h.requireAdmin(h.backups))
		mux.HandleFunc("GET /v1/adminview/dns-zones", h.requireAdmin(h.zones))
		mux.HandleFunc("GET /v1/adminview/ports", h.requireAdmin(h.ports))
		mux.HandleFunc("GET /v1/adminview/alerts", h.requireAdmin(h.alerts))
		mux.HandleFunc("GET /v1/adminview/users", h.requireAdmin(h.users))
		mux.HandleFunc("GET /v1/adminview/jobs", h.requireAdmin(h.jobsList))
		mux.HandleFunc("GET /v1/adminview/jobs/dead-letter", h.requireAdmin(h.deadLetter))
		mux.HandleFunc("GET /v1/adminview/live", h.requireAdmin(h.liveFrames))
	}
	reads(mux)

	// Account lifecycle (cross-org; audited + events on the bus).
	mux.HandleFunc("POST /v1/adminview/accounts/{website_id}/suspend", h.requireAdmin(h.suspendAccount))
	mux.HandleFunc("POST /v1/adminview/accounts/{website_id}/resume", h.requireAdmin(h.resumeAccount))

	// Package assignment (plan changes converge sites via the Phase 9 engine).
	mux.HandleFunc("PATCH /v1/adminview/organizations/{org_id}/package", h.requireAdmin(h.setOrgPackage))

	// Jobs console (retry / cancel; dead-letter read-only above).
	mux.HandleFunc("POST /v1/adminview/jobs/{job_id}/retry", h.requireAdmin(h.retryJob))
	mux.HandleFunc("POST /v1/adminview/jobs/{job_id}/cancel", h.requireAdmin(h.cancelJob))
}

// requireAdminSession delegates to the shared httpapi.RequireAdmin gate:
// platform-admin session or epa_ platform admin API key; org tokens never.
func requireAdminSession(next http.HandlerFunc) http.HandlerFunc {
	return httpapi.RequireAdmin(next)
}

type adminViewHandler struct {
	srv          *Server
	svc          *adminview.Service
	requireAdmin func(http.HandlerFunc) http.HandlerFunc
}

// audit records an admin action (platform-wide: no organization scoping).
func (h *adminViewHandler) audit(r *http.Request, action, resourceType, resourceID string, meta map[string]any) {
	if h.srv.Audit == nil {
		return
	}
	var actorID *uuid.UUID
	if user, ok := httpapi.UserFrom(r.Context()); ok {
		if uid, err := uuid.Parse(user.ID); err == nil {
			actorID = &uid
		}
	}
	h.srv.Audit.RecordBestEffort(r.Context(), audit.Entry{
		ActorUserID:  actorID,
		ActorType:    audit.ActorUser,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Metadata:     meta,
		IP:           httpapi.ClientIP(r),
	})
}

// publish fans an admin action out on the event bus (WS + Phase 13 seams).
func (h *adminViewHandler) publish(eventType string, orgID *uuid.UUID, resourceType, resourceID string, payload map[string]any) {
	if h.srv.Events == nil {
		return
	}
	h.srv.Events.Publish(context.Background(), events.Event{
		Type:         eventType,
		Organization: orgID,
		ActorType:    "user",
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Payload:      payload,
	})
}

// ---------------------------------------------------------------------------
// read handlers
// ---------------------------------------------------------------------------

func (h *adminViewHandler) overview(w http.ResponseWriter, r *http.Request) {
	ov, err := h.svc.Overview(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, ov)
}

func (h *adminViewHandler) servers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Servers(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"servers": rows})
}

func (h *adminViewHandler) accounts(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Accounts(r.Context(), adminview.AccountFilter{
		OrgID:    r.URL.Query().Get("org_id"),
		ServerID: r.URL.Query().Get("server_id"),
		Status:   r.URL.Query().Get("status"),
		Search:   r.URL.Query().Get("q"),
		Limit:    atoiDefault(r.URL.Query().Get("limit"), 200),
	})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"accounts": rows})
}

func (h *adminViewHandler) organizations(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Organizations(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"organizations": rows})
}

func (h *adminViewHandler) domains(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Domains(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"domains": rows})
}

func (h *adminViewHandler) databases(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Databases(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"databases": rows})
}

func (h *adminViewHandler) backups(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Backups(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"backups": rows})
}

func (h *adminViewHandler) zones(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Zones(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"zones": rows})
}

func (h *adminViewHandler) ports(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.PortPools(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (h *adminViewHandler) alerts(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Alerts(r.Context(), r.URL.Query().Get("resolved") != "true")
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"alerts": rows})
}

func (h *adminViewHandler) users(w http.ResponseWriter, r *http.Request) {
	rows, err := h.svc.Users(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	var svcAccounts, activeTokens int64
	_ = h.srv.Pool.QueryRow(r.Context(), `SELECT
			(SELECT count(*) FROM service_accounts WHERE revoked_at IS NULL),
			(SELECT count(*) FROM api_tokens WHERE revoked_at IS NULL)`).Scan(&svcAccounts, &activeTokens)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"users": rows,
		"security": map[string]any{
			"service_accounts_active": svcAccounts,
			"api_tokens_active":       activeTokens,
		},
	})
}

// liveFrames is the one-shot seed for the admin app's WS-driven dashboard.
// After this load the UI relies solely on the live WebSocket stream.
func (h *adminViewHandler) liveFrames(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"frames": h.svc.LiveFrames()})
}

// jobsList is the admin jobs console: optional type/status/server filters.
func (h *adminViewHandler) jobsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, typ, serverID := q.Get("status"), q.Get("type"), q.Get("server_id")
	limit := atoiDefault(q.Get("limit"), 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `
		SELECT j.id::text, j.server_id, coalesce(sv.name, ''), j.website_id, coalesce(w.name, ''),
		       j.type::text, j.status::text, j.error, j.attempts, j.max_attempts, j.created_at, j.finished_at
		FROM jobs j
		JOIN servers sv ON sv.id = j.server_id
		LEFT JOIN websites w ON w.id = j.website_id
		WHERE 1 = 1`
	args := []any{}
	if status != "" {
		args = append(args, status)
		query += ` AND j.status::text = $` + strconv.Itoa(len(args))
	}
	if typ != "" {
		args = append(args, typ)
		query += ` AND j.type::text = $` + strconv.Itoa(len(args))
	}
	if serverID != "" {
		args = append(args, serverID)
		query += ` AND j.server_id = $` + strconv.Itoa(len(args))
	}
	args = append(args, limit)
	query += ` ORDER BY j.created_at DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := h.srv.Pool.Query(r.Context(), query, args...)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, typ, server, wname, errMsg string
		var jstatus string
		var serverIDv uuid.UUID
		var websiteID *uuid.UUID
		var attempts, maxAttempts int
		var created time.Time
		var finished *time.Time
		if err := rows.Scan(&id, &serverIDv, &server, &websiteID, &wname, &typ, &jstatus, &errMsg,
			&attempts, &maxAttempts, &created, &finished); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		row := map[string]any{
			"id": id, "server_id": serverIDv, "server": server, "website": wname,
			"type": typ, "status": jstatus, "error": errMsg,
			"attempts": attempts, "max_attempts": maxAttempts,
			"created_at": created.UTC().Format(time.RFC3339),
		}
		if websiteID != nil {
			row["website_id"] = *websiteID
		}
		if finished != nil {
			row["finished_at"] = finished.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (h *adminViewHandler) deadLetter(w http.ResponseWriter, r *http.Request) {
	list, err := h.srv.Jobs.ListByStatus(r.Context(), "failed", atoiDefault(r.URL.Query().Get("limit"), 100))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": list})
}

// ---------------------------------------------------------------------------
// mutations (audited + events)
// ---------------------------------------------------------------------------

// suspendAccount enqueues the Phase 4 suspend_website job for one account.
// Reuses the org-scoped job contract (same payload + idempotency key as the
// customer flow), so agent behavior and state transitions are identical.
func (h *adminViewHandler) suspendAccount(w http.ResponseWriter, r *http.Request) {
	h.lifecycleAccount(w, r, true)
}

func (h *adminViewHandler) resumeAccount(w http.ResponseWriter, r *http.Request) {
	h.lifecycleAccount(w, r, false)
}

func (h *adminViewHandler) lifecycleAccount(w http.ResponseWriter, r *http.Request, suspend bool) {
	websiteID, err := uuid.Parse(r.PathValue("website_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid website id"))
		return
	}
	ws, err := h.srv.Websites.GetByIDAny(r.Context(), websiteID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("website not found"))
		return
	}
	if suspend {
		if ws.Status != websites.StatusReady && ws.Status != websites.StatusFailed {
			httpapi.RespondError(w, httpapi.ErrConflict("only ready websites can be suspended"))
			return
		}
	} else {
		if ws.Status != websites.StatusSuspended {
			httpapi.RespondError(w, httpapi.ErrConflict("only suspended websites can be resumed"))
			return
		}
	}
	jobType := jobs.Type("suspend_website")
	key := "suspend_" + ws.ID.String()
	action := "adminview.account.suspend"
	verb := "suspend"
	var payload any = websites.SuspendPayload{WebsiteID: ws.ID.String()}
	if !suspend {
		jobType = jobs.Type("resume_website")
		key = "resume_" + ws.ID.String()
		action = "adminview.account.resume"
		verb = "resume"
		payload = websites.ResumePayload{WebsiteID: ws.ID.String()}
	}
	job, err := h.srv.Jobs.EnqueueIdempotent(r.Context(), ws.ServerID, &ws.ID, jobType, payload, key)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, action, "website", ws.ID.String(), map[string]any{
		"name": ws.Name, "organization": ws.Organization, "server": ws.ServerID, "job": job.ID,
	})
	org := ws.Organization
	h.publish("admin.account_"+verb, &org, "website", ws.ID.String(), map[string]any{
		"name": ws.Name, "organization_id": ws.Organization, "job_id": job.ID, verb: true,
	})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID})
}

// setOrgPackage assigns a hosting package (plan) to an organization.
// The Phase 9 engine reads the plan row directly, so the next enforce_limits
// convergence applies the new limits without any extra bookkeeping here.
func (h *adminViewHandler) setOrgPackage(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	var req struct {
		PackageID string `json:"package_id"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	pkgID, err := uuid.Parse(req.PackageID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("package_id must be a UUID"))
		return
	}
	var exists bool
	if err := h.srv.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM hosting_packages WHERE id = $1)`, pkgID).Scan(&exists); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if !exists {
		httpapi.RespondError(w, httpapi.ErrNotFound("package not found"))
		return
	}
	tag, err := h.srv.Pool.Exec(r.Context(), `UPDATE organizations SET package_id = $2, updated_at = now() WHERE id = $1`, orgID, pkgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if tag.RowsAffected() == 0 {
		httpapi.RespondError(w, httpapi.ErrNotFound("organization not found"))
		return
	}
	h.audit(r, "adminview.organization.package", "organization", orgID.String(), map[string]any{"package_id": pkgID.String()})
	h.publish("admin.organization_package_changed", &orgID, "organization", orgID.String(), map[string]any{"package_id": pkgID.String()})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"organization_id": orgID, "package_id": pkgID})
}

// retryJob gives a terminal job another run.
//   - failed  -> the row returns to pending (attempts reset; history kept)
//   - success -> a fresh job is enqueued with the same type/payload
//   - pending/running -> conflict (still live)
func (h *adminViewHandler) retryJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := uuid.Parse(r.PathValue("job_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid job id"))
		return
	}
	job, err := h.srv.Jobs.GetByID(r.Context(), jobID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("job not found"))
		return
	}
	switch job.Status {
	case jobs.StatusFailed:
		tag, err := h.srv.Pool.Exec(r.Context(), `
			UPDATE jobs SET status = 'pending', attempts = 0, error = '', result = '{}'::jsonb,
			       finished_at = NULL, claimed_at = NULL, lease_expires_at = NULL, visible_after = now(), updated_at = now()
			WHERE id = $1 AND status = 'failed'`, jobID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if tag.RowsAffected() == 0 {
			httpapi.RespondError(w, httpapi.ErrConflict("job is no longer failed"))
			return
		}
		h.audit(r, "adminview.job.retry", "job", jobID.String(), map[string]any{"type": string(job.Type), "mode": "requeue"})
		h.publish("admin.job_retried", nil, "job", jobID.String(), map[string]any{"type": string(job.Type)})
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "pending"})
	case jobs.StatusSuccess:
		fresh, err := h.srv.Jobs.Enqueue(r.Context(), job.ServerID, job.WebsiteID, job.Type, job.Payload)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		h.audit(r, "adminview.job.retry", "job", jobID.String(), map[string]any{"type": string(job.Type), "mode": "reenqueue", "new_job": fresh.ID})
		h.publish("admin.job_retried", nil, "job", jobID.String(), map[string]any{"type": string(job.Type), "new_job_id": fresh.ID})
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": fresh.ID})
	default:
		httpapi.RespondError(w, httpapi.ErrConflict("only finished jobs can be retried"))
	}
}

// cancelJob cancels a pending job (the agent has not claimed it). Running
// jobs are governed by their lease + the reaper — the control plane cannot
// reach into an executing job, so cancelling one is refused honestly.
func (h *adminViewHandler) cancelJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := uuid.Parse(r.PathValue("job_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid job id"))
		return
	}
	job, err := h.srv.Jobs.GetByID(r.Context(), jobID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("job not found"))
		return
	}
	if job.Status != jobs.StatusPending {
		httpapi.RespondError(w, httpapi.ErrConflict("only pending jobs can be cancelled; running jobs are governed by their agent lease"))
		return
	}
	tag, err := h.srv.Pool.Exec(r.Context(), `
		UPDATE jobs SET status = 'failed', error = 'cancelled by administrator',
		       finished_at = now(), lease_expires_at = NULL, updated_at = now()
		WHERE id = $1 AND status = 'pending'`, jobID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if tag.RowsAffected() == 0 {
		httpapi.RespondError(w, httpapi.ErrConflict("job is no longer pending"))
		return
	}
	h.audit(r, "adminview.job.cancel", "job", jobID.String(), map[string]any{"type": string(job.Type)})
	h.publish("admin.job_cancelled", nil, "job", jobID.String(), map[string]any{"type": string(job.Type)})
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

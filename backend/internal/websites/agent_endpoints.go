package websites

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
)

// AgentClaim lets an authenticated agent atomically claim its next pending job.
// Response: {"job": {...}} or {"job": null} when the queue is empty.
func (h *Handler) AgentClaim(w http.ResponseWriter, r *http.Request) {
	srv, ok := servers.ServerFromAgentContext(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("agent authentication required"))
		return
	}

	job, err := h.Jobs.ClaimNext(r.Context(), srv.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if job == nil {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"job": nil})
		return
	}

	// Reflect claim in the website lifecycle.
	if job.Type == jobs.TypeProvisionWebsite && job.WebsiteID != nil {
		if err := h.Websites.SetStatus(r.Context(), *job.WebsiteID, StatusProvisioning, ""); err != nil && err != ErrNotFound {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}
	// Claim-state fanout for other subsystems (deployments, backups).
	if h.OnJobClaimed != nil {
		h.OnJobClaimed(r.Context(), job)
	}
	if h.Events != nil {
		org := srv.Organization
		h.Events.Publish(r.Context(), events.Event{
			Type:         "job.claimed",
			Organization: &org,
			ActorType:    "system",
			ResourceType: "job",
			ResourceID:   job.ID.String(),
			Payload:      map[string]any{"job_type": string(job.Type), "server_id": srv.ID.String()},
		})
	}

	if h.Audit != nil {
		h.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &srv.Organization,
			ActorType:      audit.ActorSystem,
			Action:         "job.claimed",
			ResourceType:   "job",
			ResourceID:     job.ID.String(),
			Metadata:       map[string]any{"type": string(job.Type), "server": srv.Name},
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"job": job})
}

type agentResultRequest struct {
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Result  json.RawMessage `json:"result"`
}

type agentProgressRequest struct {
	Progress int    `json:"progress"`
	Step     string `json:"step"`
}

// AgentProgress receives live progress updates (0-100 + stage text) while a
// job runs. The UI polls this to show real activity instead of a fake bar.
func (h *Handler) AgentProgress(w http.ResponseWriter, r *http.Request) {
	srv, ok := servers.ServerFromAgentContext(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("agent authentication required"))
		return
	}
	jobID, err := uuid.Parse(r.PathValue("job_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid job id"))
		return
	}
	job, err := h.Jobs.GetByID(r.Context(), jobID)
	if err == jobs.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("job not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if job.ServerID != srv.ID {
		httpapi.RespondError(w, httpapi.ErrNotFound("job not found"))
		return
	}
	var req agentProgressRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if len(req.Step) > 300 {
		req.Step = req.Step[:300]
	}
	if err := h.Jobs.UpdateProgress(r.Context(), jobID, req.Progress, req.Step); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type provisionResult struct {
	UnixUser     string `json:"unix_user"`
	DocumentRoot string `json:"document_root"`
}

// AgentResult receives the outcome of a claimed job and advances the website
// lifecycle accordingly.
func (h *Handler) AgentResult(w http.ResponseWriter, r *http.Request) {
	srv, ok := servers.ServerFromAgentContext(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("agent authentication required"))
		return
	}
	jobID, err := uuid.Parse(r.PathValue("job_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid job id"))
		return
	}

	// The job must exist and belong to this agent's server.
	job, err := h.Jobs.GetByID(r.Context(), jobID)
	if err == jobs.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("job not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if job.ServerID != srv.ID {
		httpapi.RespondError(w, httpapi.ErrNotFound("job not found"))
		return
	}

	var req agentResultRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	updated, err := h.Jobs.ReportResult(r.Context(), jobID, req.Success, req.Result, req.Error)
	if err == jobs.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrConflict("job is not in running state"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	if h.Audit != nil {
		h.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &srv.Organization,
			ActorType:      audit.ActorSystem,
			Action:         "job." + string(updated.Status),
			ResourceType:   "job",
			ResourceID:     updated.ID.String(),
			Metadata:       map[string]any{"type": string(updated.Type), "attempts": updated.Attempts, "error": updated.Error},
		})
	}

	if h.OnJobFinished != nil {
		h.OnJobFinished(r.Context(), updated, req.Result)
	}
	if h.Events != nil {
		evType := "job.succeeded"
		if updated.Status == jobs.StatusFailed {
			evType = "job.failed"
		} else if updated.Status == jobs.StatusPending {
			evType = "job.retry_scheduled"
		}
		org := srv.Organization
		h.Events.Publish(r.Context(), events.Event{
			Type:         evType,
			Organization: &org,
			ActorType:    "system",
			ResourceType: "job",
			ResourceID:   updated.ID.String(),
			Payload:      map[string]any{"job_type": string(updated.Type), "attempts": updated.Attempts, "error": updated.Error},
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"job": updated})
}

// ApplyWebsiteTransition advances the website lifecycle for a finished
// website-related job. Exported for composition via OnJobFinished.
func (h *Handler) ApplyWebsiteTransition(job *jobs.Job, resultJSON json.RawMessage) {
	if job.WebsiteID == nil {
		return
	}
	ctx := context.Background()

	switch job.Type {
	case jobs.TypeProvisionWebsite:
		switch job.Status {
		case jobs.StatusSuccess:
			var pr provisionResult
			_ = json.Unmarshal(resultJSON, &pr)
			if err := h.Websites.MarkReady(ctx, *job.WebsiteID, pr.UnixUser, pr.DocumentRoot); err != nil &&
				!errors.Is(err, ErrNotFound) && !errors.Is(err, ErrMarkReadyBlocked) {
				slog.Error("website mark ready failed", "website", job.WebsiteID, "err", err)
			}
			// App-platform chaining: a provisioned app site whose desired
			// state is running converges to built + started with no further
			// user action (build_app success chains start_app below).
			if ws, err := h.Websites.GetByIDAny(ctx, *job.WebsiteID); err == nil && ws != nil &&
				isAppRuntime(ws.Runtime) && ws.AppDesiredState == "running" {
				if err := h.enqueueBuildApp(ctx, ws); err != nil {
					slog.Error("app build enqueue failed", "website", ws.ID, "err", err)
				}
			}
		case jobs.StatusFailed:
			if err := h.Websites.SetStatus(ctx, *job.WebsiteID, StatusFailed, job.Error); err != nil && err != ErrNotFound {
				slog.Error("website mark failed", "website", job.WebsiteID, "err", err)
			}
		}
	case jobs.TypeBuildApp:
		if job.Status == jobs.StatusSuccess {
			if ws, err := h.Websites.GetByIDAny(ctx, *job.WebsiteID); err == nil && ws != nil &&
				isAppRuntime(ws.Runtime) && ws.AppDesiredState == "running" {
				if err := h.enqueueStartApp(ctx, ws); err != nil {
					slog.Error("app start enqueue failed", "website", ws.ID, "err", err)
				}
			}
		}
	case jobs.TypeInstallRuntime:
		if job.Status == jobs.StatusSuccess {
			// Sites created with install_if_missing converge now that the
			// runtime landed: provision every pending site waiting on it.
			var p struct {
				Type    string `json:"type"`
				Version string `json:"version"`
			}
			if err := json.Unmarshal(job.Payload, &p); err == nil && p.Type != "" {
				pending, err := h.Websites.ListPendingForRuntime(ctx, job.ServerID, Runtime(p.Type), p.Version)
				if err != nil {
					slog.Error("pending sites lookup failed", "server", job.ServerID, "err", err)
				}
				for i := range pending {
					if apiErr := h.provisionPendingSite(ctx, &pending[i]); apiErr != nil {
						slog.Error("post-install provision enqueue failed", "website", pending[i].ID, "err", apiErr)
					}
				}
			}
		}
	case jobs.TypeDeleteWebsite:
		switch job.Status {
		case jobs.StatusSuccess:
			if err := h.Websites.DeleteByServer(ctx, job.ServerID, *job.WebsiteID); err != nil && err != ErrNotFound {
				slog.Error("website delete row failed", "website", job.WebsiteID, "err", err)
			}
		case jobs.StatusFailed:
			if err := h.Websites.SetStatus(ctx, *job.WebsiteID, StatusFailed, job.Error); err != nil && err != ErrNotFound {
				slog.Error("website mark failed", "website", job.WebsiteID, "err", err)
			}
		}
	case TypeSuspendWebsite:
		switch job.Status {
		case jobs.StatusSuccess:
			// Guarded: only ready/failed/suspended move; never resurrects
			// deleting/deleted. Failure keeps the previous status (the
			// error is recorded on the job) and the queue retries. The
			// payload's reason (absent on legacy payloads = manual) is
			// persisted with the transition so pages/API/audit agree.
			var sp SuspendPayload
			_ = json.Unmarshal(job.Payload, &sp) // legacy payloads carry website_id only
			if err := h.Websites.MarkSuspended(ctx, *job.WebsiteID, sp.EffectiveReason(), sp.Metadata); err != nil && !errors.Is(err, ErrNotFound) {
				slog.Error("website mark suspended failed", "website", job.WebsiteID, "err", err)
			}
			h.publishLifecycleEvent(ctx, "website.suspended", job, map[string]any{
				"reason": string(sp.EffectiveReason()),
			})
		}
	case TypeResumeWebsite:
		switch job.Status {
		case jobs.StatusSuccess:
			// Guarded: only suspended moves back to ready. Failure keeps
			// the site suspended (the error is recorded on the job).
			if err := h.Websites.MarkResumed(ctx, *job.WebsiteID); err != nil && !errors.Is(err, ErrNotFound) {
				slog.Error("website mark resumed failed", "website", job.WebsiteID, "err", err)
			}
			h.publishLifecycleEvent(ctx, "website.resumed", job, nil)
		}
	case jobs.TypeInstallLaravel:
		// One-click Laravel: the agent reports the new serving docroot
		// (<site>/app/public). Persist it so every later desired-state build
		// (and the api-layer vhost reconcile) serves the framework layout.
		if job.Status != jobs.StatusSuccess {
			return
		}
		var lr laravelResult
		if err := json.Unmarshal(resultJSON, &lr); err == nil && strings.HasPrefix(lr.DocumentRoot, "/srv/epicpanel/websites/") {
			if err := h.Websites.SetServingDocroot(ctx, *job.WebsiteID, "app/public", lr.DocumentRoot); err != nil && !errors.Is(err, ErrNotFound) {
				slog.Error("laravel docroot update failed", "website", job.WebsiteID, "err", err)
			}
		}
	}
}

// provisionPendingSite enqueues the provision job for a site that was
// created with install_if_missing and is now unblocked (runtime available).
func (h *Handler) provisionPendingSite(ctx context.Context, ws *Website) *httpapi.APIError {
	payload, apiErr := h.buildDesiredPayload(ctx, ws, ws.Organization, ws.UnixUser, ws.RuntimeVersion)
	if apiErr != nil {
		return apiErr
	}
	if _, err := h.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeProvisionWebsite, payload, "provision_website_"+ws.ID.String()); err != nil {
		return httpapi.ErrInternal(err)
	}
	return nil
}

// JobOutcomeSubscriber receives finished jobs so other subsystems (runtimes)
// can advance their state machines without the jobs package knowing about them.
type JobOutcomeSubscriber interface {
	OnJobFinished(ctx context.Context, job *jobs.Job, result json.RawMessage)
}

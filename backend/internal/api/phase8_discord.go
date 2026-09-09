package api

// ============================================================================
// Phase 8 — Discord bot hosting API (registered via registerPhase8).
//
// Every route is org-scoped server-side (ResolveOrg): cross-tenant access is
// 404, mutations require developer+, reads require billing+. Secrets are
// write-only: env values and git tokens never appear in any response; the
// console/log pipeline scrubs secret values before bytes leave the server.
// ============================================================================

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/discord"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
	"github.com/epicbyte/epicpanel/backend/internal/runtimes"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
)

// registerPhase8 mounts the Discord bot routes. The coordinator adds the
// single call line in server.go.
func registerPhase8(s *Server, mux *http.ServeMux) {
	srvH := &servers.Handler{Orgs: s.Orgs}
	h := &botHandler{srv: s, bots: &discord.Store{Pool: s.Pool}, requireOrg: srvH.ResolveOrg}

	mux.HandleFunc("GET /v1/organizations/{org_id}/bots", h.wrap(organizations.RoleBilling, h.list))
	mux.HandleFunc("POST /v1/organizations/{org_id}/bots", h.wrap(organizations.RoleDeveloper, h.create))
	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/runtime-offers", h.wrap(organizations.RoleBilling, h.runtimeOffers))
	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/{bot_id}", h.wrap(organizations.RoleBilling, h.get))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/bots/{bot_id}", h.wrap(organizations.RoleDeveloper, h.update))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/bots/{bot_id}", h.wrap(organizations.RoleDeveloper, h.delete))

	for _, action := range []string{"start", "stop", "restart", "kill"} {
		mux.HandleFunc("POST /v1/organizations/{org_id}/bots/{bot_id}/"+action,
			h.wrap(organizations.RoleDeveloper, h.lifecycle(action)))
	}

	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/{bot_id}/env", h.wrap(organizations.RoleBilling, h.getEnv))
	mux.HandleFunc("PUT /v1/organizations/{org_id}/bots/{bot_id}/env", h.wrap(organizations.RoleDeveloper, h.putEnv))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/bots/{bot_id}/env/{key}", h.wrap(organizations.RoleDeveloper, h.deleteEnvKey))

	mux.HandleFunc("POST /v1/organizations/{org_id}/bots/{bot_id}/deploy-git", h.wrap(organizations.RoleDeveloper, h.deployGit))
	mux.HandleFunc("POST /v1/organizations/{org_id}/bots/{bot_id}/files/upload", h.wrap(organizations.RoleDeveloper, h.uploadFile))
	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/{bot_id}/files", h.wrap(organizations.RoleDeveloper, h.listFiles))

	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/{bot_id}/logs", h.wrap(organizations.RoleBilling, h.logs))
	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/{bot_id}/console", h.wrap(organizations.RoleBilling, h.consoleTail))
	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/{bot_id}/console/ws", h.wrap(organizations.RoleBilling, h.consoleWS))

	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/{bot_id}/metrics", h.wrap(organizations.RoleBilling, h.metrics))
	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/{bot_id}/jobs", h.wrap(organizations.RoleBilling, h.botJobs))

	mux.HandleFunc("GET /v1/organizations/{org_id}/bots/{bot_id}/schedules", h.wrap(organizations.RoleBilling, h.listSchedules))
	mux.HandleFunc("POST /v1/organizations/{org_id}/bots/{bot_id}/schedules", h.wrap(organizations.RoleDeveloper, h.createSchedule))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/bots/{bot_id}/schedules/{schedule_id}", h.wrap(organizations.RoleDeveloper, h.deleteSchedule))
}

// ---------------------------------------------------------------------------
// handler plumbing
// ---------------------------------------------------------------------------

type botHandler struct {
	srv        *Server
	bots       *discord.Store
	requireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

// wrap enforces auth + org role, then delegates.
func (h *botHandler) wrap(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if _, apiErr := h.requireOrg(r, r.PathValue("org_id"), min); apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r)
	}
}

func (h *botHandler) org(r *http.Request) uuid.UUID {
	id, _ := uuid.Parse(r.PathValue("org_id"))
	return id
}

func (h *botHandler) audit(r *http.Request, orgID uuid.UUID, action string, botID uuid.UUID, meta map[string]any) {
	if h.srv.Audit == nil {
		return
	}
	user, _ := httpapi.UserFrom(r.Context())
	var actorID *uuid.UUID
	if user != nil {
		if uid, err := uuid.Parse(user.ID); err == nil {
			actorID = &uid
		}
	}
	h.srv.Audit.RecordBestEffort(r.Context(), audit.Entry{
		OrganizationID: &orgID,
		ActorUserID:    actorID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   "bot",
		ResourceID:     botID.String(),
		Metadata:       meta,
	})
}

func (h *botHandler) actorID(r *http.Request) uuid.UUID {
	user, _ := httpapi.UserFrom(r.Context())
	if user == nil {
		return uuid.Nil
	}
	uid, _ := uuid.Parse(user.ID)
	return uid
}

func (h *botHandler) botFromPath(r *http.Request, orgID uuid.UUID) (*discord.Bot, *httpapi.APIError) {
	botID, err := uuid.Parse(r.PathValue("bot_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid bot id")
	}
	bot, err := h.bots.GetByID(r.Context(), orgID, botID)
	if err == discord.ErrNotFound {
		return nil, httpapi.ErrNotFound("bot not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return bot, nil
}

// enqueue wraps the job store; bot jobs carry the bot id for per-bot lists.
func (h *botHandler) enqueue(ctx context.Context, serverID, botID uuid.UUID, jobType string, payload any) (*jobs.Job, error) {
	return botEnqueue(ctx, h.srv.Jobs, serverID, botID, jobs.Type(jobType), payload)
}

func (h *botHandler) publish(r *http.Request, orgID uuid.UUID, eventType string, botID uuid.UUID, payload map[string]any) {
	if h.srv.Events == nil {
		return
	}
	org := orgID
	h.srv.Events.Publish(r.Context(), events.Event{
		Type:         eventType,
		Organization: &org,
		ResourceType: "bot",
		ResourceID:   botID.String(),
		Payload:      payload,
	})
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

type createBotRequest struct {
	Name           string            `json:"name"`
	Runtime        string            `json:"runtime"`
	RuntimeVersion string            `json:"runtime_version"`
	StartupFile    string            `json:"startup_file"`
	StartupCommand string            `json:"startup_command"`
	BuildCommand   string            `json:"build_command"`
	Env            map[string]string `json:"env_vars"`
	GitRepo        string            `json:"git_repo_url"`
	GitBranch      string            `json:"git_branch"`
	GitToken       string            `json:"git_token"`
	RestartPolicy  string            `json:"restart_policy"`
	MaxRestarts    int               `json:"max_restarts"`
	ServerID       string            `json:"server_id"`
}

func (h *botHandler) create(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	var req createBotRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	// Phase 9 create-time gate: the org's plan must be a bot plan and the
	// bot count must fit. On Discord plans the per-workload count column is
	// max_websites (Discord Basic/Pro = 1 bot). Fail closed when the plan
	// cannot be resolved.
	//
	// Unassigned orgs are NOT blocked here: when no hosting package is
	// explicitly assigned (and no platform default exists), PlanForOrg
	// cannot name a bot-capable plan — failing that lookup would make bot
	// hosting dead on arrival for every fresh organization. Those orgs run
	// on the platform-default limits (the resolved fallback plan still
	// feeds the count gate below). A GENUINE plan conflict — the org is
	// explicitly on a non-Discord package — stays a hard 403.
	if h.srv.ResourceLimits == nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("plan limits unavailable; bot creation blocked"))
		return
	}
	plan, err := h.srv.ResourceLimits.PlanForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("plan limits unavailable; bot creation blocked"))
		return
	}
	if plan.Kind != string(resources.KindDiscord) && h.orgHasOwnPlan(r.Context(), orgID) {
		httpapi.RespondError(w, httpapi.ErrForbidden("the organization plan does not include Discord bot hosting"))
		return
	}
	count, err := h.bots.CountForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if plan.MaxWebsites > 0 && count >= plan.MaxWebsites {
		httpapi.RespondError(w, httpapi.ErrForbidden("plan limit reached: "+plan.Name+" allows "+strconv.Itoa(plan.MaxWebsites)+" bot(s)"))
		return
	}

	serverID, needsRuntime, apiErr := h.resolveServer(r, orgID, req.ServerID, req.Runtime, req.RuntimeVersion)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	// Runtime missing on the picked server: register a runtime row and
	// enqueue install_runtime HERE (validation phase — nothing mutated yet).
	// Jobs claim oldest-first, so this install runs before the bot_install
	// job enqueued below: the agent has the interpreter on the box before it
	// provisions the bot tree. Only AFTER this passed do the mutating steps
	// run, so a runtime-registration error cannot orphan a half-created bot.
	if needsRuntime {
		runtimeType := req.Runtime
		if runtimeType == "" {
			runtimeType = "node"
		}
		if err := h.enqueueBotRuntimeInstall(r.Context(), orgID, h.actorID(r), serverID, runtimeType, req.RuntimeVersion); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}

	bot, err := discord.CreateBot(r.Context(), h.bots, orgID, serverID, h.actorID(r), discord.CreateInput{
		Name: req.Name, Runtime: req.Runtime, RuntimeVersion: req.RuntimeVersion,
		StartupFile: req.StartupFile, StartupCommand: req.StartupCommand, BuildCommand: req.BuildCommand,
		Env: req.Env, GitRepo: req.GitRepo, GitBranch: req.GitBranch, GitToken: req.GitToken,
		RestartPolicy: req.RestartPolicy, MaxRestarts: req.MaxRestarts,
	})
	if err == discord.ErrNameTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("a bot with this name already exists"))
		return
	}
	if err != nil {
		if discord.IsValidationError(err) {
			httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
			return
		}
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	payload := discord.BotInstallPayload{BotID: bot.ID.String(), Runtime: bot.Runtime, RuntimeVersion: bot.RuntimeVer, EnvEnc: discord.EnvEncOf(nil)}
	if _, err := h.enqueue(r.Context(), serverID, bot.ID, discord.JobBotInstall, payload); err != nil {
		_ = h.bots.MarkFailed(r.Context(), bot.ID, "install enqueue failed")
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.publish(r, orgID, discord.EventBotCreated, bot.ID, map[string]any{"name": bot.Name, "runtime": bot.Runtime})
	h.audit(r, orgID, "bot.created", bot.ID, map[string]any{"runtime": bot.Runtime})
	httpapi.WriteJSON(w, http.StatusCreated, bot)
}

// orgHasOwnPlan reports whether the org is EXPLICITLY assigned a hosting
// package (package_id set) — i.e. a non-Discord plan kind is a real admin
// decision, not the platform default fallback. Best effort: on error the
// org is treated as unassigned (gate stays satisfiable; a missing row would
// 404/500 at insert time anyway).
func (h *botHandler) orgHasOwnPlan(ctx context.Context, orgID uuid.UUID) bool {
	var has bool
	if err := h.srv.Pool.QueryRow(ctx,
		`SELECT package_id IS NOT NULL FROM organizations WHERE id = $1`, orgID).Scan(&has); err != nil {
		return false
	}
	return has
}

// resolveServer resolves the target server for a bot.
//
// Runtime placement, best effort in order (Phase 12 placement model):
//  1. explicit server_id wins (admin/customer choice — no runtime filter);
//  2. auto-pick an ONLINE server that already has the bot runtime registered;
//  3. auto-pick any online server and signal the caller to enqueue an
//     install_runtime job FIRST — jobs claim oldest-first, so the runtime
//     install runs before the bot_install provisioning job.
//
// A missing runtime is therefore never a dead end; only an empty/offline
// fleet is.
func (h *botHandler) resolveServer(r *http.Request, orgID uuid.UUID, serverIDParam, runtime, version string) (uuid.UUID, bool, *httpapi.APIError) {
	if serverIDParam != "" {
		id, err := uuid.Parse(serverIDParam)
		if err != nil {
			return uuid.Nil, false, httpapi.ErrValidation("invalid server_id")
		}
		if _, err := h.srv.Servers.GetByID(r.Context(), id); err != nil {
			return uuid.Nil, false, httpapi.ErrNotFound("server not found")
		}
		return id, false, nil
	}
	runtimeType := runtime
	if runtimeType == "" {
		runtimeType = "node"
	}
	id, err := h.srv.Servers.AutoPickServer(r.Context(), runtimeType, version)
	if err == nil {
		return id, false, nil
	}
	// No server has the bot runtime registered: fall back to any online
	// server; the caller queues the runtime install ahead of bot_install.
	id, err = h.srv.Servers.AutoPickServer(r.Context(), "", "")
	if err != nil {
		return uuid.Nil, false, httpapi.ErrValidation("no online server available for this bot")
	}
	return id, true, nil
}

// enqueueBotRuntimeInstall registers the runtime row + enqueues the
// install_runtime job for a bot runtime missing on the target server. Uses
// the platform runtimes pipeline verbatim (same registry row, same job
// payload contract, same fanout) so the bot path stays one code path behind
// the runtime manager.
func (h *botHandler) enqueueBotRuntimeInstall(ctx context.Context, orgID, actorID, serverID uuid.UUID, runtimeType, version string) error {
	rt, err := discord.RuntimeFor(runtimeType)
	if err != nil {
		return fmt.Errorf("bot runtime %q has no installable runtime", runtimeType)
	}
	if version == "" {
		version = rt.DefaultVersion()
	}
	// Registry majors: node/java take a single major, python takes major.minor.
	regType := runtimes.Type(runtimeType)
	regVersion := version
	if strings.Contains(regVersion, ".") && (regType == runtimes.TypeNode || regType == runtimes.TypeJava) {
		regVersion = strings.Split(regVersion, ".")[0]
	}
	// Store.Create resets a FAILED row for retry; an existing
	// installing/available row means the runtime is already there — reuse it.
	if _, err := h.srv.Runtimes.Create(ctx, serverID, actorID, regType, regVersion); err != nil && err != runtimes.ErrDuplicate {
		return err
	}
	row, err := h.srv.Runtimes.GetByTypeVersion(ctx, serverID, regType, regVersion)
	if err != nil {
		return err
	}
	payload := runtimes.InstallPayload{RuntimeID: row.ID, Type: string(regType), Version: regVersion}
	if _, err := h.srv.Jobs.Enqueue(ctx, serverID, nil, jobs.TypeInstallRuntime, payload); err != nil {
		_ = h.srv.Runtimes.SetStatus(ctx, row.ID, runtimes.StatusFailed, "enqueue failed: "+err.Error())
		return err
	}
	return nil
}

func (h *botHandler) list(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bots, err := h.bots.ListForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	out := make([]map[string]any, 0, len(bots))
	for i := range bots {
		out = append(out, h.botView(r, &bots[i]))
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"bots": out})
}

func (h *botHandler) get(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, h.botView(r, bot))
}

// botView projects a bot + live metrics (LIVE/STALE/OFFLINE + age). Env keys
// ride along; values NEVER do.
func (h *botHandler) botView(r *http.Request, bot *discord.Bot) map[string]any {
	out := map[string]any{
		"id":              bot.ID,
		"organization_id": bot.OrgID,
		"server_id":       bot.ServerID,
		"name":            bot.Name,
		"runtime":         bot.Runtime,
		"runtime_version": bot.RuntimeVer,
		"status":          bot.Status,
		"desired_state":   bot.DesiredState,
		"startup_file":    bot.StartupFile,
		"startup_command": bot.StartupCmd,
		"build_command":   bot.BuildCmd,
		"restart_policy":  bot.RestartPolicy,
		"max_restarts":    bot.MaxRestarts,
		"restart_count":   bot.RestartCount,
		"git_repo_url":    bot.GitRepo,
		"git_branch":      bot.GitBranch,
		"env_keys":        bot.EnvKeys,
		"last_error":      bot.LastError,
		"unit_state":      bot.UnitState,
		"agent_seen_at":   bot.AgentSeenAt,
		"created_at":      bot.CreatedAt,
		"updated_at":      bot.UpdatedAt,
	}
	if m, ok := h.liveMetrics(bot); ok {
		out["metrics"] = m
	}
	return out
}

// liveMetrics reads the Phase 3 live store (WS-streamed samples) for the
// bot's app envelope. Freshness is computed against the sample age — old
// metrics are never presented as current.
func (h *botHandler) liveMetrics(bot *discord.Bot) (map[string]any, bool) {
	if h.srv.LiveStore == nil {
		return nil, false
	}
	frame := h.srv.LiveStore.Frame(bot.ServerID)
	if frame == nil {
		return map[string]any{"freshness": map[string]any{"state": "OFFLINE", "age_ms": -1}, "kind": "discord"}, true
	}
	for _, app := range frame.Apps {
		if app.WebsiteID == bot.ID.String() {
			return map[string]any{
				"kind":          "discord",
				"status":        app.Status,
				"cpu_percent":   app.CPUPercent,
				"memory_bytes":  app.MemoryBytes,
				"net_rx_bps":    app.NetRxBPS,
				"net_tx_bps":    app.NetTxBPS,
				"disk_used_mb":  app.DiskUsedMB,
				"uptime_s":      app.UptimeS,
				"restart_count": app.RestartCount,
				"freshness":     frame.Freshness,
			}, true
		}
	}
	return map[string]any{"freshness": frame.Freshness, "kind": "discord"}, true
}

type updateBotRequest struct {
	StartupFile    string `json:"startup_file"`
	StartupCommand string `json:"startup_command"`
	BuildCommand   string `json:"build_command"`
	RuntimeVersion string `json:"runtime_version"`
}

func (h *botHandler) update(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req updateBotRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	rt, err := discord.RuntimeFor(bot.Runtime)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	version := req.RuntimeVersion
	if version == "" {
		version = bot.RuntimeVer
	}
	if version != bot.RuntimeVer {
		if verr := rt.ValidateVersion(version); verr != nil {
			httpapi.RespondError(w, httpapi.ErrValidation(verr.Error()))
			return
		}
	}
	if err := rt.ValidateStartup(req.StartupFile, req.StartupCommand); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	// Preserve ciphertexts (env + git token) — this endpoint never touches
	// secrets, so it cannot leak them either.
	row, err := h.bots.GetBotRow(r.Context(), orgID, bot.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.bots.UpdateConfig(r.Context(), bot.ID, req.StartupFile, req.StartupCommand, req.BuildCommand, version, row.EnvEnc, row.Bot.GitRepo, row.Bot.GitBranch, row.GitTokenEnc); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "bot.updated", bot.ID, nil)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "updated (applies at next start/restart)"})
}

func (h *botHandler) delete(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if err := h.bots.MarkDeleting(r.Context(), bot.ID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if _, err := h.enqueue(r.Context(), bot.ServerID, bot.ID, discord.JobBotDelete, discord.BotIDPayload{BotID: bot.ID.String()}); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.publish(r, orgID, discord.EventBotDeleted, bot.ID, nil)
	h.audit(r, orgID, "bot.deleted", bot.ID, nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "deletion queued"})
}

// ---------------------------------------------------------------------------
// lifecycle
// ---------------------------------------------------------------------------

// lifecycle returns the start/stop/restart/kill handler. Jobs are enqueued
// idempotently; the DB status moves to starting/stopping and ONLY agent
// truth (via the reconciliation loop) promotes it further.
func (h *botHandler) lifecycle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := h.org(r)
		bot, apiErr := h.botFromPath(r, orgID)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		ctx := r.Context()
		var jobType, event string
		var payload any
		switch action {
		case "start", "restart":
			if action == "start" {
				if !(discord.LifecycleGuard{}).CanStart(bot.Status) {
					httpapi.RespondError(w, httpapi.ErrConflict("bot in state "+string(bot.Status)+" cannot start"))
					return
				}
			}
			row, err := h.bots.GetBotRow(ctx, orgID, bot.ID)
			if err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
			if err := h.bots.MarkStarting(ctx, bot.ID); err != nil {
				httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
				return
			}
			// The customer wants it running; reconciliation converges to
			// agent truth against this target.
			_ = h.bots.SetDesiredState(ctx, bot.ID, discord.DesiredRunning)
			jobType = discord.JobBotStart
			if action == "restart" {
				jobType = discord.JobBotRestart
			}
			payload = h.startPayload(row)
			event = discord.EventBotStarting
		case "stop", "kill":
			if !(discord.LifecycleGuard{}).CanStop(bot.Status) {
				httpapi.RespondError(w, httpapi.ErrConflict("bot in state "+string(bot.Status)+" cannot stop"))
				return
			}
			if err := h.bots.MarkStopping(ctx, bot.ID); err != nil {
				httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
				return
			}
			_ = h.bots.SetDesiredState(ctx, bot.ID, discord.DesiredStopped)
			jobType = discord.JobBotStop
			if action == "kill" {
				jobType = discord.JobBotKill
			}
			payload = discord.BotIDPayload{BotID: bot.ID.String()}
			event = discord.EventBotStopping
		default:
			httpapi.RespondError(w, httpapi.ErrValidation("unknown action"))
			return
		}
		if _, err := h.enqueue(ctx, bot.ServerID, bot.ID, jobType, payload); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		h.publish(r, orgID, event, bot.ID, nil)
		h.audit(r, orgID, "bot."+action, bot.ID, nil)
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": action + " queued", "job_type": jobType})
	}
}

// startPayload assembles the start/restart job body from the stored row.
// Env rides as ciphertext; limits come from the Phase 9 engine — the SAME
// numbers the usage bars display (anti-drift rule).
func (h *botHandler) startPayload(row *discord.BotRow) discord.BotLifecyclePayload {
	return discord.BotLifecyclePayload{
		BotID:          row.Bot.ID.String(),
		Runtime:        row.Bot.Runtime,
		RuntimeVersion: row.Bot.RuntimeVer,
		StartupFile:    row.Bot.StartupFile,
		StartupCommand: row.Bot.StartupCmd,
		EnvEnc:         row.EnvEnc,
		RestartPolicy:  row.Bot.RestartPolicy,
		NetAllow:       row.Bot.NetAllow,
		Limits:         h.srv.botLimitsForOrg(row.Bot.OrgID),
	}
}

// ---------------------------------------------------------------------------
// env vars + secrets (write-only, masked reads)
// ---------------------------------------------------------------------------

func (h *botHandler) getEnv(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	// Values are NEVER returned. Keys only.
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"keys":  bot.EnvKeys,
		"count": len(bot.EnvKeys),
		"note":  "values are write-only; set a key again to change it",
	})
}

type putEnvRequest struct {
	Vars map[string]string `json:"vars"`
}

func (h *botHandler) putEnv(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req putEnvRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if req.Vars == nil {
		req.Vars = map[string]string{}
	}
	enc, err := discord.EncryptEnv(req.Vars)
	if err != nil {
		if discord.IsValidationError(err) {
			httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		} else {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
		}
		return
	}
	if err := h.bots.SetEnvEnc(r.Context(), bot.ID, enc); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "bot.env_updated", bot.ID, map[string]any{"count": len(req.Vars)})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"keys": discord.MaskEnv(req.Vars).Keys,
		"note": "stored encrypted; applied at next start/restart",
	})
}

func (h *botHandler) deleteEnvKey(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	key := r.PathValue("key")
	if err := discord.ValidateEnvKey(key); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	row, err := h.bots.GetBotRow(r.Context(), orgID, bot.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	vars, err := discord.DecryptEnv(row.EnvEnc)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	delete(vars, key)
	enc, err := discord.EncryptEnv(vars)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.bots.SetEnvEnc(r.Context(), bot.ID, enc); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "bot.env_key_deleted", bot.ID, map[string]any{"key": key})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"keys": discord.MaskEnv(vars).Keys})
}

// ---------------------------------------------------------------------------
// deploy / files
// ---------------------------------------------------------------------------

type deployGitRequest struct {
	RepoURL string `json:"repo_url"`
	Branch  string `json:"branch"`
	Token   string `json:"token"`
}

func (h *botHandler) deployGit(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req deployGitRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if req.RepoURL == "" && bot.GitRepo == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("repo_url is required (first deploy)"))
		return
	}
	if req.RepoURL != "" && !discord.ValidGitURL(req.RepoURL) {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid git repository URL"))
		return
	}
	row, err := h.bots.GetBotRow(r.Context(), orgID, bot.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	repo := req.RepoURL
	if repo == "" {
		repo = bot.GitRepo
	}
	branch := req.Branch
	if branch == "" {
		branch = bot.GitBranch
	}
	tokenEnc := row.GitTokenEnc
	if req.Token != "" {
		enc, terr := discord.EncryptGitToken(req.Token)
		if terr != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(terr))
			return
		}
		tokenEnc = enc
	}
	if err := h.bots.UpdateConfig(r.Context(), bot.ID, bot.StartupFile, bot.StartupCmd, bot.BuildCmd, bot.RuntimeVer, row.EnvEnc, repo, branch, tokenEnc); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	payload := discord.BotDeployGitPayload{
		BotID: bot.ID.String(), RepoURL: repo, Branch: branch, TokenEnc: tokenEnc,
		BuildTimeout: int(discord.DefaultBuildTimeout.Seconds()),
	}
	if _, err := h.enqueue(r.Context(), bot.ServerID, bot.ID, discord.JobBotDeployGit, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// The response NEVER contains the token; only the repo surface.
	h.audit(r, orgID, "bot.deploy_queued", bot.ID, nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "git deploy queued", "repo_url": repo, "branch": branch})
}

type uploadFileRequest struct {
	Path       string `json:"path"`
	ContentB64 string `json:"content_b64"`
}

func (h *botHandler) uploadFile(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req uploadFileRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("path is required"))
		return
	}
	payload := discord.BotUploadPayload{BotID: bot.ID.String(), Path: req.Path, B64: req.ContentB64}
	if _, err := h.enqueue(r.Context(), bot.ServerID, bot.ID, discord.JobBotUpload, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "bot.file_uploaded", bot.ID, map[string]any{"path": req.Path})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "upload queued", "path": req.Path})
}

func (h *botHandler) listFiles(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	payload := discord.BotFilesPayload{BotID: bot.ID.String(), Path: r.URL.Query().Get("path")}
	if _, err := h.enqueue(r.Context(), bot.ServerID, bot.ID, discord.JobBotFiles, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "listing queued; fetch the job result", "job_type": discord.JobBotFiles})
}

// ---------------------------------------------------------------------------
// console + logs
// ---------------------------------------------------------------------------

func (h *botHandler) logs(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	lines := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			lines = n
		}
	}
	payload := discord.BotLogsPayload{BotID: bot.ID.String(), Lines: lines}
	if _, err := h.enqueue(r.Context(), bot.ServerID, bot.ID, discord.JobBotLogs, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "log fetch queued"})
}

// consoleTail serves the REST snapshot: runs a bot_logs job synchronously
// (bounded wait) and returns scrubbed lines + the cursor.
func (h *botHandler) consoleTail(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	lines := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			lines = n
		}
	}
	out, err := h.runBotLogsJob(r, bot, lines)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

// runBotLogsJob enqueues bot_logs and waits (server-side, bounded) for the
// agent result.
func (h *botHandler) runBotLogsJob(r *http.Request, bot *discord.Bot, lines int) (*discord.BotLogsOutcome, error) {
	payload := discord.BotLogsPayload{BotID: bot.ID.String(), Lines: lines}
	job, err := h.enqueue(r.Context(), bot.ServerID, bot.ID, discord.JobBotLogs, payload)
	if err != nil {
		return nil, err
	}
	result, err := h.awaitJob(r.Context(), job.ID, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var out discord.BotLogsOutcome
	if err := json.Unmarshal(result, &out); err != nil {
		return nil, err
	}
	// Defense in depth: scrub once more at the edge against the stored env.
	if row, rerr := h.bots.GetBotRow(r.Context(), bot.OrgID, bot.ID); rerr == nil {
		if vars, derr := discord.DecryptEnv(row.EnvEnc); derr == nil {
			token, _ := discord.DecryptGitToken(row.GitTokenEnc)
			secrets := discord.SecretValues(vars, token)
			for i := range out.Lines {
				out.Lines[i].Text = discord.ScrubText(out.Lines[i].Text, secrets)
			}
		}
	}
	return &out, nil
}

// consoleWS upgrades to the live tail (discord.ServeConsoleWS hub). Input is
// discarded by design — no shell, no docker, no command channel.
func (h *botHandler) consoleWS(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	discord.ServeConsoleWS(w, r, bot.ID)
}

// ---------------------------------------------------------------------------
// metrics + jobs + runtime offers
// ---------------------------------------------------------------------------

func (h *botHandler) metrics(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	m, _ := h.liveMetrics(bot)
	httpapi.WriteJSON(w, http.StatusOK, m)
}

func (h *botHandler) botJobs(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	list, err := botListRecent(r.Context(), h.srv.Jobs, bot.ID, limit, false)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []jobs.Job{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": list})
}

func (h *botHandler) runtimeOffers(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"runtimes": discord.VersionOffers()})
}

// ---------------------------------------------------------------------------
// schedules
// ---------------------------------------------------------------------------

type createScheduleRequest struct {
	Kind string `json:"kind"`
	Cron string `json:"cron"`
}

func (h *botHandler) listSchedules(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	list, err := h.bots.ListSchedules(r.Context(), bot.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"schedules": list})
}

func (h *botHandler) createSchedule(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	bot, apiErr := h.botFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req createScheduleRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	// Scheduled CUSTOM commands would be arbitrary shell by another name —
	// only lifecycle kinds are allowed.
	switch req.Kind {
	case "restart", "start", "stop":
	default:
		httpapi.RespondError(w, httpapi.ErrValidation("kind must be restart, start or stop (custom commands are not schedulable)"))
		return
	}
	interval, err := discord.ParseCron(req.Cron)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	created, err := h.bots.CreateSchedule(r.Context(), bot.ID, req.Kind, req.Cron, interval)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "bot.schedule_created", bot.ID, map[string]any{"kind": req.Kind, "cron": req.Cron})
	httpapi.WriteJSON(w, http.StatusCreated, created)
}

func (h *botHandler) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	if _, apiErr := h.botFromPath(r, orgID); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	scheduleID, err := uuid.Parse(r.PathValue("schedule_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid schedule id"))
		return
	}
	if err := h.bots.DeleteSchedule(r.Context(), orgID, scheduleID); err == discord.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("schedule not found"))
		return
	} else if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// ---------------------------------------------------------------------------
// job await (console snapshot path)
// ---------------------------------------------------------------------------

func (h *botHandler) awaitJob(ctx context.Context, jobID uuid.UUID, timeout time.Duration) (json.RawMessage, error) {
	deadline := time.Now().Add(timeout)
	for {
		job, err := h.srv.Jobs.GetByID(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if job.Status == jobs.StatusSuccess {
			return job.Result, nil
		}
		if job.Status == jobs.StatusFailed {
			return nil, &jobFailedError{msg: job.Error}
		}
		if time.Now().After(deadline) {
			return nil, context.DeadlineExceeded
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

type jobFailedError struct{ msg string }

func (e *jobFailedError) Error() string { return "agent job failed: " + e.msg }

// ---------------------------------------------------------------------------
// jobs store extensions (bot linkage) — kept in THIS file because the jobs
// package is platform-shared and Phase 8 must not edit it.
// ---------------------------------------------------------------------------

// botEnqueueFunc is the enqueue seam (test-injectable).
var botEnqueue = func(ctx context.Context, js *jobs.Store, serverID, botID uuid.UUID, jobType jobs.Type, payload any) (*jobs.Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	row := js.Pool.QueryRow(ctx, `
		INSERT INTO jobs (server_id, bot_id, type, payload)
		VALUES ($1, $2, $3, $4)
		RETURNING `+jobColsExpr, serverID, botID, jobType, payloadJSON)
	return scanJobRow(row)
}

const jobColsExpr = `id, server_id, website_id, bot_id, type, status, payload, result, error, progress, progress_step, attempts, max_attempts, idempotency_key, claimed_at, lease_expires_at, finished_at, created_at`

type botJobRow struct {
	jobs.Job
	BotID *uuid.UUID `json:"bot_id,omitempty"`
}

func scanJobRow(row interface{ Scan(dest ...any) error }) (*jobs.Job, error) {
	var j jobs.Job
	var botID *uuid.UUID
	if err := row.Scan(&j.ID, &j.ServerID, &j.WebsiteID, &botID, &j.Type, &j.Status, &j.Payload, &j.Result,
		&j.Error, &j.Progress, &j.ProgressStep, &j.Attempts, &j.MaxAttempts, &j.IdempotencyKey,
		&j.ClaimedAt, &j.LeaseExpiresAt, &j.FinishedAt, &j.CreatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// botEnqueueIdempotent collapses concurrent duplicates (crash recovery,
// scheduled restarts) onto one pending/running job.
var botEnqueueIdempotent = func(ctx context.Context, js *jobs.Store, serverID, botID uuid.UUID, jobType jobs.Type, payload any, key string) (*jobs.Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	row := js.Pool.QueryRow(ctx, `
		INSERT INTO jobs (server_id, bot_id, type, payload, idempotency_key)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL AND status IN ('pending', 'running')
		DO UPDATE SET updated_at = now()
		RETURNING `+jobColsExpr, serverID, botID, jobType, payloadJSON, key)
	return scanJobRow(row)
}

func botListRecent(ctx context.Context, js *jobs.Store, botID uuid.UUID, limit int, onlyBot bool) ([]jobs.Job, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := `SELECT ` + jobColsExpr + ` FROM jobs WHERE bot_id = $1 ORDER BY created_at DESC LIMIT $2`
	rows, err := js.Pool.Query(ctx, q, botID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jobs.Job
	for rows.Next() {
		j, err := scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

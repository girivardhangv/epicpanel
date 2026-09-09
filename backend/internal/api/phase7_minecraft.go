package api

// ============================================================================
// Phase 7 — Minecraft hosting API (registered via registerPhase7).
//
// Every route is org-scoped server-side (ResolveOrg): cross-tenant access is
// 404, mutations require developer+, reads require billing+. The RCON
// password is write-only (ciphertext at rest, never in any response); the
// console pipeline scrubs it before bytes leave the server. Jobs are
// idempotent and the instance state machine only advances on AGENT truth —
// an accepted request is never success.
// ============================================================================

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/minecraft"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
)

// registerPhase7 mounts the Minecraft routes. The coordinator adds the
// single call line in server.go.
func registerPhase7(s *Server, mux *http.ServeMux) {
	srvH := &servers.Handler{Orgs: s.Orgs}
	h := &mcHandler{srv: s, mc: &minecraft.Store{Pool: s.Pool}, requireOrg: srvH.ResolveOrg}

	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft", h.wrap(organizations.RoleBilling, h.list))
	mux.HandleFunc("POST /v1/organizations/{org_id}/minecraft", h.wrap(organizations.RoleDeveloper, h.create))
	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/provider-offers", h.wrap(organizations.RoleBilling, h.providerOffers))
	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/{instance_id}", h.wrap(organizations.RoleBilling, h.get))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/minecraft/{instance_id}", h.wrap(organizations.RoleDeveloper, h.update))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/minecraft/{instance_id}", h.wrap(organizations.RoleDeveloper, h.delete))

	for _, action := range []string{"start", "stop", "restart", "kill"} {
		mux.HandleFunc("POST /v1/organizations/{org_id}/minecraft/{instance_id}/"+action,
			h.wrap(organizations.RoleDeveloper, h.lifecycle(action)))
	}

	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/{instance_id}/console", h.wrap(organizations.RoleBilling, h.consoleTail))
	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/{instance_id}/console/ws", h.wrap(organizations.RoleBilling, h.consoleWS))
	mux.HandleFunc("POST /v1/organizations/{org_id}/minecraft/{instance_id}/console/command", h.wrap(organizations.RoleDeveloper, h.consoleCommand))

	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/{instance_id}/files", h.wrap(organizations.RoleDeveloper, h.listFiles))
	mux.HandleFunc("POST /v1/organizations/{org_id}/minecraft/{instance_id}/files/upload", h.wrap(organizations.RoleDeveloper, h.uploadFile))

	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/{instance_id}/properties", h.wrap(organizations.RoleBilling, h.getProperties))
	mux.HandleFunc("PUT /v1/organizations/{org_id}/minecraft/{instance_id}/properties", h.wrap(organizations.RoleDeveloper, h.putProperties))

	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/{instance_id}/metrics", h.wrap(organizations.RoleBilling, h.metrics))
	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/{instance_id}/jobs", h.wrap(organizations.RoleBilling, h.instanceJobs))

	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/{instance_id}/schedules", h.wrap(organizations.RoleBilling, h.listSchedules))
	mux.HandleFunc("POST /v1/organizations/{org_id}/minecraft/{instance_id}/schedules", h.wrap(organizations.RoleDeveloper, h.createSchedule))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/minecraft/{instance_id}/schedules/{schedule_id}", h.wrap(organizations.RoleDeveloper, h.deleteSchedule))

	mux.HandleFunc("GET /v1/organizations/{org_id}/minecraft/{instance_id}/backups", h.wrap(organizations.RoleBilling, h.listBackups))
	mux.HandleFunc("POST /v1/organizations/{org_id}/minecraft/{instance_id}/backups", h.wrap(organizations.RoleDeveloper, h.createBackup))
	mux.HandleFunc("POST /v1/organizations/{org_id}/minecraft/{instance_id}/backups/{backup_id}/restore", h.wrap(organizations.RoleDeveloper, h.restoreBackup))

	// Control-plane loops: reconciliation + schedule sweeper (once).
	s.startMCLoops()
}

// ---------------------------------------------------------------------------
// handler plumbing
// ---------------------------------------------------------------------------

type mcHandler struct {
	srv        *Server
	mc         *minecraft.Store
	requireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *mcHandler) wrap(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
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

func (h *mcHandler) org(r *http.Request) uuid.UUID {
	id, _ := uuid.Parse(r.PathValue("org_id"))
	return id
}

func (h *mcHandler) audit(r *http.Request, orgID uuid.UUID, action string, instanceID uuid.UUID, meta map[string]any) {
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
		ResourceType:   "minecraft_instance",
		ResourceID:     instanceID.String(),
		Metadata:       meta,
	})
}

func (h *mcHandler) actorID(r *http.Request) uuid.UUID {
	user, _ := httpapi.UserFrom(r.Context())
	if user == nil {
		return uuid.Nil
	}
	uid, _ := uuid.Parse(user.ID)
	return uid
}

func (h *mcHandler) instanceFromPath(r *http.Request, orgID uuid.UUID) (*minecraft.Instance, *httpapi.APIError) {
	id, err := uuid.Parse(r.PathValue("instance_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid instance id")
	}
	inst, err := h.mc.GetByID(r.Context(), orgID, id)
	if err == minecraft.ErrNotFound {
		return nil, httpapi.ErrNotFound("minecraft instance not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return inst, nil
}

func (h *mcHandler) rowFromPath(r *http.Request, orgID uuid.UUID) (*minecraft.Row, *httpapi.APIError) {
	id, err := uuid.Parse(r.PathValue("instance_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid instance id")
	}
	row, err := h.mc.GetRow(r.Context(), orgID, id)
	if err == minecraft.ErrNotFound {
		return nil, httpapi.ErrNotFound("minecraft instance not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return row, nil
}

// enqueue links the job to the instance (minecraft_id) for per-instance lists.
func (h *mcHandler) enqueue(ctx context.Context, serverID, instanceID uuid.UUID, jobType jobs.Type, payload any) (*jobs.Job, error) {
	return mcEnqueue(ctx, h.srv.Jobs, serverID, instanceID, jobType, payload)
}

func (h *mcHandler) publish(r *http.Request, orgID uuid.UUID, eventType string, instanceID uuid.UUID, payload map[string]any) {
	if h.srv.Events == nil {
		return
	}
	org := orgID
	h.srv.Events.Publish(r.Context(), events.Event{
		Type:         eventType,
		Organization: &org,
		ResourceType: "minecraft_instance",
		ResourceID:   instanceID.String(),
		Payload:      payload,
	})
}

// ---------------------------------------------------------------------------
// CRUD
// ---------------------------------------------------------------------------

type createMCRequest struct {
	Name          string            `json:"name"`
	Provider      string            `json:"provider"`
	Version       string            `json:"version"`
	Properties    map[string]string `json:"properties"`
	MaxPlayers    int               `json:"max_players"`
	AcceptEULA    bool              `json:"accept_eula"`
	RestartPolicy string            `json:"restart_policy"`
	MaxRestarts   int               `json:"max_restarts"`
	ServerID      string            `json:"server_id"`
}

func (h *mcHandler) create(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	var req createMCRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	// Phase 9 create-time gate: the org's plan must be a Minecraft plan and
	// the port count must fit. Fail closed when the plan cannot be resolved.
	if h.srv.ResourceLimits == nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("plan limits unavailable; minecraft creation blocked"))
		return
	}
	plan, err := h.srv.ResourceLimits.PlanForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("plan limits unavailable; minecraft creation blocked"))
		return
	}
	if plan.Kind != string(resources.KindMinecraft) {
		httpapi.RespondError(w, httpapi.ErrForbidden("the organization plan does not include Minecraft hosting"))
		return
	}
	limits, err := h.srv.ResourceLimits.LimitsForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden("plan limits unavailable; minecraft creation blocked"))
		return
	}

	// Validation before gates: user-fixable input errors win over limit
	// errors (a 422 says what to fix; a 409/403 about limits is next).
	if !req.AcceptEULA {
		httpapi.RespondError(w, httpapi.ErrValidation("the Minecraft EULA must be accepted (accept_eula)"))
		return
	}
	if !minecraft.ValidInstanceName(req.Name) {
		httpapi.RespondError(w, httpapi.ErrValidation("instance name must be lowercase letters, digits, - and _ (2-63 chars, alnum edges)"))
		return
	}
	if req.Provider == "" {
		req.Provider = "vanilla"
	}
	if req.Version == "" {
		for _, o := range minecraft.ProviderOffers() {
			if o.Provider == req.Provider && o.Default != "" {
				req.Version = o.Default
				break
			}
		}
	}
	javaMajor, verr := minecraft.ValidateProviderVersion(req.Provider, req.Version)
	if verr != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(verr.Error()))
		return
	}
	props, verr := minecraft.SanitizeProperties(req.Properties)
	if verr != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(verr.Error()))
		return
	}
	if req.RestartPolicy == "" {
		req.RestartPolicy = "on-failure"
	}
	switch req.RestartPolicy {
	case "on-failure", "always", "no":
	default:
		httpapi.RespondError(w, httpapi.ErrValidation("restart_policy must be on-failure, always or no"))
		return
	}

	count, err := h.mc.CountForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// One externally-listening game port per instance — the Ports count is
	// the plan's governed number for it.
	if err := resources.CheckCount(limits, resources.ResPorts, count); err != nil {
		httpapi.RespondError(w, httpapi.ErrForbidden(err.Error()))
		return
	}

	serverID, apiErr := h.resolveServer(r, req.ServerID, javaMajor)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	// Port allocation via internal/ports (DB-driven used set).
	gamePort, rconPort, apiErr := h.allocatePorts(r, serverID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	rconPass, err := minecraft.GenerateRCONPassword()
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	rconEnc, err := minecraft.EncryptRCONPassword(rconPass)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	propsJSON, err := minecraft.MarshalProps(props)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	inst, err := h.mc.Create(r.Context(), orgID, serverID, h.actorID(r), req.Name,
		req.Provider, req.Version, javaMajor, gamePort, rconPort,
		minecraft.XmxForPlan(int64(plan.MemoryLimitMB)), req.RestartPolicy, req.MaxRestarts,
		[]byte(propsJSON), rconEnc)
	if err == minecraft.ErrNameTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("a minecraft instance with this name already exists"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	payload := minecraft.MCInstallPayload{
		InstanceID: inst.ID.String(), Provider: inst.Provider, Version: inst.Version,
		JavaMajor: inst.JavaMajor, Port: inst.Port, RCONPort: inst.RCONPort,
		RCONPassEnc: rconEnc, XmxMB: inst.XmxMB, EULA: true, Properties: props,
	}
	if _, err := h.enqueue(r.Context(), serverID, inst.ID, minecraft.JobMCInstall, payload); err != nil {
		_ = h.mc.MarkFailed(r.Context(), inst.ID, "install enqueue failed")
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.publish(r, orgID, minecraft.EventMCCreated, inst.ID, map[string]any{"name": inst.Name, "provider": inst.Provider})
	h.audit(r, orgID, "minecraft.created", inst.ID, map[string]any{"provider": inst.Provider, "version": inst.Version})
	httpapi.WriteJSON(w, http.StatusCreated, h.instanceView(r, inst))
}

func (h *mcHandler) resolveServer(r *http.Request, serverIDParam string, javaMajor int) (uuid.UUID, *httpapi.APIError) {
	if serverIDParam != "" {
		id, err := uuid.Parse(serverIDParam)
		if err != nil {
			return uuid.Nil, httpapi.ErrValidation("invalid server_id")
		}
		if _, err := h.srv.Servers.GetByID(r.Context(), id); err != nil {
			return uuid.Nil, httpapi.ErrNotFound("server not found")
		}
		return id, nil
	}
	// Prefer a server that already carries the Java major this provider
	// needs ("minecraft" is NOT a runtime type — the old call here matched
	// nothing and dead-ended every auto-placement with "no server available").
	id, err := h.srv.Servers.AutoPickServer(r.Context(), "java", strconv.Itoa(javaMajor))
	if err != nil {
		// No exact-Java server: any online server still works — the agent
		// auto-installs the required Java during provisioning.
		id, err = h.srv.Servers.AutoPickServer(r.Context(), "", "")
		if err != nil {
			return uuid.Nil, httpapi.ErrValidation("no server available; enroll a server or pick one explicitly")
		}
	}
	return id, nil
}

// allocatePorts picks the game + RCON ports from the Minecraft range
// (internal/ports helpers over the DB used set).
func (h *mcHandler) allocatePorts(r *http.Request, serverID uuid.UUID) (int, int, *httpapi.APIError) {
	rows, err := h.srv.Pool.Query(r.Context(), `
		SELECT port, rcon_port FROM minecraft_instances
		WHERE server_id = $1 AND status <> 'deleted'`, serverID)
	if err != nil {
		return 0, 0, httpapi.ErrInternal(err)
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var g, rc int
		if err := rows.Scan(&g, &rc); err == nil {
			used[g] = true
			used[rc] = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, httpapi.ErrInternal(err)
	}
	gamePort, ok := minecraft.AllocPort(used)
	if !ok {
		return 0, 0, httpapi.ErrForbidden("no free Minecraft ports left on this server")
	}
	used[gamePort] = true
	rconPort, ok := minecraft.AllocRCONPort(used)
	if !ok {
		return 0, 0, httpapi.ErrForbidden("no free RCON ports left on this server")
	}
	return gamePort, rconPort, nil
}

func (h *mcHandler) list(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	list, err := h.mc.ListForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, h.instanceView(r, &list[i]))
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"instances": out})
}

func (h *mcHandler) get(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, h.instanceView(r, inst))
}

// instanceView projects an instance + live metrics (LIVE/STALE/OFFLINE +
// age). The RCON password NEVER rides along.
func (h *mcHandler) instanceView(r *http.Request, inst *minecraft.Instance) map[string]any {
	out := map[string]any{
		"id":              inst.ID,
		"organization_id": inst.OrgID,
		"server_id":       inst.ServerID,
		"name":            inst.Name,
		"provider":        inst.Provider,
		"version":         inst.Version,
		"java_major":      inst.JavaMajor,
		"status":          inst.Status,
		"desired_state":   inst.DesiredState,
		"port":            inst.Port,
		"xmx_mb":          inst.XmxMB,
		"extra_args":      inst.ExtraArgs,
		"restart_policy":  inst.RestartPolicy,
		"max_restarts":    inst.MaxRestarts,
		"restart_count":   inst.RestartCount,
		"property_keys":   inst.PropertyKeys,
		"last_error":      inst.LastError,
		"unit_state":      inst.UnitState,
		"agent_seen_at":   inst.AgentSeenAt,
		"created_at":      inst.CreatedAt,
		"updated_at":      inst.UpdatedAt,
	}
	if m, ok := h.liveMetrics(inst); ok {
		out["metrics"] = m
	}
	return out
}

// liveMetrics reads the Phase 3 live store (WS-streamed samples) for the
// instance's minecraft envelope. Honesty: TPS/MSPT carry a per-provider
// source note — a vanilla server never exposes TPS over RCON.
func (h *mcHandler) liveMetrics(inst *minecraft.Instance) (map[string]any, bool) {
	if h.srv.LiveStore == nil {
		return nil, false
	}
	frame := h.srv.LiveStore.Frame(inst.ServerID)
	if frame == nil {
		return map[string]any{"freshness": map[string]any{"state": "OFFLINE", "age_ms": -1}, "kind": "minecraft"}, true
	}
	for _, app := range frame.Apps {
		if app.WebsiteID == inst.ID.String() {
			tpsSource := "unavailable"
			if app.TPS > 0 {
				tpsSource = "rcon"
			} else if p, err := minecraft.ProviderFor(inst.Provider); err == nil {
				if p.SupportsRCONTPS() {
					tpsSource = "waiting"
				} else {
					tpsSource = "unsupported"
				}
			}
			return map[string]any{
				"kind":         "minecraft",
				"status":       app.Status,
				"cpu_percent":  app.CPUPercent,
				"memory_bytes": app.MemoryBytes,
				"net_rx_bps":   app.NetRxBPS,
				"net_tx_bps":   app.NetTxBPS,
				"disk_used_mb": app.DiskUsedMB,
				"uptime_s":     app.UptimeS,
				"players":      app.Players,
				"tps":          app.TPS,
				"mspt":         app.MSPT,
				"tps_source":   tpsSource,
				"freshness":    frame.Freshness,
			}, true
		}
	}
	return map[string]any{"freshness": frame.Freshness, "kind": "minecraft"}, true
}

type updateMCRequest struct {
	Version    string            `json:"version"`
	Properties map[string]string `json:"properties"`
	XmxMB      int64             `json:"xmx_mb"`
	ExtraArgs  []string          `json:"extra_args"`
}

func (h *mcHandler) update(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	row, apiErr := h.rowFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req updateMCRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	version := req.Version
	if version == "" {
		version = row.Version
	}
	javaMajor := row.JavaMajor
	if version != row.Version {
		jm, verr := minecraft.ValidateProviderVersion(row.Provider, version)
		if verr != nil {
			httpapi.RespondError(w, httpapi.ErrValidation(verr.Error()))
			return
		}
		javaMajor = jm
	}
	if req.XmxMB > 0 && req.XmxMB < minecraft.MinXmxMB {
		httpapi.RespondError(w, httpapi.ErrValidation("xmx_mb is below the minimum heap ("+strconv.Itoa(minecraft.MinXmxMB)+" MB)"))
		return
	}
	if len(req.ExtraArgs) > 16 {
		httpapi.RespondError(w, httpapi.ErrValidation("too many extra args (max 16)"))
		return
	}
	for _, a := range req.ExtraArgs {
		if strings_ConservativeArg(a) != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid extra arg (no whitespace or control characters)"))
			return
		}
	}
	xmx := row.XmxMB
	if req.XmxMB > 0 {
		xmx = req.XmxMB
	}

	// Merge properties (protected keys rejected; existing values preserved).
	props, err := h.currentProps(r, row)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if req.Properties != nil {
		clean, verr := minecraft.SanitizeProperties(req.Properties)
		if verr != nil {
			httpapi.RespondError(w, httpapi.ErrValidation(verr.Error()))
			return
		}
		for k, v := range clean {
			props[k] = v
		}
	}
	propsJSON, err := minecraft.MarshalProps(props)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.mc.UpdateConfig(r.Context(), row.ID, version, javaMajor, xmx, req.ExtraArgs, []byte(propsJSON)); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "minecraft.updated", row.ID, nil)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "updated (applies at next start/restart)"})
}

// strings_ConservativeArg validates one extra JVM arg (single token, no
// control characters, bounded size). Returns nil when valid.
func strings_ConservativeArg(a string) error {
	if a == "" || len(a) > 200 {
		return errExtraArg
	}
	for _, c := range a {
		if c < 0x20 || c == 0x7f {
			return errExtraArg
		}
	}
	return nil
}

type simpleError string

func (e simpleError) Error() string { return string(e) }

const errExtraArg = simpleError("invalid extra arg")

// currentProps fetches the stored properties map for an instance row.
func (h *mcHandler) currentProps(r *http.Request, row *minecraft.Row) (map[string]string, error) {
	var raw []byte
	err := h.srv.Pool.QueryRow(r.Context(),
		`SELECT properties FROM minecraft_instances WHERE id = $1`, row.ID).Scan(&raw)
	if err != nil {
		return map[string]string{}, err
	}
	props := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &props); err != nil {
			return map[string]string{}, nil
		}
	}
	return props, nil
}

func (h *mcHandler) delete(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if !(minecraft.LifecycleGuard{}).CanDelete(inst.Status) {
		httpapi.RespondError(w, httpapi.ErrConflict("instance in state "+string(inst.Status)+" cannot be deleted"))
		return
	}
	if err := h.mc.MarkDeleting(r.Context(), inst.ID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if _, err := h.enqueue(r.Context(), inst.ServerID, inst.ID, minecraft.JobMCDelete, minecraft.MCIDPayload{InstanceID: inst.ID.String()}); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.publish(r, orgID, minecraft.EventMCDeleted, inst.ID, nil)
	h.audit(r, orgID, "minecraft.deleted", inst.ID, nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "deletion queued"})
}

// ---------------------------------------------------------------------------
// lifecycle
// ---------------------------------------------------------------------------

// lifecycle returns the start/stop/restart/kill handler. Jobs are enqueued
// idempotently; the DB status moves to starting/stopping and ONLY agent
// truth (via the reconciliation loop) promotes it further.
func (h *mcHandler) lifecycle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		orgID := h.org(r)
		inst, apiErr := h.instanceFromPath(r, orgID)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		ctx := r.Context()
		var jobType jobs.Type
		var event string
		var payload any
		switch action {
		case "start", "restart":
			if action == "start" {
				if !(minecraft.LifecycleGuard{}).CanStart(inst.Status) {
					httpapi.RespondError(w, httpapi.ErrConflict("instance in state "+string(inst.Status)+" cannot start"))
					return
				}
			}
			row, err := h.mc.GetRow(ctx, orgID, inst.ID)
			if err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
			if err := h.mc.MarkStarting(ctx, inst.ID); err != nil {
				httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
				return
			}
			// The customer wants it running; reconciliation converges to
			// agent truth against this target.
			_ = h.mc.SetDesiredState(ctx, inst.ID, minecraft.DesiredRunning)
			jobType = minecraft.JobMCStart
			if action == "restart" {
				jobType = minecraft.JobMCRestart
			}
			payload = h.lifecyclePayload(row)
			event = minecraft.EventMCStarting
		case "stop", "kill":
			if !(minecraft.LifecycleGuard{}).CanStop(inst.Status) {
				httpapi.RespondError(w, httpapi.ErrConflict("instance in state "+string(inst.Status)+" cannot stop"))
				return
			}
			if err := h.mc.MarkStopping(ctx, inst.ID); err != nil {
				httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
				return
			}
			_ = h.mc.SetDesiredState(ctx, inst.ID, minecraft.DesiredStopped)
			jobType = minecraft.JobMCStop
			if action == "kill" {
				jobType = minecraft.JobMCKill
			}
			payload = minecraft.MCIDPayload{InstanceID: inst.ID.String()}
			event = minecraft.EventMCStopping
		default:
			httpapi.RespondError(w, httpapi.ErrValidation("unknown action"))
			return
		}
		if _, err := h.enqueue(ctx, inst.ServerID, inst.ID, jobType, payload); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		h.publish(r, orgID, event, inst.ID, nil)
		h.audit(r, orgID, "minecraft."+action, inst.ID, nil)
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": action + " queued", "job_type": jobType})
	}
}

// lifecyclePayload assembles the start/restart job body from the stored
// row. The RCON password rides as ciphertext only; limits come from the
// Phase 9 engine — the SAME numbers the usage bars display (anti-drift rule).
func (h *mcHandler) lifecyclePayload(row *minecraft.Row) minecraft.MCLifecyclePayload {
	return minecraft.MCLifecyclePayload{
		InstanceID:    row.ID.String(),
		Provider:      row.Provider,
		Version:       row.Version,
		JavaMajor:     row.JavaMajor,
		Port:          row.Port,
		RCONPort:      row.RCONPort,
		RCONPassEnc:   row.RCONPassEnc,
		XmxMB:         row.XmxMB,
		ExtraArgs:     row.ExtraArgs,
		RestartPolicy: row.RestartPolicy,
		Limits:        h.srv.mcLimitsForOrg(row.OrgID),
	}
}

// mcLifecyclePayload rebuilds the start payload from the stored row (loops).
func (s *Server) mcLifecyclePayload(row *minecraft.Row) minecraft.MCLifecyclePayload {
	return minecraft.MCLifecyclePayload{
		InstanceID:    row.ID.String(),
		Provider:      row.Provider,
		Version:       row.Version,
		JavaMajor:     row.JavaMajor,
		Port:          row.Port,
		RCONPort:      row.RCONPort,
		RCONPassEnc:   row.RCONPassEnc,
		XmxMB:         row.XmxMB,
		ExtraArgs:     row.ExtraArgs,
		RestartPolicy: row.RestartPolicy,
		Limits:        s.mcLimitsForOrg(row.OrgID),
	}
}

// mcLimitsForOrg resolves the Phase 9 engine numbers for a unit.
func (s *Server) mcLimitsForOrg(orgID uuid.UUID) minecraft.MCLimitsPayload {
	var out minecraft.MCLimitsPayload
	if s.ResourceLimits == nil {
		return out
	}
	limits, err := s.ResourceLimits.LimitsForOrg(context.Background(), orgID)
	if err != nil {
		return out
	}
	if r, ok := limits.Get(resources.ResCPU); ok && r.Limit > 0 {
		out.CPUPercent = r.Limit
	}
	if r, ok := limits.Get(resources.ResRAM); ok && r.Limit > 0 {
		out.MemoryMB = int64(r.Limit)
	}
	if r, ok := limits.Get(resources.ResDisk); ok && r.Limit > 0 {
		out.DiskMB = int64(r.Limit)
	}
	if r, ok := limits.Get(resources.ResProcesses); ok && r.Limit > 0 {
		out.PidsMax = int64(r.Limit)
	}
	return out
}

// ---------------------------------------------------------------------------
// console + commands (controlled interface — allowlist, never shell)
// ---------------------------------------------------------------------------

func (h *mcHandler) consoleTail(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
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
	out, err := h.runLogsJob(r, inst, lines, 0)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// Defense in depth: scrub once more at the edge against the stored
	// RCON password.
	if row, rerr := h.mc.GetRow(r.Context(), orgID, inst.ID); rerr == nil {
		if pw, derr := minecraft.DecryptRCONPassword(row.RCONPassEnc); derr == nil {
			secrets := minecraft.SecretValues(pw)
			for i := range out.Lines {
				out.Lines[i].Text = minecraft.ScrubText(out.Lines[i].Text, secrets)
			}
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

// consoleWS upgrades to the live tail (minecraft.ServeConsoleWS hub). Input
// is discarded by design — commands go through the REST allowlist only.
func (h *mcHandler) consoleWS(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	minecraft.ServeConsoleWS(w, r, inst.ID)
}

type mcCommandRequest struct {
	Command string `json:"command"`
}

func (h *mcHandler) consoleCommand(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req mcCommandRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	// Allowlist enforcement happens HERE (before any queueing) and AGAIN on
	// the agent. Non-allowlisted input never reaches the node.
	cmd, err := minecraft.ValidateConsoleCommand(req.Command)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	if _, err := h.enqueue(r.Context(), inst.ServerID, inst.ID, minecraft.JobMCCommand,
		minecraft.MCCommandPayload{InstanceID: inst.ID.String(), Command: cmd}); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "minecraft.command", inst.ID, map[string]any{"command": cmd})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "command queued", "command": cmd})
}

// ---------------------------------------------------------------------------
// files + properties
// ---------------------------------------------------------------------------

type mcUploadRequest struct {
	Path       string `json:"path"`
	ContentB64 string `json:"content_b64"`
}

func (h *mcHandler) uploadFile(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req mcUploadRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if req.Path == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("path is required"))
		return
	}
	payload := minecraft.MCUploadPayload{InstanceID: inst.ID.String(), Path: req.Path, B64: req.ContentB64}
	if _, err := h.enqueue(r.Context(), inst.ServerID, inst.ID, minecraft.JobMCUpload, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "minecraft.file_uploaded", inst.ID, map[string]any{"path": req.Path})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "upload queued", "path": req.Path})
}

func (h *mcHandler) listFiles(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	payload := minecraft.MCFilesPayload{InstanceID: inst.ID.String(), Path: r.URL.Query().Get("path")}
	job, err := h.enqueue(r.Context(), inst.ServerID, inst.ID, minecraft.JobMCFiles, payload)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	result, err := h.awaitJob(r.Context(), job.ID, 15*time.Second)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	var out minecraft.MCFilesOutcome
	if err := json.Unmarshal(result, &out); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (h *mcHandler) getProperties(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	payload := minecraft.MCPropertiesPayload{InstanceID: inst.ID.String(), Action: "get"}
	job, err := h.enqueue(r.Context(), inst.ServerID, inst.ID, minecraft.JobMCProperties, payload)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	result, err := h.awaitJob(r.Context(), job.ID, 15*time.Second)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	var out minecraft.MCPropertiesOutcome
	if err := json.Unmarshal(result, &out); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

type putPropertiesRequest struct {
	Properties map[string]string `json:"properties"`
}

func (h *mcHandler) putProperties(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	row, apiErr := h.rowFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req putPropertiesRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	clean, verr := minecraft.SanitizeProperties(req.Properties)
	if verr != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(verr.Error()))
		return
	}
	props, err := h.currentProps(r, row)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	for k, v := range clean {
		props[k] = v
	}
	propsJSON, err := minecraft.MarshalProps(props)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.mc.UpdateConfig(r.Context(), row.ID, row.Version, row.JavaMajor, row.XmxMB, row.ExtraArgs, []byte(propsJSON)); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	payload := minecraft.MCPropertiesPayload{InstanceID: row.ID.String(), Action: "set", Properties: clean}
	if _, err := h.enqueue(r.Context(), row.ServerID, row.ID, minecraft.JobMCProperties, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "minecraft.properties_updated", row.ID, map[string]any{"count": len(clean)})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "properties queued for write (applies at next restart for most keys)"})
}

// ---------------------------------------------------------------------------
// metrics + jobs + provider offers
// ---------------------------------------------------------------------------

func (h *mcHandler) metrics(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	m, _ := h.liveMetrics(inst)
	httpapi.WriteJSON(w, http.StatusOK, m)
}

func (h *mcHandler) instanceJobs(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
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
	list, err := mcListRecent(r.Context(), h.srv.Jobs, inst.ID, limit)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []jobs.Job{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": list})
}

func (h *mcHandler) providerOffers(w http.ResponseWriter, r *http.Request) {
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"providers": minecraft.ProviderOffers()})
}

// ---------------------------------------------------------------------------
// schedules (restart/start/stop/command — commands must pass the allowlist)
// ---------------------------------------------------------------------------

type createMCScheduleRequest struct {
	Kind    string `json:"kind"`
	Cron    string `json:"cron"`
	Command string `json:"command"`
}

func (h *mcHandler) listSchedules(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	list, err := h.mc.ListSchedules(r.Context(), inst.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"schedules": list})
}

func (h *mcHandler) createSchedule(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req createMCScheduleRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	command := ""
	switch req.Kind {
	case "restart", "start", "stop":
	case "command":
		// Scheduled CUSTOM commands would be shell by another name — only
		// allowlisted console commands are schedulable.
		cmd, err := minecraft.ValidateConsoleCommand(req.Command)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
			return
		}
		command = cmd
	default:
		httpapi.RespondError(w, httpapi.ErrValidation("kind must be restart, start, stop or command (allowlisted)"))
		return
	}
	interval, err := minecraft.ParseCron(req.Cron)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	created, err := h.mc.CreateSchedule(r.Context(), inst.ID, req.Kind, req.Cron, command, interval)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "minecraft.schedule_created", inst.ID, map[string]any{"kind": req.Kind, "cron": req.Cron})
	httpapi.WriteJSON(w, http.StatusCreated, created)
}

func (h *mcHandler) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	if _, apiErr := h.instanceFromPath(r, orgID); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	scheduleID, err := uuid.Parse(r.PathValue("schedule_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid schedule id"))
		return
	}
	if err := h.mc.DeleteSchedule(r.Context(), orgID, scheduleID); err == minecraft.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("schedule not found"))
		return
	} else if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// ---------------------------------------------------------------------------
// world backups (Phase 11 seam: world-scoped agent ops)
// ---------------------------------------------------------------------------

type mcBackupRequest struct {
	Name string `json:"name"`
}

func (h *mcHandler) createBackup(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req mcBackupRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if req.Name == "" {
		req.Name = "world-" + time.Now().UTC().Format("20060102-150405")
	}
	if _, err := h.enqueue(r.Context(), inst.ServerID, inst.ID, minecraft.JobMCBackupWorld,
		minecraft.MCBackupPayload{InstanceID: inst.ID.String(), BackupName: req.Name}); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "minecraft.backup_queued", inst.ID, map[string]any{"name": req.Name})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "backup queued", "name": req.Name})
}

func (h *mcHandler) listBackups(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	list, err := h.mc.ListWorldBackups(r.Context(), inst.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []minecraft.WorldBackup{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"backups": list})
}

func (h *mcHandler) restoreBackup(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	inst, apiErr := h.instanceFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	backupID, err := uuid.Parse(r.PathValue("backup_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid backup id"))
		return
	}
	b, err := h.mc.GetWorldBackup(r.Context(), orgID, backupID)
	if err == minecraft.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("backup not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	row, err := h.mc.GetRow(r.Context(), orgID, inst.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	payload := minecraft.MCRestorePayload{
		InstanceID: inst.ID.String(), BackupName: b.Name,
		Provider: row.Provider, Version: row.Version, JavaMajor: row.JavaMajor,
		Port: row.Port, RCONPort: row.RCONPort, RCONPassEnc: row.RCONPassEnc,
		XmxMB: row.XmxMB, RestartPolicy: row.RestartPolicy,
	}
	if _, err := h.enqueue(r.Context(), inst.ServerID, inst.ID, minecraft.JobMCRestoreWorld, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "minecraft.restore_queued", inst.ID, map[string]any{"backup": b.Name})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "restore queued (instance will be stopped)"})
}

// ---------------------------------------------------------------------------
// job await + store extensions (instance linkage) — kept in THIS file
// because the jobs package is platform-shared and Phase 7 must not edit it.
// ---------------------------------------------------------------------------

func (h *mcHandler) awaitJob(ctx context.Context, jobID uuid.UUID, timeout time.Duration) (json.RawMessage, error) {
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
			return nil, &mcJobFailedError{msg: job.Error}
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

type mcJobFailedError struct{ msg string }

func (e *mcJobFailedError) Error() string { return "agent job failed: " + e.msg }

// mcEnqueue is the enqueue seam (test-injectable).
var mcEnqueue = func(ctx context.Context, js *jobs.Store, serverID, instanceID uuid.UUID, jobType jobs.Type, payload any) (*jobs.Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	row := js.Pool.QueryRow(ctx, `
		INSERT INTO jobs (server_id, minecraft_id, type, payload)
		VALUES ($1, $2, $3, $4)
		RETURNING `+mcJobColsExpr, serverID, instanceID, jobType, payloadJSON)
	return scanMCJobRow(row)
}

const mcJobColsExpr = `id, server_id, website_id, bot_id, minecraft_id, type, status, payload, result, error, progress, progress_step, attempts, max_attempts, idempotency_key, claimed_at, lease_expires_at, finished_at, created_at`

func scanMCJobRow(row interface{ Scan(dest ...any) error }) (*jobs.Job, error) {
	var j jobs.Job
	var botID, mcID *uuid.UUID
	if err := row.Scan(&j.ID, &j.ServerID, &j.WebsiteID, &botID, &mcID, &j.Type, &j.Status, &j.Payload, &j.Result,
		&j.Error, &j.Progress, &j.ProgressStep, &j.Attempts, &j.MaxAttempts, &j.IdempotencyKey,
		&j.ClaimedAt, &j.LeaseExpiresAt, &j.FinishedAt, &j.CreatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// mcEnqueueIdempotent collapses concurrent duplicates (crash recovery,
// scheduled restarts) onto one pending/running job.
var mcEnqueueIdempotent = func(ctx context.Context, js *jobs.Store, serverID, instanceID uuid.UUID, jobType jobs.Type, payload any, key string) (*jobs.Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	row := js.Pool.QueryRow(ctx, `
		INSERT INTO jobs (server_id, minecraft_id, type, payload, idempotency_key)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL AND status IN ('pending', 'running')
		DO UPDATE SET updated_at = now()
		RETURNING `+mcJobColsExpr, serverID, instanceID, jobType, payloadJSON, key)
	return scanMCJobRow(row)
}

func mcListRecent(ctx context.Context, js *jobs.Store, instanceID uuid.UUID, limit int) ([]jobs.Job, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := `SELECT ` + mcJobColsExpr + ` FROM jobs WHERE minecraft_id = $1 ORDER BY created_at DESC LIMIT $2`
	rows, err := js.Pool.Query(ctx, q, instanceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jobs.Job
	for rows.Next() {
		j, err := scanMCJobRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// runLogsJob enqueues mc_logs and waits (server-side, bounded) for the
// agent result.
func (h *mcHandler) runLogsJob(r *http.Request, inst *minecraft.Instance, lines int, afterSeq int64) (*minecraft.MCLogsOutcome, error) {
	payload := minecraft.MCLogsPayload{InstanceID: inst.ID.String(), Lines: lines, AfterSeq: afterSeq}
	job, err := h.enqueue(r.Context(), inst.ServerID, inst.ID, minecraft.JobMCLogs, payload)
	if err != nil {
		return nil, err
	}
	result, err := h.awaitJob(r.Context(), job.ID, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var out minecraft.MCLogsOutcome
	if err := json.Unmarshal(result, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ============================================================================
// Control-plane loops: reconciliation of instance rows against agent truth
// (live Phase 3 metrics + finished mc jobs), crash recovery and the
// schedule sweeper. Started by registerPhase7 (once per process).
// ============================================================================

var mcLoopsOnce sync.Once

// startMCLoops runs the reconciliation (30s) + schedule sweeper (30s).
// The loops are skipped when EPICPANEL_DISABLE_MC_LOOPS=1 (deterministic
// unit-test harnesses drive reconcileMinecraft directly).
func (s *Server) startMCLoops() {
	mcLoopsOnce.Do(func() {
		if os.Getenv("EPICPANEL_DISABLE_MC_LOOPS") == "1" {
			return
		}
		go func() {
			time.Sleep(10 * time.Second) // settle after boot; converge immediately
			ctx := context.Background()
			s.reconcileMinecraft(ctx)
			s.fireDueMCSchedules(ctx)
		}()
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
					s.reconcileMinecraft(ctx)
					s.fireDueMCSchedules(ctx)
					cancel()
				}
			}
		}()
	})
}

// reconcileMinecraft converges instance rows to agent truth and applies
// crash recovery (restart policy + episode counter). Truth sources, in
// order: 1) live metrics envelope (LIVE samples only — STALE is not truth);
// 2) finished mc jobs since the row's last update.
func (s *Server) reconcileMinecraft(ctx context.Context) {
	store := &minecraft.Store{Pool: s.Pool}
	list, err := store.ListActiveInstances(ctx)
	if err != nil {
		slog.Debug("minecraft reconcile list failed", "err", err)
		return
	}
	for i := range list {
		inst := &list[i]

		// --- live metrics truth ---
		if sample, fresh := s.mcSample(inst); fresh {
			switch {
			case sample.Status == "active" && inst.Status == minecraft.StatusStarting && inst.DesiredState == minecraft.DesiredRunning:
				if _, err := store.ReconcileAgentTruth(ctx, inst.ID, "active"); err == nil {
					s.publishMCEvent(ctx, inst.OrgID, minecraft.EventMCStarted, inst.ID)
				}
			case (sample.Status == "failed" || sample.Status == "inactive") &&
				inst.DesiredState == minecraft.DesiredRunning && inst.Status == minecraft.StatusRunning:
				// Crash (or node-level stop): crash transition + policy.
				if _, err := store.ReconcileAgentTruth(ctx, inst.ID, sample.Status); err == nil {
					s.publishMCEvent(ctx, inst.OrgID, minecraft.EventMCCrashed, inst.ID)
					s.attemptMCCrashRecovery(ctx, store, inst)
				}
			case inst.DesiredState == minecraft.DesiredStopped && sample.Status == "active" &&
				(inst.Status == minecraft.StatusRunning || inst.Status == minecraft.StatusStopping):
				// Converge: wanted stopped, unit still active.
				_, _ = mcEnqueueIdempotent(ctx, s.Jobs, inst.ServerID, inst.ID, jobs.Type(minecraft.JobMCStop),
					minecraft.MCIDPayload{InstanceID: inst.ID.String()}, "mcstop-"+inst.ID.String())
			case sample.Status == "inactive" && inst.DesiredState == minecraft.DesiredStopped &&
				(inst.Status == minecraft.StatusStopping || inst.Status == minecraft.StatusRunning || inst.Status == minecraft.StatusStarting):
				// Agent truth: the unit is down and that is what the customer
				// wanted — confirm stopped (the stop job itself may still be
				// queued; the agent converge is idempotent).
				if err := store.SetStatus(ctx, inst.ID, minecraft.StatusStopped, ""); err == nil {
					s.publishMCEvent(ctx, inst.OrgID, minecraft.EventMCStopped, inst.ID)
				}
			}
		}

		// --- finished-job truth ---
		if err := s.applyMCJobOutcomes(ctx, store, inst); err != nil {
			slog.Debug("minecraft job outcome apply failed", "instance", inst.ID, "err", err)
		}

		// --- truth refresh when no live data ---
		if inst.Status == minecraft.StatusRunning || inst.Status == minecraft.StatusStarting {
			if _, fresh := s.mcSample(inst); !fresh {
				_, _ = mcEnqueueIdempotent(ctx, s.Jobs, inst.ServerID, inst.ID, jobs.Type(minecraft.JobMCStatus),
					minecraft.MCIDPayload{InstanceID: inst.ID.String()}, "mcstatus-"+inst.ID.String())
			}
		}
	}
}

// mcSample returns the live app envelope for an instance + whether it is
// fresh (LIVE only — STALE is not truth).
func (s *Server) mcSample(inst *minecraft.Instance) (*agentprotoSampleView, bool) {
	if s.LiveStore == nil {
		return nil, false
	}
	frame := s.LiveStore.Frame(inst.ServerID)
	if frame == nil || frame.Sample == nil {
		return nil, false
	}
	if state, _ := frame.Freshness["state"].(string); state != "LIVE" {
		return nil, false
	}
	for i := range frame.Apps {
		if frame.Apps[i].WebsiteID == inst.ID.String() {
			return &agentprotoSampleView{
				Status:       frame.Apps[i].Status,
				RestartCount: frame.Apps[i].RestartCount,
				CPUPercent:   frame.Apps[i].CPUPercent,
				MemoryBytes:  frame.Apps[i].MemoryBytes,
				Players:      frame.Apps[i].Players,
				TPS:          frame.Apps[i].TPS,
				MSPT:         frame.Apps[i].MSPT,
			}, true
		}
	}
	return nil, false
}

// agentprotoSampleView mirrors the fields of agentproto.AppSample the loop
// uses (local to avoid importing the protocol package in the api wiring).
type agentprotoSampleView = struct {
	Status       string
	RestartCount int
	CPUPercent   float64
	MemoryBytes  int64
	Players      int
	TPS          float64
	MSPT         float64
}

// applyMCJobOutcomes reads the instance's recent finished jobs and
// converges the row (self-contained; no coordinator wiring needed).
func (s *Server) applyMCJobOutcomes(ctx context.Context, store *minecraft.Store, inst *minecraft.Instance) error {
	list, err := mcListRecent(ctx, s.Jobs, inst.ID, 10)
	if err != nil {
		return err
	}
	for _, job := range list {
		if job.CreatedAt.Before(inst.UpdatedAt.Add(-time.Minute)) {
			continue // stale relative to the row's last update
		}
		if job.Status != jobs.StatusSuccess && job.Status != jobs.StatusFailed {
			continue
		}
		switch job.Type {
		case jobs.Type(minecraft.JobMCStart), jobs.Type(minecraft.JobMCRestart):
			if job.Status == jobs.StatusSuccess {
				if inst.Status == minecraft.StatusStarting || inst.Status == minecraft.StatusCrashed {
					var out minecraft.MCStatusOutcome
					_ = json.Unmarshal(job.Result, &out)
					state := out.UnitState
					if state == "" {
						state = "active"
					}
					if _, aerr := store.ReconcileAgentTruth(ctx, inst.ID, state); aerr == nil {
						s.publishMCEvent(ctx, inst.OrgID, minecraft.EventMCStarted, inst.ID)
					}
				}
			} else if job.Attempts >= job.MaxAttempts && inst.Status == minecraft.StatusStarting {
				_ = store.MarkFailed(ctx, inst.ID, "start job failed: "+job.Error)
			}
		case jobs.Type(minecraft.JobMCStop), jobs.Type(minecraft.JobMCKill):
			if job.Status == jobs.StatusSuccess {
				if err := store.SetStatus(ctx, inst.ID, minecraft.StatusStopped, ""); err == nil {
					s.publishMCEvent(ctx, inst.OrgID, minecraft.EventMCStopped, inst.ID)
				}
			}
		case jobs.Type(minecraft.JobMCInstall):
			if job.Status == jobs.StatusSuccess && inst.Status == minecraft.StatusInstalling {
				if err := store.SetStatus(ctx, inst.ID, minecraft.StatusStopped, ""); err != nil {
					slog.Debug("minecraft install settle failed", "instance", inst.ID, "err", err)
				}
			} else if job.Status == jobs.StatusFailed && job.Attempts >= job.MaxAttempts && inst.Status == minecraft.StatusInstalling {
				_ = store.MarkFailed(ctx, inst.ID, "install failed: "+job.Error)
			}
		case jobs.Type(minecraft.JobMCDelete):
			if job.Status == jobs.StatusSuccess {
				_ = store.MarkDeleted(ctx, inst.ID)
				minecraft.DropConsole(inst.ID)
			}
		case jobs.Type(minecraft.JobMCBackupWorld):
			if job.Status == jobs.StatusSuccess {
				var out minecraft.MCBackupOutcome
				if json.Unmarshal(job.Result, &out) == nil && out.BackupName != "" {
					if _, berr := store.CreateWorldBackup(ctx, inst.ID, out.BackupName, out.SizeBytes, out.SHA256); berr == nil {
						s.publishMCEvent(ctx, inst.OrgID, minecraft.EventMCBackupDone, inst.ID,
							map[string]any{"name": out.BackupName})
					}
				}
			}
		}
	}
	return nil
}

// attemptMCCrashRecovery applies the restart policy: auto-restart crashes
// while the episode counter allows it (policy "no" never auto-restarts).
func (s *Server) attemptMCCrashRecovery(ctx context.Context, store *minecraft.Store, inst *minecraft.Instance) {
	if inst.RestartPolicy == "no" {
		return
	}
	row, err := store.GetRowAny(ctx, inst.ID)
	if err != nil {
		return
	}
	if row.EpisodeRestarts > row.MaxRestarts {
		slog.Warn("minecraft crash recovery exhausted", "instance", inst.ID, "episode_restarts", row.EpisodeRestarts)
		return
	}
	if err := store.MarkStarting(ctx, inst.ID); err != nil {
		return
	}
	payload := s.mcLifecyclePayload(row)
	if _, err := mcEnqueueIdempotent(ctx, s.Jobs, inst.ServerID, inst.ID, jobs.Type(minecraft.JobMCStart), payload,
		"mccrash-"+inst.ID.String()+"-"+strconv.Itoa(row.EpisodeRestarts)); err != nil {
		slog.Error("minecraft crash recovery enqueue failed", "instance", inst.ID, "err", err)
		return
	}
	s.publishMCEvent(ctx, inst.OrgID, "minecraft.recovery_started", inst.ID)
	slog.Info("minecraft crash recovery enqueued", "instance", inst.ID, "episode_restarts", row.EpisodeRestarts)
}

// fireDueMCSchedules enqueues due scheduled actions and advances next_run
// (idempotent claim). Scheduled commands pass the allowlist at create AND
// at fire.
func (s *Server) fireDueMCSchedules(ctx context.Context) {
	store := &minecraft.Store{Pool: s.Pool}
	due, err := store.ListDueSchedules(ctx, 50)
	if err != nil {
		return
	}
	for _, sc := range due {
		row, err := store.GetRowAny(ctx, sc.InstanceID)
		if err != nil || row == nil || row.Status == minecraft.StatusDeleted || row.Status == minecraft.StatusDeleting {
			continue
		}
		interval := minecraft.NextCronAfter(sc.Cron, time.Now().UTC())
		if interval <= 0 {
			continue
		}
		var jobType string
		var payload any
		switch sc.Kind {
		case "restart", "start":
			_ = store.SetDesiredState(ctx, row.ID, minecraft.DesiredRunning)
			if sc.Kind == "start" {
				jobType = minecraft.JobMCStart
				if err := store.MarkStarting(ctx, row.ID); err != nil {
					_ = store.ScheduleFired(ctx, sc.ID, interval)
					continue
				}
			} else {
				jobType = minecraft.JobMCRestart
				if err := store.MarkStopping(ctx, row.ID); err != nil {
					_ = store.ScheduleFired(ctx, sc.ID, interval)
					continue
				}
			}
			payload = s.mcLifecyclePayload(row)
		case "stop":
			_ = store.SetDesiredState(ctx, row.ID, minecraft.DesiredStopped)
			if err := store.MarkStopping(ctx, row.ID); err != nil {
				_ = store.ScheduleFired(ctx, sc.ID, interval)
				continue
			}
			jobType = minecraft.JobMCStop
			payload = minecraft.MCIDPayload{InstanceID: row.ID.String()}
		case "command":
			cmd, cerr := minecraft.ValidateConsoleCommand(sc.Command)
			if cerr != nil {
				_ = store.ScheduleFired(ctx, sc.ID, interval)
				continue
			}
			jobType = minecraft.JobMCCommand
			payload = minecraft.MCCommandPayload{InstanceID: row.ID.String(), Command: cmd}
		default:
			_ = store.ScheduleFired(ctx, sc.ID, interval)
			continue
		}
		if _, err := mcEnqueueIdempotent(ctx, s.Jobs, row.ServerID, row.ID, jobs.Type(jobType), payload,
			"mcsched-"+sc.ID.String()+"-"+strconv.FormatInt(time.Now().Unix()/60, 10)); err == nil {
			_ = store.ScheduleFired(ctx, sc.ID, interval)
			s.publishMCEvent(ctx, row.OrgID, minecraft.EventMCScheduleFired, row.ID, map[string]any{"kind": sc.Kind, "cron": sc.Cron})
		}
	}
}

func (s *Server) publishMCEvent(ctx context.Context, orgID uuid.UUID, eventType string, instanceID uuid.UUID, payload ...map[string]any) {
	if s.Events == nil {
		return
	}
	org := orgID
	var p any
	if len(payload) > 0 {
		p = payload[0]
	}
	s.Events.Publish(ctx, events.Event{
		Type:         eventType,
		Organization: &org,
		ResourceType: "minecraft_instance",
		ResourceID:   instanceID.String(),
		Payload:      p,
	})
}

package servers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/metrics"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

const registrationTokenTTL = 24 * time.Hour

type Handler struct {
	Store *Store
	Orgs  *organizations.Store
	Jobs  *jobs.Store
	Audit *audit.Store
	// Live is the Phase 3 instantaneous metrics store (nil-safe: legacy
	// DB-backed metrics still work when unset, e.g. some tests).
	Live *metrics.LiveStore
	// OnStreamEvent fans agent connection transitions out to the WS hub.
	// Optional; set by the api wiring.
	OnStreamEvent func(serverID uuid.UUID, online bool)
	// OnAgentFrame handles realtime frames forwarded over the agent stream
	// (console.output, server.state, console.history, sync.*). Optional.
	OnAgentFrame func(serverID uuid.UUID, frame agentproto.Frame) bool
}

func (h *Handler) Register(mux *http.ServeMux) {
	// Mutations are platform-admin-only (session, no tokens): the fleet is
	// shared infrastructure and org members must never be able to delete,
	// re-enroll (rotate registration token) or reconfigure servers — the
	// audit's top security finding.
	mux.HandleFunc("POST /v1/organizations/{org_id}/servers", h.requirePlatformAdmin(h.Create))
	mux.HandleFunc("GET /v1/organizations/{org_id}/servers", h.requireOrgRole(organizations.RoleBilling, h.List))
	mux.HandleFunc("GET /v1/organizations/{org_id}/servers/{server_id}", h.requireOrgRole(organizations.RoleBilling, h.Get))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/servers/{server_id}", h.requirePlatformAdmin(h.Delete))
	mux.HandleFunc("POST /v1/organizations/{org_id}/servers/{server_id}/registration-token", h.requirePlatformAdmin(h.RotateRegistrationToken))
	mux.HandleFunc("GET /v1/organizations/{org_id}/servers/{server_id}/metrics", h.requireOrgRole(organizations.RoleBilling, h.GetMetrics))
	mux.HandleFunc("GET /v1/organizations/{org_id}/servers/metrics", h.requireOrgRole(organizations.RoleBilling, h.ListMetrics))
	mux.HandleFunc("GET /v1/organizations/{org_id}/servers/capacity", h.requirePlatformAdmin(h.Capacity))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/servers/{server_id}/maintenance", h.requirePlatformAdmin(h.SetMaintenance))
	mux.HandleFunc("POST /v1/organizations/{org_id}/servers/{server_id}/database-tools", h.requirePlatformAdmin(h.InstallDatabaseTools))

	mux.HandleFunc("POST /v1/agent/enroll", h.AgentEnroll)
	mux.HandleFunc("POST /v1/agent/heartbeat", h.requireAgent(h.AgentHeartbeat))
	mux.HandleFunc("GET /v1/agent/stream", h.requireAgent(h.AgentStream))
}

// RequestConsole asks a connected agent to backfill a workload's console from
// afterSeq. Non-blocking; a disconnected agent simply yields no backfill (the
// ring already holds whatever streamed live).
func RequestConsole(serverID uuid.UUID, workloadID string, afterSeq int64, lines int) bool {
	if !AgentConnected(serverID) {
		return false
	}
	data, err := json.Marshal(agentproto.ConsoleRequest{ServerID: workloadID, AfterSeq: afterSeq, Lines: lines})
	if err != nil {
		return false
	}
	return SendToAgent(serverID, agentproto.Frame{
		Type: agentproto.TypeConsoleRequest, Ts: time.Now().UTC(), Data: data,
	})
}

// SendConsoleCommand pushes a validated console command to a connected agent
// over the persistent stream. Returns false when the agent is offline (the
// caller falls back to the job queue).
func SendConsoleCommand(serverID uuid.UUID, workloadID, command, requestID string) bool {
	if !AgentConnected(serverID) {
		return false
	}
	data, err := json.Marshal(agentproto.ServerCommand{ServerID: workloadID, Command: command, RequestID: requestID})
	if err != nil {
		return false
	}
	return SendToAgent(serverID, agentproto.Frame{
		Type: agentproto.TypeServerCommand, Ts: time.Now().UTC(), RequestID: requestID, Data: data,
	})
}

// requirePlatformAdmin enforces a platform-admin SESSION (API tokens never
// inherit platform-admin) while preserving the org context the handlers read.
func (h *Handler) requirePlatformAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := httpapi.UserFrom(r.Context())
		if !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if user.Role != "admin" || httpapi.IsAPIToken(r.Context()) {
			httpapi.RespondError(w, httpapi.ErrForbidden("platform admin session required"))
			return
		}
		orgID, apiErr := h.resolveOrg(r, r.PathValue("org_id"), organizations.RoleAdmin)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r.WithContext(withOrgID(r.Context(), orgID)))
	}
}

// ListMetrics returns the latest metrics for every server in one call —
// live-store first (in-memory, no DB); DB fallback replaces the frontend's
// per-server N+1 loop for agents that never streamed.
func (h *Handler) ListMetrics(w http.ResponseWriter, r *http.Request) {
	if h.Live != nil {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"metrics": h.Live.Frames()})
		return
	}
	rows, err := h.Store.LatestMetricsForAll(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"metrics": rows})
}

// requireOrgRole resolves the org from the path and enforces server-side RBAC.
// Non-members get 404 so organization existence is not leaked.
func (h *Handler) requireOrgRole(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		orgID, apiErr := h.resolveOrg(r, r.PathValue("org_id"), min)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r.WithContext(withOrgID(r.Context(), orgID)))
	}
}

// ResolveOrg is the exported org resolver for use by other domain handlers
// (websites etc.): resolves org from path param and enforces RBAC.
// Non-members get 404 so organization existence is not leaked.
func (h *Handler) ResolveOrg(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError) {
	return h.resolveOrg(r, orgIDParam, min)
}

// RequireAgent exposes the agent-token middleware for other domain packages.
func (h *Handler) RequireAgent(next http.HandlerFunc) http.HandlerFunc {
	return h.requireAgent(next)
}

func (h *Handler) resolveOrg(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError) {
	orgID, err := uuid.Parse(orgIDParam)
	if err != nil {
		return uuid.Nil, httpapi.ErrValidation("invalid organization id")
	}
	// Platform admins operate across all organizations (cPanel root model).
	user, _ := httpapi.UserFrom(r.Context())
	isPlatformAdmin := user != nil && user.Role == "admin" && !httpapi.IsAPIToken(r.Context())
	if isPlatformAdmin {
		return orgID, nil
	}
	// API tokens are confined to their own organization regardless of the
	// creator's memberships (Phase 11 tenant confinement).
	if httpapi.IsAPIToken(r.Context()) {
		if bound := httpapi.TokenOrgID(r.Context()); bound != orgIDParam {
			return uuid.Nil, httpapi.ErrNotFound("organization not found")
		}
	}
	uid, parseErr := uuid.Parse(user.ID)
	if parseErr != nil {
		return uuid.Nil, httpapi.ErrInternal(parseErr)
	}
	role, err := h.Orgs.RoleFor(r.Context(), orgID, uid)
	if err != nil {
		return uuid.Nil, httpapi.ErrInternal(err)
	}
	if role == "" {
		return uuid.Nil, httpapi.ErrNotFound("organization not found")
	}
	if organizations.RoleRank[role] < organizations.RoleRank[min] {
		return uuid.Nil, httpapi.ErrForbidden("insufficient organization role")
	}
	return orgID, nil
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

// POST /v1/organizations/{org_id}/servers — register a server, get one-time token.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validServerName(req.Name) {
		httpapi.RespondError(w, httpapi.ErrValidation("name must be 1-100 characters (letters, digits, dash, underscore, dot)"))
		return
	}

	user, _ := httpapi.UserFrom(r.Context())
	registeredBy, err := uuid.Parse(user.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	srv, token, err := h.Store.Create(r.Context(), orgID, registeredBy, req.Name, registrationTokenTTL)
	if err == ErrNameTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("a server with that name already exists in this organization"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	h.auditUser(r, &orgID, "server.registered", "server", srv.ID.String(), map[string]any{"name": srv.Name})
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"server":               srv,
		"registration_token":   token,
		"registration_expires": time.Now().Add(registrationTokenTTL).Format(time.RFC3339),
	})
}

// GET /v1/organizations/{org_id}/servers
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	// Route exists under an org path for API consistency, but the fleet is
	// shared across organizations, so the org id is not used for filtering.
	if _, ok := OrgIDFromRequest(r); !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	list, err := h.Store.ListAll(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Server{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"servers": list})
}

func (h *Handler) serverFromPath(r *http.Request, orgID uuid.UUID) (*Server, *httpapi.APIError) {
	serverID, err := uuid.Parse(r.PathValue("server_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid server id")
	}
	srv, err := h.Store.GetByID(r.Context(), serverID)
	if err == ErrNotFound {
		return nil, httpapi.ErrNotFound("server not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return srv, nil
}

// GET /v1/organizations/{org_id}/servers/{server_id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srv, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, srv)
}

// DELETE /v1/organizations/{org_id}/servers/{server_id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srv, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if err := h.Store.Delete(r.Context(), srv.ID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "server.deleted", "server", srv.ID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/organizations/{org_id}/servers/{server_id}/registration-token
func (h *Handler) RotateRegistrationToken(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srv, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	token, expiresAt, err := h.Store.RotateRegistrationToken(r.Context(), srv.ID, registrationTokenTTL)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "server.registration_token_rotated", "server", srv.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"registration_token":   token,
		"registration_expires": expiresAt.Format(time.RFC3339),
	})
}

// GET /v1/organizations/{org_id}/servers/{server_id}/metrics
func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srv, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	// Live store first (master doc: the live dashboard must NOT read the
	// historical DB). DB fallback covers agents that never streamed.
	if h.Live != nil {
		if frame := h.Live.Frame(srv.ID); frame != nil && frame.Sample != nil {
			httpapi.WriteJSON(w, http.StatusOK, map[string]any{"metrics": frame, "freshness": frame.Freshness})
			return
		}
	}
	m, err := h.Store.LatestMetrics(r.Context(), srv.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"metrics": m, "freshness": classifyFreshness(m, srv.LastSeenAt)})
}

// freshness contract (master doc): every live value carries LIVE/STALE/OFFLINE
// plus its age, so the UI can never present old metrics as current.
func classifyFreshness(m *Metrics, lastSeen *time.Time) map[string]any {
	if m == nil {
		return map[string]any{"state": "OFFLINE", "age_ms": nil}
	}
	age := time.Since(m.CollectedAt)
	state := "LIVE"
	if age > 30*time.Second {
		state = "STALE"
	}
	if lastSeen == nil || time.Since(*lastSeen) > OfflineAfter {
		state = "OFFLINE"
	}
	return map[string]any{"state": state, "age_ms": age.Milliseconds(), "collected_at": m.CollectedAt}
}

// GET /v1/organizations/{org_id}/servers/capacity — placement overview.
// NOTE: registered BEFORE the {server_id} routes would shadow it; Go 1.22
// ServeMux resolves the more specific literal path first, so this is safe.
func (h *Handler) Capacity(w http.ResponseWriter, r *http.Request) {
	if _, ok := OrgIDFromRequest(r); !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	caps, err := h.Store.CapacityAll(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if caps == nil {
		caps = []Capacity{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"capacity": caps})
}

// PATCH .../servers/{server_id}/maintenance {"enabled": true}
func (h *Handler) SetMaintenance(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srv, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if err := h.Store.SetMaintenance(r.Context(), srv.ID, req.Enabled); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "server.maintenance", "server", srv.ID.String(), map[string]any{"enabled": req.Enabled})
	w.WriteHeader(http.StatusNoContent)
}

// POST .../database-tools — deploy phpMyAdmin + Adminer (agent job).
func (h *Handler) InstallDatabaseTools(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srv, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if h.Jobs == nil {
		httpapi.RespondError(w, httpapi.ErrInternal(errors.New("jobs store not wired")))
		return
	}
	job, err := h.Jobs.Enqueue(r.Context(), srv.ID, nil, "install_database_tools", map[string]any{})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "server.database_tools_install", "server", srv.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID})
}

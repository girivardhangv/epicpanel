package apps

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

type Handler struct {
	Apps       *Store
	Jobs       *jobs.Store
	Websites   *websites.Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
	// OnAppChanged fires after the desired app state is created or updated so
	// the api layer can converge the reverse-proxy vhost (nil-safe).
	OnAppChanged func(ctx context.Context, websiteID uuid.UUID)
}

// Register mounts application routes. appSites are websites whose runtime is
// node/python/go — those get the process model instead of static/php hosting.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/application", h.requireOrg(organizations.RoleBilling, h.Get))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/application", h.requireOrg(organizations.RoleDeveloper, h.Create))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/websites/{website_id}/application", h.requireOrg(organizations.RoleDeveloper, h.Update))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/application/start", h.requireOrg(organizations.RoleDeveloper, h.Start))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/application/stop", h.requireOrg(organizations.RoleDeveloper, h.Stop))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/application/restart", h.requireOrg(organizations.RoleDeveloper, h.Restart))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/application/status", h.requireOrg(organizations.RoleBilling, h.Status))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/application/logs", h.requireOrg(organizations.RoleBilling, h.Logs))
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

type ctxKey struct{}

func orgFrom(r *http.Request) uuid.UUID {
	if v, ok := r.Context().Value(ctxKey{}).(uuid.UUID); ok {
		return v
	}
	id, _ := uuid.Parse(r.PathValue("org_id"))
	return id
}

func (h *Handler) audit(r *http.Request, orgID uuid.UUID, action, resourceID string, meta map[string]any) {
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
		OrganizationID: &orgID,
		ActorUserID:    actorID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   "application",
		ResourceID:     resourceID,
		Metadata:       meta,
	})
}

// validAppRuntime reports whether a runtime uses the process model.
func validAppRuntime(rt websites.Runtime) bool {
	switch rt {
	case websites.RuntimeNode, websites.RuntimePython, websites.RuntimeGo:
		return true
	}
	return false
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

func (h *Handler) appFromPath(r *http.Request, orgID uuid.UUID) (*websites.Website, *App, *httpapi.APIError) {
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		return nil, nil, apiErr
	}
	app, err := h.Apps.GetByWebsiteAndOrg(r.Context(), orgID, ws.ID)
	if err == ErrNotFound {
		return ws, nil, httpapi.ErrNotFound("no application configured for this website")
	}
	if err != nil {
		return ws, nil, httpapi.ErrInternal(err)
	}
	return ws, app, nil
}

// appPayload is the desired state for the app process.
type appPayload struct {
	StartupCommand string            `json:"startup_command"`
	StartupFile    string            `json:"startup_file"`
	BuildCommand   string            `json:"build_command"`
	Port           int               `json:"internal_port"`
	Env            map[string]string `json:"env_vars"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := orgFrom(r)
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if !validAppRuntime(ws.Runtime) {
		httpapi.RespondError(w, httpapi.ErrValidation("runtime "+string(ws.Runtime)+" does not use the application process model (static/php sites don't have one)"))
		return
	}
	var req appPayload
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if req.Port <= 0 || req.Port > 65535 {
		port, _ := defaultPort(ws.ID.String())
		req.Port = port
	}
	for k := range req.Env {
		if !safeEnv(k) {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid environment variable key: "+k))
			return
		}
	}
	if ws.Runtime == websites.RuntimeNode && strings.TrimSpace(req.StartupFile) == "" && strings.TrimSpace(req.StartupCommand) == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("startup_file or startup_command is required for Node.js apps"))
		return
	}

	envJSON, _ := json.Marshal(req.Env)
	app, err := h.Apps.Create(r.Context(), ws.ID, strings.TrimSpace(req.StartupCommand),
		strings.TrimSpace(req.BuildCommand), strings.TrimSpace(req.StartupFile), req.Port, envJSON)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	// Queue: build (deps) -> provision (service+proxy) -> start.
	buildPayload := map[string]any{
		"website_id":      ws.ID.String(),
		"unix_user":       ws.UnixUser,
		"runtime":         string(ws.Runtime),
		"runtime_version": ws.RuntimeVersion,
		"build_command":   app.BuildCmd,
	}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeBuildApp, buildPayload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "application.created", app.ID.String(), map[string]any{"runtime": string(ws.Runtime)})
	if h.OnAppChanged != nil {
		h.OnAppChanged(r.Context(), ws.ID)
	}
	httpapi.WriteJSON(w, http.StatusCreated, app)
}

func defaultPort(websiteID string) (int, bool) {
	sum := 0
	for _, c := range strings.ReplaceAll(websiteID, "-", "") {
		sum = (sum*31 + int(c)) % 20000
	}
	return 10000 + sum, true
}

func safeEnv(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, c := range k {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// lifecycle job payloads shared with the agent
type appJobPayload struct {
	WebsiteID      string            `json:"website_id"`
	UnixUser       string            `json:"unix_user"`
	Runtime        string            `json:"runtime"`
	RuntimeVersion string            `json:"runtime_version"`
	StartupCommand string            `json:"startup_command"`
	StartupFile    string            `json:"startup_file"`
	BuildCommand   string            `json:"build_command"`
	InternalPort   int               `json:"internal_port"`
	Env            map[string]string `json:"env"`
}

func (h *Handler) buildPayload(ws *websites.Website, app *App) appJobPayload {
	var env map[string]string
	_ = json.Unmarshal(app.Env, &env)
	return appJobPayload{
		WebsiteID:      ws.ID.String(),
		UnixUser:       ws.UnixUser,
		Runtime:        string(ws.Runtime),
		RuntimeVersion: ws.RuntimeVersion,
		StartupCommand: app.StartupCmd,
		StartupFile:    app.StartupFile,
		BuildCommand:   app.BuildCmd,
		InternalPort:   app.Port,
		Env:            env,
	}
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := orgFrom(r)
	_, app, apiErr := h.appFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	// Env VALUES are never returned by GET (they are secrets at rest since
	// the audit); only the variable names are exposed.
	out := *app
	var env map[string]string
	_ = json.Unmarshal(app.Env, &env)
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	if b, err := json.Marshal(map[string]any{"keys": names}); err == nil {
		out.Env = b
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := orgFrom(r)
	ws, app, apiErr := h.appFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req appPayload
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	for k := range req.Env {
		if !safeEnv(k) {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid environment variable key: "+k))
			return
		}
	}
	envJSON, _ := json.Marshal(req.Env)
	if req.Env == nil {
		// env_vars omitted from the PATCH: keep the stored values. The GET
		// endpoint only exposes key NAMES (values are secrets), so a config
		// edit that doesn't touch env must never wipe the stored env.
		envJSON = app.Env
	}
	if _, err := h.Apps.Update(r.Context(), app, strings.TrimSpace(req.StartupCommand),
		strings.TrimSpace(req.BuildCommand), strings.TrimSpace(req.StartupFile), req.Port, envJSON); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// restart to apply new config
	payload := h.buildPayload(ws, app)
	payload.StartupCommand = req.StartupCommand
	payload.StartupFile = req.StartupFile
	payload.BuildCommand = req.BuildCommand
	if req.Port > 0 {
		payload.InternalPort = req.Port
	} // else keep the stored port resolved by buildPayload
	if req.Env != nil {
		payload.Env = req.Env
	} // else keep the stored env resolved by buildPayload
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeRestartApp, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "application.updated", app.ID.String(), nil)
	if h.OnAppChanged != nil {
		h.OnAppChanged(r.Context(), ws.ID)
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "restart queued"})
}

func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	orgID := orgFrom(r)
	ws, app, apiErr := h.appFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeStartApp, h.buildPayload(ws, app)); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "application.started", app.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "start queued"})
}

func (h *Handler) Stop(w http.ResponseWriter, r *http.Request) {
	orgID := orgFrom(r)
	ws, app, apiErr := h.appFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeStopApp, h.buildPayload(ws, app)); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "application.stopped", app.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "stop queued"})
}

func (h *Handler) Restart(w http.ResponseWriter, r *http.Request) {
	orgID := orgFrom(r)
	ws, app, apiErr := h.appFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeRestartApp, h.buildPayload(ws, app)); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "application.restarted", app.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "restart queued"})
}

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	orgID := orgFrom(r)
	ws, app, apiErr := h.appFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	payload := h.buildPayload(ws, app)
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeAppStatus, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// status result is fetched from the job result; return current known health
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"health": string(app.Health), "job": "status check queued"})
}

func (h *Handler) Logs(w http.ResponseWriter, r *http.Request) {
	orgID := orgFrom(r)
	ws, app, apiErr := h.appFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	lines := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			lines = n
		}
	}
	logPayload := map[string]any{"website_id": app.WebsiteID.String(), "lines": lines}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeAppLogs, logPayload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "log fetch queued"})
}

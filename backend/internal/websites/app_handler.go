package websites

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// ============================================================================
// App-platform API (node/python/go websites): config, lifecycle and logs.
//
// Serving model: nginx reverse-proxies to the site's app process on a
// private loopback port (websites.app_port); the process runs as the site's
// unix user under a managed systemd unit. Config is desired state persisted
// on the websites row; convergence happens through the job queue — provision
// renders the proxy vhost, build_app builds, start_app runs the process
// (chained automatically, see agent_endpoints.go).
// ============================================================================

// AppJobSpec mirrors the agent's AppSpec job payload — plain JSON so the
// control plane never imports the agent package.
type AppJobSpec struct {
	WebsiteID      string            `json:"website_id"`
	UnixUser       string            `json:"unix_user"`
	Runtime        string            `json:"runtime"`
	RuntimeVersion string            `json:"runtime_version"`
	AppRoot        string            `json:"app_root"`
	StartupFile    string            `json:"startup_file"`
	StartupCommand string            `json:"startup_command"`
	BuildCommand   string            `json:"build_command"`
	InternalPort   int               `json:"internal_port"`
	Env            map[string]string `json:"env,omitempty"`
	// EnvEnc is the base64 secretbox ciphertext of the JSON env map (env
	// vars hold secrets; they are encrypted at rest and travel ciphertext).
	EnvEnc string `json:"app_env_enc,omitempty"`
}

// isAppRuntime reports whether the runtime is served as a proxied app
// process rather than as files (docroot mode).
func isAppRuntime(rt Runtime) bool {
	switch rt {
	case RuntimeNode, RuntimePython, RuntimeGo:
		return true
	}
	return false
}

// buildAppSpec assembles the agent job payload from the website's desired
// state, decrypting the env blob for the agent (the agent re-verifies keys).
func (h *Handler) buildAppSpec(ctx context.Context, ws *Website) (AppJobSpec, *httpapi.APIError) {
	spec := AppJobSpec{
		WebsiteID:      ws.ID.String(),
		UnixUser:       ws.UnixUser,
		Runtime:        string(ws.Runtime),
		RuntimeVersion: ws.RuntimeVersion,
		AppRoot:        DocumentRootFor(ws.ID), // the release the public symlink points at
		StartupCommand: ws.AppStartupCommand,
		BuildCommand:   ws.AppBuildCommand,
		InternalPort:   ws.AppPort,
	}
	if blob, err := h.Websites.GetAppEnv(ctx, ws.ID); err == nil && len(blob) > 0 {
		spec.EnvEnc = base64.StdEncoding.EncodeToString(blob)
	} else if err != nil {
		return spec, httpapi.ErrInternal(err)
	}
	return spec, nil
}

func (h *Handler) enqueueBuildApp(ctx context.Context, ws *Website) error {
	spec, apiErr := h.buildAppSpec(ctx, ws)
	if apiErr != nil {
		return apiErr
	}
	_, err := h.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeBuildApp, spec, "build_app_"+ws.ID.String())
	return err
}

// enqueueStartApp runs the process directly (no build hop) — used by the
// build→start chaining, not by the start endpoint.
func (h *Handler) enqueueStartApp(ctx context.Context, ws *Website) error {
	spec, apiErr := h.buildAppSpec(ctx, ws)
	if apiErr != nil {
		return apiErr
	}
	_, err := h.Jobs.Enqueue(ctx, ws.ServerID, &ws.ID, jobs.TypeStartApp, spec)
	return err
}

// GET /v1/organizations/{org_id}/websites/{website_id}/app
// Returns the app config incl. env (org-scoped; env is user-authored config,
// not a platform secret — deploy tokens, by contrast, are never returned).
func (h *Handler) GetApp(w http.ResponseWriter, r *http.Request) {
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
	if !isAppRuntime(ws.Runtime) {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"app": nil})
		return
	}
	env := map[string]string{}
	if blob, err := h.Websites.GetAppEnv(r.Context(), ws.ID); err == nil && len(blob) > 0 {
		if plain, derr := decryptAppEnv(blob); derr == nil {
			env = plain
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"app": map[string]any{
		"runtime":         ws.Runtime,
		"runtime_version": ws.RuntimeVersion,
		"startup_command": ws.AppStartupCommand,
		"build_command":   ws.AppBuildCommand,
		"desired_state":   ws.AppDesiredState,
		"port":            ws.AppPort,
		"env":             env,
		"unit":            "epicpanel-app-" + ws.ID.String(),
	}})
}

// PUT /v1/organizations/{org_id}/websites/{website_id}/app
// Body: {"startup_command": "...", "build_command": "...", "desired_state":
// "running|stopped", "env": {"KEY": "value"}} — fields are optional; env
// omitted leaves stored env untouched. Enqueues a provision so the proxy
// vhost converges; chaining builds + (re)starts the process per desired
// state.
func (h *Handler) SetApp(w http.ResponseWriter, r *http.Request) {
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
	if !isAppRuntime(ws.Runtime) {
		httpapi.RespondError(w, httpapi.ErrValidation(string(ws.Runtime)+" sites are served from files; app config applies to node/python/go sites"))
		return
	}
	var req struct {
		StartupCommand *string           `json:"startup_command"`
		BuildCommand   *string           `json:"build_command"`
		DesiredState   string            `json:"desired_state"`
		Env            map[string]string `json:"env"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	startup, build := ws.AppStartupCommand, ws.AppBuildCommand
	if req.StartupCommand != nil {
		startup = strings.TrimSpace(*req.StartupCommand)
		if len(startup) > 500 {
			httpapi.RespondError(w, httpapi.ErrValidation("startup_command too long (max 500 chars)"))
			return
		}
	}
	if req.BuildCommand != nil {
		build = strings.TrimSpace(*req.BuildCommand)
		if len(build) > 500 {
			httpapi.RespondError(w, httpapi.ErrValidation("build_command too long (max 500 chars)"))
			return
		}
	}
	desired := ws.AppDesiredState
	if desired == "" {
		desired = "running"
	}
	if req.DesiredState != "" {
		if req.DesiredState != "running" && req.DesiredState != "stopped" {
			httpapi.RespondError(w, httpapi.ErrValidation("desired_state must be running or stopped"))
			return
		}
		desired = req.DesiredState
	}
	envCipher, apiErr := encryptAppEnv(req.Env)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	if err := h.Websites.SetAppConfig(r.Context(), ws.ID, startup, build, desired); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if envCipher != nil {
		if err := h.Websites.SetAppEnv(r.Context(), ws.ID, envCipher); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}
	// First app config on a site: allocate the private loopback port now so
	// the agent receives a stable, persisted port in the desired state.
	if ws.AppPort == 0 && h.Ports != nil {
		port, err := h.Ports.AllocateFor(r.Context(), ws.ServerID.String(), ws.ID.String(), "app", 0)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
			return
		}
		ws.AppPort = port
	}
	ws.AppStartupCommand, ws.AppBuildCommand, ws.AppDesiredState = startup, build, desired

	payload, apiErr := h.buildDesiredPayload(r.Context(), ws, orgID, ws.UnixUser, ws.RuntimeVersion)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	job, err := h.Jobs.EnqueueIdempotent(r.Context(), ws.ServerID, &ws.ID, jobs.TypeProvisionWebsite, payload, "provision_website_"+ws.ID.String())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.app_configured", "website", ws.ID.String(), map[string]any{
		"desired_state": desired, "env_keys": len(req.Env),
	})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID})
}

// POST /v1/organizations/{org_id}/websites/{website_id}/app/build
func (h *Handler) BuildApp(w http.ResponseWriter, r *http.Request) {
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
	if !isAppRuntime(ws.Runtime) {
		httpapi.RespondError(w, httpapi.ErrValidation(string(ws.Runtime)+" sites have no app build step"))
		return
	}
	if err := h.enqueueBuildApp(r.Context(), ws); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.app_build_requested", "website", ws.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

// POST /v1/organizations/{org_id}/websites/{website_id}/app/start
func (h *Handler) StartApp(w http.ResponseWriter, r *http.Request) {
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
	if !isAppRuntime(ws.Runtime) {
		httpapi.RespondError(w, httpapi.ErrValidation(string(ws.Runtime)+" sites have no app process"))
		return
	}
	if ws.AppDesiredState != "running" {
		if err := h.Websites.SetAppConfig(r.Context(), ws.ID, ws.AppStartupCommand, ws.AppBuildCommand, "running"); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		ws.AppDesiredState = "running"
	}
	// Start funnels through build_app: its success outcome chains start_app
	// (desired state is running now), so an unbuilt app is built first and
	// an already-built one converges regardless of current unit state.
	if err := h.enqueueBuildApp(r.Context(), ws); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.app_started", "website", ws.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

// POST /v1/organizations/{org_id}/websites/{website_id}/app/stop
func (h *Handler) StopApp(w http.ResponseWriter, r *http.Request) {
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
	if !isAppRuntime(ws.Runtime) {
		httpapi.RespondError(w, httpapi.ErrValidation(string(ws.Runtime)+" sites have no app process"))
		return
	}
	if err := h.Websites.SetAppConfig(r.Context(), ws.ID, ws.AppStartupCommand, ws.AppBuildCommand, "stopped"); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	ws.AppDesiredState = "stopped"
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeStopApp,
		map[string]string{"website_id": ws.ID.String()}); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.app_stopped", "website", ws.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

// POST /v1/organizations/{org_id}/websites/{website_id}/app/restart
func (h *Handler) RestartApp(w http.ResponseWriter, r *http.Request) {
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
	if !isAppRuntime(ws.Runtime) {
		httpapi.RespondError(w, httpapi.ErrValidation(string(ws.Runtime)+" sites have no app process"))
		return
	}
	spec, apiErr := h.buildAppSpec(r.Context(), ws)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeRestartApp, spec); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.app_restarted", "website", ws.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "queued"})
}

// POST /v1/organizations/{org_id}/websites/{website_id}/app/logs
// Body: {"lines": 200} — enqueues an app_logs job (journald dump); the
// result lands in the website's job list for the UI to poll.
func (h *Handler) AppLogs(w http.ResponseWriter, r *http.Request) {
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
	if !isAppRuntime(ws.Runtime) {
		httpapi.RespondError(w, httpapi.ErrValidation(string(ws.Runtime)+" sites have no app process"))
		return
	}
	var req struct {
		Lines int `json:"lines"`
	}
	_ = httpapi.Read(r, &req)
	if req.Lines <= 0 || req.Lines > 500 {
		req.Lines = 200
	}
	job, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeAppLogs,
		map[string]any{"website_id": ws.ID.String(), "lines": req.Lines})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID})
}

// encryptAppEnv validates the user-supplied env map and encrypts it for the
// app_env_encrypted column. nil env → nil (leaves stored env untouched).
func encryptAppEnv(env map[string]string) ([]byte, *httpapi.APIError) {
	if env == nil {
		return nil, nil
	}
	for k, v := range env {
		if !validEnvKey(k) {
			return nil, httpapi.ErrValidation("invalid env key " + k + " (A-Z, a-z, 0-9, _; max 64 chars)")
		}
		if len(v) > 2000 {
			return nil, httpapi.ErrValidation("env value for " + k + " too long (max 2000 chars)")
		}
	}
	plain, err := json.Marshal(env)
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	cipher, err := secretbox.Encrypt(string(plain))
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return cipher, nil
}

// decryptAppEnv reverses encryptAppEnv (stored blob → plain map).
func decryptAppEnv(cipher []byte) (map[string]string, error) {
	plain, err := secretbox.Decrypt(cipher)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	if err := json.Unmarshal([]byte(plain), &env); err != nil {
		return nil, err
	}
	return env, nil
}

// validEnvKey mirrors the agent's env key rules (A-Z, a-z, 0-9, _, ≤64).
func validEnvKey(k string) bool {
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

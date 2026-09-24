package websites

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/limits"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/packages"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
)

var errOrgContext = errStr("org id missing from request context")

type errStr string

func (e errStr) Error() string { return string(e) }

type Handler struct {
	Websites *Store
	Jobs     *jobs.Store
	Configs  *ConfigStore
	// PHPSettings persists per-site php.ini overrides (MultiPHP INI Editor).
	PHPSettings *PHPSettingsStore
	Ext         *HandlerExtensions
	Orgs     *organizations.Store
	Servers  *servers.Store
	Audit    *audit.Store
	// Runtimes is used to validate runtime_version against installed runtimes.
	Runtimes RuntimeChecker
	// PackageChecker enforces hosting-package limits (sites, runtimes).
	PackageChecker PackageLimits
	// DBs provisions WordPress databases (api adapter over databases store).
	DBs DBProvisioner
	// WPPending stores admin details until the auto-created DB is ready.
	WPPending *WPPendingStore
	// LimitsFor resolves plan limits for the resource-usage view.
	LimitsFor LimitsForFunc
	// Domains provides the serving list for vhost rendering.
	Domains DomainLister
	// Redirects provides the domain-level redirect list for vhost rendering
	// (implemented by the api layer over the domains store; nil-safe).
	Redirects RedirectLister
	// Events publishes lifecycle events to the platform event bus (nil-safe).
	Events *events.Bus
	// Ports allocates private backend ports for proxy web-server modes.
	Ports *BackendPortAllocator
	// PackageForOrg resolves the org's effective hosting package (nil = no
	// quota information; agent defaults apply).
	PackageForOrg PackageForOrgFunc
	// FreePerkPackage resolves the free_perk hosting_packages row (the
	// Free Perk resource set, admin-editable via package CRUD). nil = perk
	// overlay has no dedicated package and leaves agent defaults.
	FreePerkPackage func(ctx context.Context) (*packages.Package, bool)
	// FreePerkLimit returns (cap, used) for the org's Free Perk sites —
	// cap comes from the panel setting (implemented by the api layer;
	// nil = unlimited).
	FreePerkLimit func(ctx context.Context, orgID uuid.UUID) (int, int, error)
	// AppPortLookup reports the loopback port of the site's configured
	// application process (node/python/go), if any (nil-safe; implemented by
	// the api layer over the applications store to avoid a package cycle).
	AppPortLookup func(ctx context.Context, websiteID uuid.UUID) (int, bool)
	// RegisterPrimaryDomain creates the primary domain row after website
	// creation (implemented by the api layer to avoid package cycles).
	RegisterPrimaryDomain func(ctx context.Context, orgID, websiteID uuid.UUID, domain string) error
	// PickServer auto-selects a server for placement (Phase 12 scheduler);
	// implemented by the api layer.
	PickServer func(ctx context.Context, orgID uuid.UUID, runtime, runtimeVersion string) (uuid.UUID, error)
	// InstallRuntime requests a runtime install on a server (runtimes
	// subsystem seam, implemented by the api layer) — used by website
	// creation with install_if_missing.
	InstallRuntime func(ctx context.Context, serverID, createdBy uuid.UUID, rtType, version string) error
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
	// OnJobFinished is invoked for every finished job so other subsystems
	// (e.g. runtimes) can react to install/remove outcomes.
	OnJobFinished func(ctx context.Context, job *jobs.Job, result json.RawMessage)
	// OnJobClaimed is invoked when an agent claims a job (running states).
	OnJobClaimed func(ctx context.Context, job *jobs.Job)
}

// RuntimeChecker lets the websites package validate runtime availability
// without importing the runtimes store (keeps domain packages decoupled).
type RuntimeChecker interface {
	GetByTypeVersion(ctx context.Context, serverID uuid.UUID, t string, version string) (RuntimeRef, error)
}

type RuntimeRef struct {
	ID     uuid.UUID
	Status string
}

// PackageLimits is the quota gate (implemented by the api layer over the
// packages store) — max sites + allowed runtimes per organization.
type PackageLimits interface {
	CheckSiteAllowed(ctx context.Context, orgID uuid.UUID, runtime, primaryDomain string) error
}

// DomainLister provides the domain serving list for a website (primary +
// aliases with SSL config) without importing the domains package.
type DomainLister interface {
	ListForWebsiteServing(ctx context.Context, websiteID uuid.UUID) ([]DomainServing, error)
}

// RedirectLister provides the enabled domain redirects for vhost rendering.
type RedirectLister interface {
	ListForWebsiteServing(ctx context.Context, websiteID uuid.UUID) ([]DesiredRedirect, error)
}

// PackageForOrgFunc resolves the effective hosting package for an org
// (implemented by the api layer over the packages store, avoiding the
// packages -> websites import direction).
type PackageForOrgFunc func(ctx context.Context, orgID uuid.UUID) (*PackageRef, error)

// PackageRef carries the quota columns the limits seam consumes.
type PackageRef struct {
	MemoryLimitMB int
	CPUCores      float64
}

type DomainServing struct {
	Domain        string
	SSLMode       string
	CertPath      string
	KeyPath       string
	DocrootSuffix string
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)
var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func (h *Handler) Register(mux *http.ServeMux, requireAgent func(http.HandlerFunc) http.HandlerFunc) {
	h.RegisterFiles(mux)
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites", h.requireOrg(organizations.RoleDeveloper, h.Create))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites", h.requireOrg(organizations.RoleBilling, h.List))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}", h.requireOrg(organizations.RoleBilling, h.Get))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/websites/{website_id}", h.requireOrg(organizations.RoleDeveloper, h.Update))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/websites/{website_id}", h.requireOrg(organizations.RoleAdmin, h.Delete))
	// developer+ (not billing): job results can carry credentials (e.g. the
	// WordPress admin password), so they are not for read-only roles.
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/jobs", h.requireOrg(organizations.RoleDeveloper, h.ListJobs))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/config", h.requireOrg(organizations.RoleBilling, h.GetConfig))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/usage", h.requireOrg(organizations.RoleBilling, h.GetUsage))
	mux.HandleFunc("PUT /v1/organizations/{org_id}/websites/{website_id}/config/rewrite", h.requireOrg(organizations.RoleDeveloper, h.SetRewriteRules))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/app", h.requireOrg(organizations.RoleBilling, h.GetApp))
	mux.HandleFunc("PUT /v1/organizations/{org_id}/websites/{website_id}/app", h.requireOrg(organizations.RoleDeveloper, h.SetApp))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/app/build", h.requireOrg(organizations.RoleDeveloper, h.BuildApp))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/app/start", h.requireOrg(organizations.RoleDeveloper, h.StartApp))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/app/stop", h.requireOrg(organizations.RoleAdmin, h.StopApp))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/app/restart", h.requireOrg(organizations.RoleDeveloper, h.RestartApp))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/app/logs", h.requireOrg(organizations.RoleDeveloper, h.AppLogs))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/php-settings", h.requireOrg(organizations.RoleBilling, h.GetPHPSettings))
	mux.HandleFunc("PUT /v1/organizations/{org_id}/websites/{website_id}/php-settings", h.requireOrg(organizations.RoleDeveloper, h.SetPHPSettings))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/suspend", h.requireOrg(organizations.RoleAdmin, h.Suspend))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/resume", h.requireOrg(organizations.RoleAdmin, h.Resume))

	mux.HandleFunc("POST /v1/agent/jobs/claim", requireAgent(h.AgentClaim))
	mux.HandleFunc("POST /v1/agent/jobs/{job_id}/result", requireAgent(h.AgentResult))
	mux.HandleFunc("POST /v1/agent/jobs/{job_id}/progress", requireAgent(h.AgentProgress))
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

// OrgIDFromRequest resolves the org id regardless of which wrapper set it:
// routes behind requireOrg carry orgIDCtxKey, while routes registered inline
// in the api layer (one-click installs, site commands) inject OrgKeyType.
func OrgIDFromRequest(r *http.Request) (uuid.UUID, bool) {
	if id, ok := r.Context().Value(orgIDCtxKey{}).(uuid.UUID); ok {
		return id, true
	}
	return OrgIDFromStagingContext(r)
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

// POST /v1/organizations/{org_id}/websites
// Body: {"name": "mysite", "server_id": "...", "runtime": "static", "primary_domain": "example.com"}
// Returns 202 with the website in pending state and the enqueued provision job.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	// Business rule (cPanel account model): sites are created by the platform
	// ADR-030: sites are created by platform administrators or through the
	// API (scoped tokens — org-confined); assigned users manage them but
	// cannot create new ones.
	user, _ := httpapi.UserFrom(r.Context())
	if user != nil && user.Role != "admin" && !httpapi.IsAPIToken(r.Context()) {
		httpapi.RespondError(w, httpapi.ErrForbidden("site creation is performed by platform administrators"))
		return
	}
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	var req struct {
		Name             string `json:"name"`
		ServerID         string `json:"server_id"`
		Runtime          string `json:"runtime"`
		RuntimeVersion   string `json:"runtime_version"`
		WebServer        string `json:"web_server"`
		PrimaryDomain    string `json:"primary_domain"`
		InstallIfMissing bool   `json:"install_if_missing"`
		StartupCommand   string `json:"startup_command"`
		BuildCommand     string `json:"build_command"`
		// Dynamic resources: opt the new site into traffic-adaptive
		// allocation and/or the Free Perk overlay at creation time (both
		// can also be changed later via the dedicated endpoints).
		FreePerk        bool `json:"free_perk"`
		DynamicEnabled  bool `json:"dynamic_enabled"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.PrimaryDomain = strings.ToLower(strings.TrimSpace(req.PrimaryDomain))
	req.RuntimeVersion = strings.TrimSpace(req.RuntimeVersion)

	if !nameRe.MatchString(req.Name) {
		httpapi.RespondError(w, httpapi.ErrValidation("name must be lowercase letters, digits and dashes (2-63 chars)"))
		return
	}
	rt := Runtime(req.Runtime)
	if rt == "" {
		rt = RuntimeStatic
	}
	if req.WebServer == "" {
		req.WebServer = "nginx"
	}
	if _, vErr := validateWebServerList(req.WebServer); vErr != nil && req.WebServer != "none" {
		httpapi.RespondError(w, vErr)
		return
	}
	switch rt {
	case RuntimeStatic:
		req.RuntimeVersion = ""
	case RuntimePHP, RuntimeNode, RuntimePython, RuntimeGo:
		if req.RuntimeVersion == "" {
			httpapi.RespondError(w, httpapi.ErrValidation("runtime_version is required for "+string(rt)))
			return
		}
	default:
		httpapi.RespondError(w, httpapi.ErrValidation("runtime must be one of: static, php, node, python, go"))
		return
	}
	if req.PrimaryDomain != "" && len(req.PrimaryDomain) > 253 {
		httpapi.RespondError(w, httpapi.ErrValidation("primary_domain too long"))
		return
	}
	if req.PrimaryDomain != "" && !domainRe.MatchString(req.PrimaryDomain) {
		httpapi.RespondError(w, httpapi.ErrValidation("primary_domain is not a valid hostname"))
		return
	}
	serverID, err := uuid.Parse(req.ServerID)
	if err != nil {
		// Phase 12: server_id is optional — auto-place on the least-loaded
		// online server (runtime-aware) when omitted or "auto".
		rtFilter := string(rt)
		if rt == RuntimeStatic {
			rtFilter = ""
		}
		picked, pickErr := h.PickServer(r.Context(), orgID, rtFilter, req.RuntimeVersion)
		if pickErr != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("could not select a server automatically: "+pickErr.Error()))
			return
		}
		serverID = picked
	}

	// The target server must exist (fleet is shared across organizations).
	if _, err := h.Servers.GetByID(r.Context(), serverID); err == servers.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("server not found"))
		return
	} else if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	// Package limits: domain buckets (addon vs subdomain), overall site cap
	// and allowed runtimes for this org's plan.
	if h.PackageChecker != nil {
		if err := h.PackageChecker.CheckSiteAllowed(r.Context(), orgID, string(rt), req.PrimaryDomain); err != nil {
			httpapi.RespondError(w, httpapi.ErrForbidden(err.Error()))
			return
		}
	}

	// A dynamic runtime must be installed (or installing) on the target
	// server. With install_if_missing, a missing runtime is requested first
	// and the site provisions automatically once it lands (job chaining on
	// the install_runtime outcome) — one-click create, no pre-install trip.
	createdBy, parseErr := uuid.Parse(user.ID)
	if parseErr != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(parseErr))
		return
	}
	if rt != RuntimeStatic {
		ref, rtErr := h.Runtimes.GetByTypeVersion(r.Context(), serverID, string(rt), req.RuntimeVersion)
		switch {
		case rtErr != nil:
			if !req.InstallIfMissing || h.InstallRuntime == nil {
				httpapi.RespondError(w, httpapi.ErrValidation("runtime "+string(rt)+" "+req.RuntimeVersion+" is not installed on this server (pass install_if_missing to install it first)"))
				return
			}
			if iErr := h.InstallRuntime(r.Context(), serverID, createdBy, string(rt), req.RuntimeVersion); iErr != nil {
				httpapi.RespondError(w, httpapi.ErrValidation("runtime install request failed: "+iErr.Error()))
				return
			}
		case ref.Status != "available" && ref.Status != "installing":
			httpapi.RespondError(w, httpapi.ErrValidation("runtime "+string(rt)+" "+req.RuntimeVersion+" is not available (status: "+ref.Status+")"))
			return
		}
	}

	// Free Perk cap: check BEFORE creating so a rejected request leaves no
	// site row behind (fail-closed on lookup errors, like other gates).
	if req.FreePerk && h.FreePerkLimit != nil {
		capn, used, err := h.FreePerkLimit(r.Context(), orgID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrForbidden("free perk availability unavailable; creation blocked"))
			return
		}
		if used >= capn {
			httpapi.RespondError(w, httpapi.ErrConflict(fmt.Sprintf(
				"free perk limit reached (%d of %d sites); raise the limit in panel settings", used, capn)))
			return
		}
	}

	ws, unixUser, err := h.Websites.Create(r.Context(), orgID, serverID, createdBy, req.Name, req.PrimaryDomain, rt, req.RuntimeVersion, req.WebServer)
	if err == ErrNameTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("a website with that name already exists on this server"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// Creation-time dynamic-resources flags: set BEFORE the desired state is
	// built so the provision job already carries perk pool sizing.
	if req.FreePerk {
		if err := h.Websites.SetFreePerk(r.Context(), ws.ID, true); err == nil {
			ws.FreePerk = true
		}
	}
	if req.DynamicEnabled {
		if err := h.Websites.SetDynamicEnabled(r.Context(), ws.ID, true); err == nil {
			ws.DynamicEnabled = true
			_ = h.Websites.SetDynamicState(r.Context(), ws.ID, 1, DynStateActive)
		}
	}

	// Proxy mode: allocate the private backend port before enqueueing so the
	// agent receives a stable, persisted port in the desired state.
	if mode := backendMode(req.WebServer); mode != "" && h.Ports != nil {
		port, err := h.Ports.AllocateFor(r.Context(), ws.ServerID.String(), ws.ID.String(), mode, 0)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
			return
		}
		ws.BackendPort = port
	}

	// App runtimes: allocate the private loopback app port and seed any
	// app config that came with the create request. Desired state stays
	// "stopped" until the user asks for a start — the site is empty at
	// create time, and start jobs on missing builds would only fail.
	if isAppRuntime(rt) {
		if req.StartupCommand != "" || req.BuildCommand != "" {
			if err := h.Websites.SetAppConfig(r.Context(), ws.ID, strings.TrimSpace(req.StartupCommand), strings.TrimSpace(req.BuildCommand), "stopped"); err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
			ws.AppStartupCommand, ws.AppBuildCommand, ws.AppDesiredState = strings.TrimSpace(req.StartupCommand), strings.TrimSpace(req.BuildCommand), "stopped"
		}
		if h.Ports != nil {
			port, err := h.Ports.AllocateFor(r.Context(), ws.ServerID.String(), ws.ID.String(), "app", 0)
			if err != nil {
				httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
				return
			}
			ws.AppPort = port
		}
	}

	// Desired state -> provisioning job for the agent.
	payload, apiErr := h.buildDesiredPayload(r.Context(), ws, orgID, unixUser, ws.RuntimeVersion)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	job, err := h.Jobs.EnqueueIdempotent(r.Context(), serverID, &ws.ID, jobs.TypeProvisionWebsite, payload, "provision_website_"+ws.ID.String())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	h.auditUser(r, &orgID, "website.created", "website", ws.ID.String(), map[string]any{
		"name": ws.Name, "runtime": string(ws.Runtime), "server_id": serverID.String(),
	})
	if h.RegisterPrimaryDomain != nil && req.PrimaryDomain != "" {
		if err := h.RegisterPrimaryDomain(r.Context(), orgID, ws.ID, req.PrimaryDomain); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{
		"website": ws,
		"job_id":  job.ID,
	})
}

// GET /v1/organizations/{org_id}/websites
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	list, err := h.Websites.ListForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Website{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"websites": list})
}

func (h *Handler) websiteFromPath(r *http.Request, orgID uuid.UUID) (*Website, *httpapi.APIError) {
	websiteID, err := uuid.Parse(r.PathValue("website_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid website id")
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, websiteID)
	if err == ErrNotFound {
		return nil, httpapi.ErrNotFound("website not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return ws, nil
}

// GET /v1/organizations/{org_id}/websites/{website_id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
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
	httpapi.WriteJSON(w, http.StatusOK, ws)
}

// PATCH /v1/organizations/{org_id}/websites/{website_id}
// Body: {"runtime_version": "8.3"} — updates desired state and enqueues a
// reconcile job; the agent converges the server (pool file, cleanup of the
// previous version's pool).
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
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
	if ws.Status == StatusDeleting || ws.Status == StatusDeleted {
		httpapi.RespondError(w, httpapi.ErrConflict("website is being deleted"))
		return
	}
	if ws.Status == StatusSuspended {
		httpapi.RespondError(w, httpapi.ErrConflict("website is suspended (resume it first)"))
		return
	}

	var req struct {
		Runtime        string `json:"runtime"`
		RuntimeVersion string `json:"runtime_version"`
		WebServer      string `json:"web_server"`
		DocrootSuffix  string `json:"docroot_suffix"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Runtime = strings.TrimSpace(req.Runtime)
	req.RuntimeVersion = strings.TrimSpace(req.RuntimeVersion)
	req.WebServer = strings.TrimSpace(req.WebServer)

	// --- web server selection (multi web server support) ---
	// Modes: nginx | nginx,apache | nginx,openlitespeed. nginx is always the
	// edge and owns :80/:443; backends run on private loopback ports.
	if req.WebServer != "" {
		if _, vErr := validateWebServerList(req.WebServer); vErr != nil {
			httpapi.RespondError(w, vErr)
			return
		}
		if err := h.Websites.SetWebServer(r.Context(), ws.ID, req.WebServer); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		ws.WebServer = req.WebServer

		// Backend port lifecycle: keep a valid existing allocation (stable
		// across restarts), allocate when entering/switching proxy mode,
		// release when returning to plain nginx.
		mode := backendMode(req.WebServer)
		switch {
		case mode == "":
			if ws.BackendPort != 0 {
				if err := h.Websites.SetBackendPort(r.Context(), ws.ID, 0); err != nil {
					httpapi.RespondError(w, httpapi.ErrInternal(err))
					return
				}
				ws.BackendPort = 0
			}
		case h.Ports != nil:
			port, err := h.Ports.AllocateFor(r.Context(), ws.ServerID.String(), ws.ID.String(), mode, ws.BackendPort)
			if err != nil {
				httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
				return
			}
			ws.BackendPort = port
		}
	}

	// --- docroot override (framework layouts, e.g. Laravel "public") ---
	{
		// normalize: short relative path, no traversal. Empty resets to the
		// standard public dir.
		suffix := strings.Trim(strings.TrimSpace(req.DocrootSuffix), "/")
		if suffix != "" {
			if strings.Contains(suffix, "..") || strings.ContainsAny(suffix, "\\\x00") || strings.Count(suffix, "/") > 3 || len(suffix) > 100 {
				httpapi.RespondError(w, httpapi.ErrValidation("docroot_suffix must be a short relative path like \"public\""))
				return
			}
			for _, seg := range strings.Split(suffix, "/") {
				if seg == "" || seg == "." || seg == ".." {
					httpapi.RespondError(w, httpapi.ErrValidation("docroot_suffix contains an invalid segment"))
					return
				}
			}
		}
		if suffix != ws.DocrootSuffix {
			if err := h.Websites.SetDocrootSuffix(r.Context(), ws.ID, suffix); err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
			ws.DocrootSuffix = suffix
		}
	}

	// --- runtime change (e.g. static → php, php 8.3 → 8.4, php → node) ---
	if req.Runtime != "" && req.Runtime != string(ws.Runtime) {
		switch Runtime(req.Runtime) {
		case RuntimeStatic, RuntimePHP, RuntimeNode, RuntimePython, RuntimeGo:
		default:
			httpapi.RespondError(w, httpapi.ErrValidation("runtime must be one of: static, php, node, python, go"))
			return
		}
		if err := h.Websites.SetRuntime(r.Context(), ws.ID, Runtime(req.Runtime)); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		ws.Runtime = Runtime(req.Runtime)
		// Version no longer applies to the old stack; clear if mismatched type.
		if req.RuntimeVersion == "" {
			req.RuntimeVersion = "" // reset below via validation
			if ws.Runtime == RuntimeStatic {
				if err := h.Websites.SetRuntimeVersion(r.Context(), ws.ID, ""); err != nil {
					httpapi.RespondError(w, httpapi.ErrInternal(err))
					return
				}
			}
		}
	}

	newVersion := req.RuntimeVersion
	if newVersion == "" {
		newVersion = ws.RuntimeVersion
	}
	if ws.Runtime == RuntimeStatic {
		newVersion = ""
		if req.RuntimeVersion != "" && req.Runtime == "" {
			httpapi.RespondError(w, httpapi.ErrValidation("static websites have no runtime version"))
			return
		}
	} else {
		if newVersion == "" {
			httpapi.RespondError(w, httpapi.ErrValidation("runtime_version is required for "+string(ws.Runtime)))
			return
		}
		changedVersion := newVersion != ws.RuntimeVersion || req.Runtime != ""
		if changedVersion {
			ref, err := h.Runtimes.GetByTypeVersion(r.Context(), ws.ServerID, string(ws.Runtime), newVersion)
			if err != nil {
				httpapi.RespondError(w, httpapi.ErrValidation("runtime "+string(ws.Runtime)+" "+newVersion+" is not installed on this server"))
				return
			}
			if ref.Status != "available" {
				httpapi.RespondError(w, httpapi.ErrValidation("runtime "+string(ws.Runtime)+" "+newVersion+" is not available (status: "+ref.Status+")"))
				return
			}
		}
	}
	if newVersion != ws.RuntimeVersion {
		if err := h.Websites.SetRuntimeVersion(r.Context(), ws.ID, newVersion); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		ws.RuntimeVersion = newVersion
	}

	payload, apiErr := h.buildDesiredPayload(r.Context(), ws, orgID, ws.UnixUser, newVersion)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	job, err := h.Jobs.EnqueueIdempotent(r.Context(), ws.ServerID, &ws.ID, jobs.TypeProvisionWebsite, payload, "provision_website_"+ws.ID.String())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	h.auditUser(r, &orgID, "website.updated", "website", ws.ID.String(), map[string]any{
		"runtime": string(ws.Runtime), "runtime_version": ws.RuntimeVersion, "web_server": ws.WebServer,
	})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID})
}

// validateWebServerList validates the web server selection. Exactly the three
// nginx-based modes are supported; nginx is always the public edge and owns
// :80/:443. Apache/OLS never bind public ports — they run as private backends
// on loopback with a panel-allocated port.
func validateWebServerList(s string) ([]string, *httpapi.APIError) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(strings.ToLower(part))
		if p == "" {
			continue
		}
		switch p {
		case "nginx", "apache", "openlitespeed":
			if seen[p] {
				return nil, httpapi.ErrValidation("duplicate web server: " + p)
			}
			seen[p] = true
			out = append(out, p)
		case "none":
			return nil, httpapi.ErrValidation("web_server none cannot be combined; use just \"none\"")
		default:
			return nil, httpapi.ErrValidation("web server must be one of: nginx, apache, openlitespeed (comma-separated for combos)")
		}
	}
	if len(out) == 0 {
		return nil, httpapi.ErrValidation("at least one web server is required")
	}
	// nginx must be the edge; standalone apache/ols would fight nginx for :80.
	if (seen["apache"] || seen["openlitespeed"]) && !seen["nginx"] {
		return nil, httpapi.ErrValidation("apache/openlitespeed require nginx in front: use \"nginx,apache\" or \"nginx,openlitespeed\"")
	}
	return out, nil
}

// limitsForPackage adapts the PackageRef to the limits seam. Single seam:
// every package -> pool-limit mapping goes through limits.ForPackage.
func limitsForPackage(pkg *PackageRef) limits.PoolLimits {
	return limits.ForPackage(&packages.Package{MemoryLimitMB: pkg.MemoryLimitMB, CPUCores: pkg.CPUCores})
}

// backendMode returns "apache"/"openlitespeed" for proxy modes, "" for nginx.
func backendMode(s string) string {
	switch s {
	case "nginx,apache":
		return "apache"
	case "nginx,openlitespeed":
		return "openlitespeed"
	}
	return ""
}

// DELETE /v1/organizations/{org_id}/websites/{website_id} — enqueues a delete job.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if ws.Status == StatusDeleted {
		httpapi.RespondError(w, httpapi.ErrConflict("website is already deleted"))
		return
	}
	if ws.Status == StatusDeleting {
		httpapi.RespondError(w, httpapi.ErrConflict("website deletion already in progress"))
		return
	}

	payload, apiErr := h.buildDesiredPayload(r.Context(), ws, orgID, ws.UnixUser, ws.RuntimeVersion)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeDeleteWebsite, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.Websites.SetStatus(r.Context(), ws.ID, StatusDeleting, ""); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	h.auditUser(r, &orgID, "website.delete_requested", "website", ws.ID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/organizations/{org_id}/websites/{website_id}/suspend — enqueues a
// suspend job. Status flips to suspended only on agent success (retryable,
// idempotent, reversible via resume).
func (h *Handler) Suspend(w http.ResponseWriter, r *http.Request) {
	h.enqueueLifecycle(w, r, lifecycleOp{
		jobType:      TypeSuspendWebsite,
		idemPrefix:   "suspend_",
		auditAction:  "website.suspend",
		expectStatus: StatusReady,
		conflictMsg:  "only ready websites can be suspended",
	})
}

// POST /v1/organizations/{org_id}/websites/{website_id}/resume — enqueues a
// resume job for a suspended website.
func (h *Handler) Resume(w http.ResponseWriter, r *http.Request) {
	h.enqueueLifecycle(w, r, lifecycleOp{
		jobType:      TypeResumeWebsite,
		idemPrefix:   "resume_",
		auditAction:  "website.resume",
		expectStatus: StatusSuspended,
		conflictMsg:  "only suspended websites can be resumed",
	})
}

// lifecycleOp parameterizes the suspend/resume enqueue flow.
type lifecycleOp struct {
	jobType      jobs.Type
	idemPrefix   string
	auditAction  string
	expectStatus Status
	conflictMsg  string
}

// enqueueLifecycle validates the current status, enqueues the idempotent
// lifecycle job and writes the audit entry. At enqueue-time nothing changes:
// the transition happens on agent success via ApplyWebsiteTransition.
func (h *Handler) enqueueLifecycle(w http.ResponseWriter, r *http.Request, op lifecycleOp) {
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
	if ws.Status != op.expectStatus {
		httpapi.RespondError(w, httpapi.ErrConflict(op.conflictMsg))
		return
	}
	var payload any
	switch op.jobType {
	case TypeSuspendWebsite:
		payload = SuspendPayload{WebsiteID: ws.ID.String()}
	case TypeResumeWebsite:
		payload = ResumePayload{WebsiteID: ws.ID.String()}
	}
	job, err := h.Jobs.EnqueueIdempotent(r.Context(), ws.ServerID, &ws.ID, op.jobType, payload, op.idemPrefix+ws.ID.String())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, op.auditAction, "website", ws.ID.String(), map[string]any{"job_id": job.ID.String()})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID})
}

// GET /v1/organizations/{org_id}/websites/{website_id}/jobs
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
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
	list, err := h.Jobs.ListForWebsite(r.Context(), ws.ID, 20)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []jobs.Job{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": list})
}

// buildDesiredPayload assembles the agent payload including the current
// domain serving list (primary + aliases with SSL modes/cert paths) and the
// per-site rewrite rules snippet.
func (h *Handler) buildDesiredPayload(ctx context.Context, ws *Website, orgID uuid.UUID, unixUser, runtimeVersion string) (DesiredPayload, *httpapi.APIError) {
	payload := DesiredPayload{
		WebsiteID:      ws.ID,
		Organization:   orgID.String(),
		Name:           ws.Name,
		UnixUser:       unixUser,
		Runtime:        string(ws.Runtime),
		RuntimeVersion: runtimeVersion,
		WebServer:      ws.WebServer,
		BackendPort:    ws.BackendPort,
		DocrootSuffix:  ws.DocrootSuffix,
		PrimaryDomain:  ws.PrimaryDomain,
	}
	// App-platform serving mode: the agent renders a reverse-proxy vhost to
	// the app port and (via chaining) builds + starts the process.
	if isAppRuntime(ws.Runtime) {
		payload.AppStartupCommand = ws.AppStartupCommand
		payload.AppBuildCommand = ws.AppBuildCommand
		payload.AppPort = ws.AppPort
		if blob, err := h.Websites.GetAppEnv(ctx, ws.ID); err == nil && len(blob) > 0 {
			payload.AppEnvEnc = base64.StdEncoding.EncodeToString(blob)
		}
	}
	if h.Configs != nil {
		if cfg, err := h.Configs.Get(ctx, ws.ID); err == nil {
			payload.RewriteRules = cfg.RewriteRules
		}
	}
	if h.Domains != nil {
		list, err := h.Domains.ListForWebsiteServing(ctx, ws.ID)
		if err != nil {
			return payload, httpapi.ErrInternal(err)
		}
		for _, d := range list {
			payload.Domains = append(payload.Domains, DesiredDomain{
				Domain: d.Domain, SSLMode: d.SSLMode, CertPath: d.CertPath, KeyPath: d.KeyPath,
				DocrootSuffix: d.DocrootSuffix,
			})
		}
	}
	if h.Redirects != nil {
		if targets, err := h.Redirects.ListForWebsiteServing(ctx, ws.ID); err == nil {
			payload.Redirects = targets
		}
	}
	// FPM pool sizing: the single call-site for package -> pool limits (the
	// limits seam). Missing package or zero values leave agent defaults.
	// Free Perk sites size their pool from the free_perk package row instead
	// of the org plan (the perk replaces the plan for this site).
	perkApplied := false
	if ws.FreePerk && h.FreePerkPackage != nil {
		if pkg, ok := h.FreePerkPackage(ctx); ok && pkg != nil {
			pl := limitsForPackage(&PackageRef{MemoryLimitMB: pkg.MemoryLimitMB, CPUCores: pkg.CPUCores})
			payload.FpmMemoryLimitMB = pl.MemoryLimitMB
			payload.FpmMaxChildren = pl.MaxChildren
			perkApplied = true
		}
	}
	if !perkApplied && h.PackageForOrg != nil {
		if pkg, err := h.PackageForOrg(ctx, orgID); err == nil && pkg != nil {
			pl := limitsForPackage(pkg)
			payload.FpmMemoryLimitMB = pl.MemoryLimitMB
			payload.FpmMaxChildren = pl.MaxChildren
		}
	}
	// Application process port (node/python/go): when the site has an
	// application configured, the web server proxies to it. Filled on every
	// desired-state build so any reconcile converges the proxy vhost.
	if h.AppPortLookup != nil {
		if port, ok := h.AppPortLookup(ctx, ws.ID); ok {
			payload.AppPort = port
		}
	}
	// Per-site PHP INI overrides (MultiPHP INI Editor). The FPM request
	// watchdog is derived from max_execution_time so an "unlimited" script
	// does not get killed early.
	if h.PHPSettings != nil && ws.Runtime == RuntimePHP {
		if settings, err := h.PHPSettings.Get(ctx, ws.ID); err == nil {
			payload.PHPSettings = settings
			payload.RequestTerminateTimeout = requestTimeoutFor(settings)
		}
	}
	return payload, nil
}

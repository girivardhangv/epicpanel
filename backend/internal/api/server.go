package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/apitokens"
	"github.com/epicbyte/epicpanel/backend/internal/apps"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/backups"
	"github.com/epicbyte/epicpanel/backend/internal/config"
	"github.com/epicbyte/epicpanel/backend/internal/crons"
	"github.com/epicbyte/epicpanel/backend/internal/databases"
	"github.com/epicbyte/epicpanel/backend/internal/deployments"
	"github.com/epicbyte/epicpanel/backend/internal/dns"
	"github.com/epicbyte/epicpanel/backend/internal/domains"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/ftpaccounts"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/metrics"
	"github.com/epicbyte/epicpanel/backend/internal/monitoring"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/packages"
	"github.com/epicbyte/epicpanel/backend/internal/resourcelimits"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
	"github.com/epicbyte/epicpanel/backend/internal/runtimes"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/epicbyte/epicpanel/backend/internal/settings"

	agentpkg "github.com/epicbyte/epicpanel/backend/internal/agent"
	"github.com/epicbyte/epicpanel/backend/internal/sshkeys"
	"github.com/epicbyte/epicpanel/backend/internal/terminal"
	"github.com/epicbyte/epicpanel/backend/internal/users"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
	"github.com/jackc/pgx/v5/pgxpool"
)

const healthCheckTimeout = 2 * time.Second

type Server struct {
	Cfg         config.Config
	Pool        *pgxpool.Pool
	Sessions    *auth.SessionStore
	Users       *users.Store
	Orgs        *organizations.Store
	Servers     *servers.Store
	Websites    *websites.Store
	Jobs        *jobs.Store
	Runtimes    *runtimes.Store
	Databases   *databases.Store
	Domains     *domains.Store
	Packages    *packages.Store
	Crons       *crons.Store
	Apps        *apps.Store
	SSHKeys     *sshkeys.Store
	TerminalHub *terminal.Hub
	Settings    *settings.Store
	Deployments *deployments.Store
	Backups     *backups.Store
	Tokens      *apitokens.Store
	Limiter     *httpapi.TokenBucket
	Audit       *audit.Store
	WPPending   *websites.WPPendingStore
	MFA         *auth.MFAStore
	Accounts    *apitokens.ServiceAccountStore
	Events      *events.Bus
	WSHub       *events.Hub
	LiveStore   *metrics.LiveStore
	History     *metrics.Writer
	FTPAccounts *ftpaccounts.Store
	DNS         *dns.Store
	// ResourceLimits is the Phase 9 unified resource engine adapter: plan
	// limits, enforce payloads and count gates all resolve through it.
	ResourceLimits *resourcelimits.Engine
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	authH := &auth.Handler{Users: s.Users, Sessions: s.Sessions, Audit: s.Audit, Cfg: s.Cfg,
		SetupDone: s.setupCompleted, MFA: s.MFA}
	mfaH := &auth.MFAHandler{Users: s.Users, MFA: s.MFA, Sessions: s.Sessions, Audit: s.Audit, Cfg: s.Cfg}
	mux.HandleFunc("POST /v1/auth/register", authH.Register)
	mux.HandleFunc("POST /v1/auth/login", authH.Login)
	mux.HandleFunc("POST /v1/auth/logout", authH.Logout)
	mux.HandleFunc("GET /v1/auth/me", httpapi.RequireUser(authH.Me))
	mux.HandleFunc("POST /v1/auth/mfa/setup", httpapi.RequireUser(mfaH.Setup))
	mux.HandleFunc("POST /v1/auth/mfa/enable", httpapi.RequireUser(mfaH.Enable))
	mux.HandleFunc("POST /v1/auth/mfa/disable", httpapi.RequireUser(mfaH.Disable))
	mux.HandleFunc("POST /v1/auth/mfa/verify", mfaH.Verify)

	orgH := &organizations.Handler{Store: s.Orgs, Audit: s.Audit}
	orgH.Register(mux)

	srvH := &servers.Handler{Store: s.Servers, Orgs: s.Orgs, Jobs: s.Jobs, Audit: s.Audit, Live: s.LiveStore}
	srvH.OnStreamEvent = func(serverID uuid.UUID, online bool) {
		s.WSHub.BroadcastServerState(serverID.String(), online)
	}
	srvH.Register(mux)

	// Live job feed for a server (software install progress on the dashboard).
	mux.HandleFunc("GET /v1/organizations/{org_id}/servers/{server_id}/jobs", httpapi.RequireUser(func(w http.ResponseWriter, r *http.Request) {
		// Membership auth only — the server fleet is shared across orgs.
		if _, apiErr := srvH.ResolveOrg(r, r.PathValue("org_id"), organizations.RoleBilling); apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		serverID, err := uuid.Parse(r.PathValue("server_id"))
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid server id"))
			return
		}
		if _, err := s.Servers.GetByID(r.Context(), serverID); err == servers.ErrNotFound {
			httpapi.RespondError(w, httpapi.ErrNotFound("server not found"))
			return
		} else if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		var types []string
		if t := r.URL.Query().Get("types"); t != "" {
			types = strings.Split(t, ",")
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := s.Jobs.ListRecentForServer(r.Context(), serverID, types, limit)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if list == nil {
			list = []jobs.Job{}
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": list})
	}))

	wsH := &websites.Handler{
		Websites:       s.Websites,
		Jobs:           s.Jobs,
		Configs:        &websites.ConfigStore{Pool: s.Pool},
		Orgs:           s.Orgs,
		Servers:        s.Servers,
		Audit:          s.Audit,
		Runtimes:       runtimeChecker{s.Runtimes},
		Domains:        domainLister{s.Domains},
		Redirects:      redirectLister{s.Domains},
		Ports:          &websites.BackendPortAllocator{Websites: s.Websites},
		LimitsFor:      s.siteLimitsFor,
		PackageChecker: packageGate{Packages: s.Packages},
		PackageForOrg: func(ctx context.Context, orgID uuid.UUID) (*websites.PackageRef, error) {
			p, err := s.Packages.ForOrg(ctx, orgID)
			if err != nil {
				return nil, err
			}
			return &websites.PackageRef{MemoryLimitMB: p.MemoryLimitMB, CPUCores: p.CPUCores}, nil
		},
		RequireOrg: srvH.ResolveOrg,
	}
	wsH.Register(mux, srvH.RequireAgent)
	wsH.Ext = &websites.HandlerExtensions{
		Jobs: s.Jobs,
		Databases: func(ctx context.Context, websiteID uuid.UUID) ([]websites.StagingDBRef, error) {
			refs, err := s.websiteDatabases(ctx, websiteID)
			if err != nil {
				return nil, err
			}
			out := make([]websites.StagingDBRef, 0, len(refs))
			for _, ref := range refs {
				out = append(out, websites.StagingDBRef{Engine: ref.Engine, Name: ref.Name, User: ref.User})
			}
			return out, nil
		},
	}
	// Staging endpoints on the websites handler.
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/staging", httpapi.RequireUser(func(w http.ResponseWriter, r *http.Request) {
		orgID, apiErr := srvH.ResolveOrg(r, r.PathValue("org_id"), organizations.RoleAdmin)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		wsH.CreateStaging(w, r.WithContext(context.WithValue(r.Context(), websites.OrgKeyType{}, orgID)))
	}))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/promote", httpapi.RequireUser(func(w http.ResponseWriter, r *http.Request) {
		orgID, apiErr := srvH.ResolveOrg(r, r.PathValue("org_id"), organizations.RoleAdmin)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		wsH.PromoteStaging(w, r.WithContext(context.WithValue(r.Context(), websites.OrgKeyType{}, orgID)))
	}))

	rtH := &runtimes.Handler{
		Runtimes:   s.Runtimes,
		Extensions: &runtimes.ExtensionStore{Pool: s.Pool},
		Jobs:       s.Jobs,
		Orgs:       s.Orgs,
		Servers:    s.Servers,
		Audit:      s.Audit,
		RequireOrg: srvH.ResolveOrg,
	}
	rtH.Register(mux)

	dbH := &databases.Handler{
		Databases:       s.Databases,
		Jobs:            s.Jobs,
		SSOKey:          s.dbadminSSOKey,
		SettingsStore:   s.Settings,
		OnDatabaseReady: s.onDatabaseReady,
		Orgs:            s.Orgs,
		Servers:         s.Servers,
		Audit:           s.Audit,
		PackageChecker:  dbGate{Packages: s.Packages},
		RequireOrg:      srvH.ResolveOrg,
	}
	dbH.Register(mux)

	domH := &domains.Handler{
		Domains:    s.Domains,
		Jobs:       s.Jobs,
		Orgs:       s.Orgs,
		Websites:   s.Websites,
		Audit:      s.Audit,
		RequireOrg: srvH.ResolveOrg,
		OnDomainsChanged: func(ctx context.Context, websiteID, orgID, serverID uuid.UUID) {
			s.reconcileWebsiteServing(ctx, websiteID, orgID, serverID)
		},
	}
	domH.Register(mux)

	// DNS zones/records (Phase 4): panel-managed authoritative zones.
	dnsH := &dns.Handler{
		Store:       s.DNS,
		Jobs:        s.Jobs,
		Websites:    s.Websites,
		Audit:       s.Audit,
		DomainCheck: s.Domains.DomainBelongsToWebsite,
		RequireOrg:  srvH.ResolveOrg,
	}
	dnsH.Register(mux)

	// FTP/SFTP accounts (Phase 4): chrooted per-account access.
	ftpH := &ftpaccounts.Handler{
		Accounts:   s.FTPAccounts,
		Jobs:       s.Jobs,
		Websites:   s.Websites,
		Audit:      s.Audit,
		RequireOrg: srvH.ResolveOrg,
	}
	ftpH.Register(mux)

	// SSL lifecycle events (Phase 4): issue/failed/renewal on the event bus.
	domains.OnSSLEvent = func(event, domain string, meta map[string]any) {
		var orgID *uuid.UUID
		if err := s.Pool.QueryRow(context.Background(),
			`SELECT organization_id FROM domains WHERE domain = $1`, domain).Scan(&orgID); err != nil {
			orgID = nil
		}
		s.Events.Publish(context.Background(), events.Event{
			Type:         event,
			Organization: orgID,
			ResourceType: "domain",
			ResourceID:   domain,
			Payload:      meta,
		})
	}

	depH := &deployments.Handler{
		Deployments: s.Deployments,
		Jobs:        s.Jobs,
		Websites:    s.Websites,
		Audit:       s.Audit,
		RequireOrg:  srvH.ResolveOrg,
	}
	depH.Register(mux)

	bkH := &backups.Handler{
		Backups:    s.Backups,
		Jobs:       s.Jobs,
		Websites:   s.Websites,
		Audit:      s.Audit,
		RequireOrg: srvH.ResolveOrg,
		DatabasesForWebsite: func(ctx context.Context, websiteID uuid.UUID) ([]backups.DBRef, error) {
			return s.websiteDatabases(ctx, websiteID)
		},
		// Phase 9: create-time plan gate (Backups count) over the unified engine.
		Limits: resourcelimits.CountGate{
			Engine:  s.ResourceLimits,
			Counter: s.CountResource,
		},
	}
	bkH.Register(mux)

	monH := &monitoring.Handler{
		Pool:       s.Pool,
		RequireOrg: srvH.ResolveOrg,
	}
	monH.Register(mux)

	tokH := &apitokens.Handler{
		Tokens:     s.Tokens,
		Orgs:       s.Orgs,
		Audit:      s.Audit,
		RequireOrg: srvH.ResolveOrg,
	}
	tokH.Register(mux)

	adminUsers := &users.AdminHandler{Users: s.Users}
	adminUsers.Register(mux)

	pkgH := &packages.Handler{
		Packages:   s.Packages,
		Jobs:       s.Jobs,
		Audit:      s.Audit,
		RequireOrg: srvH.ResolveOrg,
		// Phase 9: plan assignment converges sites via the unified engine.
		EnforceForSite: func(ctx context.Context, orgID, websiteID uuid.UUID) error {
			payload, err := s.ResourceLimits.EnforcePayloadFor(ctx, orgID, websiteID)
			if err != nil {
				return err
			}
			_, err = s.Jobs.EnqueueForWebsite(ctx, websiteID, jobs.TypeEnforceLimits, agentpkg.EnforceJobPayload{
				WebsiteID:      payload.WebsiteID,
				Plan:           payload.Plan,
				CPUPercent:     payload.CPUPercent,
				MemoryMB:       payload.MemoryMB,
				DiskMB:         payload.DiskMB,
				BandwidthMB:    payload.BandwidthMB,
				IOWeight:       resources.ClampIOWeight(payload.IOWeight),
				PidsMax:        payload.PidsMax,
				FpmMaxChildren: payload.FpmMaxChildren,
				CountLimits:    payload.CountLimits,
			})
			return err
		},
	}
	pkgH.Register(mux)

	cronH := &crons.Handler{
		Crons:      s.Crons,
		Jobs:       s.Jobs,
		Websites:   s.Websites,
		Audit:      s.Audit,
		RequireOrg: srvH.ResolveOrg,
	}
	cronH.Register(mux)

	sshH := &sshkeys.Handler{
		Keys:       s.SSHKeys,
		Jobs:       s.Jobs,
		Websites:   s.Websites,
		Audit:      s.Audit,
		RequireOrg: srvH.ResolveOrg,
	}
	sshH.Register(mux)

	appH := &apps.Handler{
		Apps:       s.Apps,
		Jobs:       s.Jobs,
		Websites:   s.Websites,
		Audit:      s.Audit,
		RequireOrg: srvH.ResolveOrg,
	}
	appH.Register(mux)

	setupH := &settings.SetupHandler{Settings: s.Settings, Servers: s.Servers, Jobs: s.Jobs}
	mux.HandleFunc("GET /v1/setup/status", setupH.Status)
	mux.HandleFunc("POST /v1/setup", setupH.Complete)
	mux.HandleFunc("POST /v1/setup/verify-hostname", setupH.VerifyHostname)
	mux.HandleFunc("GET /v1/setup/software", setupH.Software)
	mux.HandleFunc("POST /v1/setup/software", setupH.SoftwareInstall)
	mux.HandleFunc("GET /v1/setup/jobs", setupH.SetupJobs)
	mux.HandleFunc("GET /v1/settings", httpapi.RequireUser(setupH.GetSettings))
	mux.HandleFunc("PATCH /v1/settings/hostname", httpapi.RequireUser(setupH.SetHostname))
	mux.HandleFunc("GET /v1/pma-gate", s.pmaGate)

	termH := &terminal.Handler{
		Hub:        s.TerminalHub,
		Websites:   s.Websites,
		RequireOrg: srvH.ResolveOrg,
	}
	termH.Register(mux)

	// WordPress one-click route (org-scoped, developer+).
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/wordpress", httpapi.RequireUser(func(w http.ResponseWriter, r *http.Request) {
		orgID, apiErr := srvH.ResolveOrg(r, r.PathValue("org_id"), organizations.RoleDeveloper)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		wsH.InstallWordPress(w, r.WithContext(context.WithValue(r.Context(), websites.OrgKeyType{}, orgID)))
	}))

	// Fanout: every finished job is routed to the subsystems that own its
	// state machine (websites lifecycle, runtime registry, databases).
	wsH.OnJobFinished = func(ctx context.Context, job *jobs.Job, result json.RawMessage) {
		wsH.ApplyWebsiteTransition(job, result)
		s.Runtimes.ApplyJobOutcome(ctx, job, job.Error)
		dbH.ApplyJobOutcome(ctx, job, result)
		depH.ApplyJobOutcome(job, result)
		bkH.ApplyJobOutcome(ctx, job, result)
		rtH.ApplyExtensionJobOutcome(ctx, job)
		rtH.AdoptDetectedSoftware(ctx, job, result)
		wsH.ApplySiteUsageOutcome(job, result)
		s.applyEnforceOutcome(ctx, job, result)
		dns.ApplyZonePublishOutcome(ctx, s.DNS, job, result)
		ftpaccounts.ApplySyncOutcome(ctx, s.FTPAccounts, job, result)
		if changedWebsite := s.Domains.ApplyJobOutcome(ctx, job, result); changedWebsite != uuid.Nil {
			// A certificate became active/failed: converge the vhost.
			if ws, err := s.Websites.GetByIDAny(ctx, changedWebsite); err == nil && ws != nil {
				s.reconcileWebsiteServing(ctx, ws.ID, ws.Organization, ws.ServerID)
			}
		}
	}
	wsH.RegisterPrimaryDomain = func(ctx context.Context, orgID, websiteID uuid.UUID, domain string) error {
		_, err := s.Domains.Create(ctx, orgID, websiteID, domain, domains.KindPrimary)
		return err
	}
	wsH.PickServer = func(ctx context.Context, orgID uuid.UUID, runtime, runtimeVersion string) (uuid.UUID, error) {
		return s.Servers.AutoPickServer(ctx, runtime, runtimeVersion)
	}
	wsH.OnJobClaimed = func(ctx context.Context, job *jobs.Job) {
		switch job.Type {
		case jobs.TypeDeployWebsite, jobs.TypeRollbackWebsite:
			depH.MarkJobClaimed(ctx, job)
		case jobs.TypeCreateBackup:
			bkH.MarkJobClaimed(ctx, job)
		}
	}

	mux.HandleFunc("GET /v1/audit-logs", httpapi.RequireUser(s.listAuditLogs))

	// Service accounts (RBAC v2 machine principals).
	saH := &apitokens.ServiceAccountHandler{Accounts: s.Accounts, Tokens: s.Tokens, Audit: s.Audit, RequireOrg: srvH.ResolveOrg}
	saH.Register(mux)

	// WebSocket event stream (session or API-token auth; org-scoped frames).
	mux.HandleFunc("GET /v1/ws", httpapi.RequireUser(func(w http.ResponseWriter, r *http.Request) {
		user, _ := httpapi.UserFrom(r.Context())
		s.WSHub.HandleWS(w, r, user.ID, user.Role == "admin", httpapi.IsAPIToken(r.Context()), httpapi.TokenOrgID(r.Context()))
	}))

	// Dead-letter / recent jobs visibility (platform admin, session only).
	mux.HandleFunc("GET /v1/jobs", httpapi.RequireUser(func(w http.ResponseWriter, r *http.Request) {
		user, _ := httpapi.UserFrom(r.Context())
		if user.Role != "admin" || httpapi.IsAPIToken(r.Context()) {
			httpapi.RespondError(w, httpapi.ErrForbidden("platform admin session required"))
			return
		}
		status := r.URL.Query().Get("status")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		list, err := s.Jobs.ListByStatus(r.Context(), status, limit)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if list == nil {
			list = []jobs.Job{}
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": list})
	}))

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /v1/openapi.json", s.openAPI)

	// Request flow (inside-out): ScopeEnforce -> CSRF -> SessionAuth ->
	// CORS -> RateLimit -> RequestLog -> APIPrefixRewrite -> mux. The prefix
	// rewrite is outermost so /api/v1 and /v1 share every route below.
	var h http.Handler = mux
	h = httpapi.ScopeEnforce(h)
	h = httpapi.CSRFGuard(h)
	h = auth.SessionMiddleware(s.Users, s.Sessions, s.Tokens, h)
	h = httpapi.CORSMiddleware(s.Cfg.CORSOrigins, h)
	h = httpapi.RateLimit(s.Limiter, h)
	h = httpapi.RequestLog(h)
	h = httpapi.APIPrefixRewrite(h)
	return h
}

// setupCompleted reports whether first-boot setup has finished; used to lock
// down public registration (cPanel model: admins create accounts).
func (s *Server) setupCompleted(ctx context.Context) bool {
	if s.Settings == nil {
		return false
	}
	return s.Settings.GetBool(ctx, "setup_completed")
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
	defer cancel()
	if err := s.Pool.Ping(ctx); err != nil {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded"})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is the deployment gate: liveness (healthz) says the process runs,
// readiness says it can serve — database reachable.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
	defer cancel()
	if err := s.Pool.Ping(ctx); err != nil {
		httpapi.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "database": "down"})
		return
	}
	resp := map[string]string{"status": "ready", "database": "up"}
	if s.Events != nil && s.Events.Driver != nil {
		resp["events_driver"] = "enabled"
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

// listAuditLogs returns audit logs for an organization. Requires platform-admin
// role or an organization membership (org-scoped, minimum billing visibility).
func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	user, _ := httpapi.UserFrom(r.Context())

	orgIDParam := r.URL.Query().Get("organization_id")
	if orgIDParam == "" {
		if user.Role != "admin" {
			httpapi.RespondError(w, httpapi.ErrValidation("organization_id query parameter is required"))
			return
		}
		rows, err := s.Audit.List(r.Context(), nil, 50, 0)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		writeLogs(w, rows)
		return
	}

	orgID, err := uuid.Parse(orgIDParam)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization_id"))
		return
	}

	uid, err := uuid.Parse(user.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	if user.Role != "admin" {
		role, err := s.Orgs.RoleFor(r.Context(), orgID, uid)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if role == "" {
			httpapi.RespondError(w, httpapi.ErrNotFound("organization not found"))
			return
		}
		if organizations.RoleRank[role] < organizations.RoleRank[organizations.RoleBilling] {
			httpapi.RespondError(w, httpapi.ErrForbidden("insufficient organization role"))
			return
		}
	}

	rows, err := s.Audit.List(r.Context(), &orgID, 50, 0)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	writeLogs(w, rows)
}

func writeLogs(w http.ResponseWriter, rows []audit.LogRow) {
	if rows == nil {
		rows = []audit.LogRow{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"logs": rows})
}

// runtimeChecker adapts runtimes.Store to websites.RuntimeChecker, keeping the
// two domain packages decoupled.
type runtimeChecker struct {
	store *runtimes.Store
}

func (c runtimeChecker) GetByTypeVersion(ctx context.Context, serverID uuid.UUID, t string, version string) (websites.RuntimeRef, error) {
	rt, err := c.store.GetByTypeVersion(ctx, serverID, runtimes.Type(t), version)
	if err != nil {
		return websites.RuntimeRef{}, err
	}
	return websites.RuntimeRef{ID: rt.ID, Status: string(rt.Status)}, nil
}

// domainLister adapts domains.Store to websites.DomainLister.
type domainLister struct {
	store *domains.Store
}

func (l domainLister) ListForWebsiteServing(ctx context.Context, websiteID uuid.UUID) ([]websites.DomainServing, error) {
	list, err := l.store.ListForWebsiteServing(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	out := make([]websites.DomainServing, 0, len(list))
	for _, d := range list {
		out = append(out, websites.DomainServing{Domain: d.Domain, SSLMode: d.SSLMode, CertPath: d.CertPath, KeyPath: d.KeyPath, DocrootSuffix: d.DocrootSuffix})
	}
	return out, nil
}

// redirectLister adapts the domains store to the websites handler's
// redirect-rendering contract.
type redirectLister struct {
	store *domains.Store
}

func (l redirectLister) ListForWebsiteServing(ctx context.Context, websiteID uuid.UUID) ([]websites.DesiredRedirect, error) {
	targets, err := l.store.RedirectTargetsForWebsite(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	out := make([]websites.DesiredRedirect, 0, len(targets))
	for _, t := range targets {
		out = append(out, websites.DesiredRedirect{Domain: t.Domain, To: t.To, Status: t.Status})
	}
	return out, nil
}

// reconcileWebsiteServing re-enqueues a provision job so the vhost converges
// to the new domain list (aliases, SSL changes).
func (s *Server) reconcileWebsiteServing(ctx context.Context, websiteID, orgID, serverID uuid.UUID) {
	ws, err := s.Websites.GetByID(ctx, orgID, websiteID)
	if err != nil || ws == nil {
		return
	}
	if ws.Status != websites.StatusReady && ws.Status != websites.StatusFailed {
		return
	}
	payload := websites.DesiredPayload{
		WebsiteID:      ws.ID,
		Organization:   orgID.String(),
		Name:           ws.Name,
		UnixUser:       ws.UnixUser,
		Runtime:        string(ws.Runtime),
		RuntimeVersion: ws.RuntimeVersion,
		WebServer:      ws.WebServer,
		BackendPort:    ws.BackendPort,
		DocrootSuffix:  ws.DocrootSuffix,
		PrimaryDomain:  ws.PrimaryDomain,
	}
	if cfg, err := (&websites.ConfigStore{Pool: s.Pool}).Get(ctx, ws.ID); err == nil {
		payload.RewriteRules = cfg.RewriteRules
	}
	if list, err := s.Domains.ListForWebsiteServing(ctx, ws.ID); err == nil {
		for _, d := range list {
			payload.Domains = append(payload.Domains, websites.DesiredDomain{
				Domain: d.Domain, SSLMode: d.SSLMode, CertPath: d.CertPath, KeyPath: d.KeyPath,
				DocrootSuffix: d.DocrootSuffix,
			})
		}
	}
	if targets, err := s.Domains.RedirectTargetsForWebsite(ctx, ws.ID); err == nil {
		for _, t := range targets {
			payload.Redirects = append(payload.Redirects, websites.DesiredRedirect{
				Domain: t.Domain, To: t.To, Status: t.Status,
			})
		}
	}
	if _, err := s.Jobs.Enqueue(ctx, serverID, &ws.ID, jobs.TypeProvisionWebsite, payload); err != nil {
		slog.Error("vhost reconcile enqueue failed", "website", ws.ID, "err", err)
	}
}

// OrgKey is the context key websites staging handlers use (set by api wrappers).
// (websites.OrgKey alias is declared in the websites package.)

// websiteDatabases lists databases attached to a website for job payloads.
func (s *Server) websiteDatabases(ctx context.Context, websiteID uuid.UUID) ([]backups.DBRef, error) {
	rows, err := s.Pool.Query(ctx, `SELECT engine, name, db_user FROM databases WHERE website_id = $1 AND status = 'ready'`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []backups.DBRef
	for rows.Next() {
		var ref backups.DBRef
		if err := rows.Scan(&ref.Engine, &ref.Name, &ref.User); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// packageGate adapts packages.Store to websites.PackageLimits.
type packageGate struct {
	Packages *packages.Store
}

func (g packageGate) CheckSiteAllowed(ctx context.Context, orgID uuid.UUID, runtime, primaryDomain string) error {
	p, err := g.Packages.ForOrg(ctx, orgID)
	if err != nil {
		// Fail closed (audit): if the gate cannot be evaluated, deny the
		// mutation rather than silently bypassing package limits.
		return errors.New("package limits unavailable; site creation blocked")
	}
	u, err := g.Packages.UsageForOrg(ctx, orgID)
	if err != nil {
		return errors.New("package usage unavailable; site creation blocked")
	}
	if u.Websites >= p.MaxWebsites {
		return errors.New("package limit reached: " + p.Name + " allows " + itoa(p.MaxWebsites) + " websites")
	}
	// Domain bucket check: is the primary domain an addon (root domain) or a
	// subdomain? www.example.com counts as addon (example.com base).
	base := primaryDomain
	if labels := strings.Split(base, "."); len(labels) > 2 {
		// strip leading "www." (and one extra label for www.example.co.uk-style
		// ambiguity we accept as subdomain only for 3+ labels starting with www)
		if labels[0] == "www" && len(labels) > 3 {
			base = strings.Join(labels[1:], ".")
		}
	}
	isSub := len(strings.Split(base, ".")) > 2
	if isSub && p.MaxSubdomains > 0 && u.Subdomains >= p.MaxSubdomains {
		return errors.New("package limit reached: " + p.Name + " allows " + itoa(p.MaxSubdomains) + " subdomains")
	}
	if !isSub && p.MaxAddonDomains > 0 && u.AddonDomains >= p.MaxAddonDomains {
		return errors.New("package limit reached: " + p.Name + " allows " + itoa(p.MaxAddonDomains) + " addon domains")
	}
	for _, r := range p.AllowedRuntimes {
		if r == runtime {
			return nil
		}
	}
	return errors.New("runtime '" + runtime + "' is not allowed on the " + p.Name + " package")
}

// dbGate adapts packages.Store to databases.DBLimits.
type dbGate struct {
	Packages *packages.Store
}

func (g dbGate) CheckDatabaseAllowed(ctx context.Context, orgID uuid.UUID) error {
	// Phase 9: the limit number comes from the unified engine (the plan row
	// IS the matrix). Legacy error text preserved for existing clients.
	p, err := g.Packages.ForOrg(ctx, orgID)
	if err != nil {
		// Fail closed (audit) — see packageGate.
		return errors.New("package limits unavailable; database creation blocked")
	}
	u, err := g.Packages.UsageForOrg(ctx, orgID)
	if err != nil {
		return errors.New("package usage unavailable; database creation blocked")
	}
	limits, err := resourcelimits.New(g.Packages, g.Packages.Pool).LimitsForOrg(ctx, orgID)
	if err != nil {
		return errors.New("package limits unavailable; database creation blocked")
	}
	if err := resources.CheckCount(limits, resources.ResDatabases, u.Databases); err != nil {
		if reach, ok := err.(resources.ErrLimitReached); ok && reach.Current == u.Databases && reach.Limit == p.MaxDatabases {
			return errors.New("package limit reached: " + p.Name + " allows " + itoa(p.MaxDatabases) + " databases")
		}
		return err
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// dbProvisioner adapts databases.Store to websites.DBProvisioner for the
// WordPress flow (creates the DB row in creating state + enqueue job).
type dbProvisioner struct {
	dbs *databases.Store
	js  *jobs.Store
}

// onDatabaseReady chains WordPress installs: when the auto-created WP
// database reports its credential, enqueue the install job with the password
// encrypted (the agent holds the matching key). The plaintext exists only in
// memory here — the credential is scrubbed from the job result and encrypted
// at rest in the databases row.
func (s *Server) onDatabaseReady(ctx context.Context, dbID uuid.UUID, password string) {
	if s.WPPending == nil {
		return
	}
	db, err := s.Databases.GetByIDAny(ctx, dbID)
	if err != nil || db == nil || db.Purpose != "wordpress" || db.WebsiteID == nil {
		return
	}
	pend, err := s.WPPending.Get(ctx, dbID)
	if err != nil || pend == nil {
		return
	}
	ws, err := s.Websites.GetByIDAny(ctx, *db.WebsiteID)
	if err != nil || ws == nil {
		return
	}
	enc, err := secretbox.Encrypt(password)
	if err != nil {
		slog.Error("wp password encryption failed", "database", dbID, "err", err)
		return
	}
	// Honor a docroot override (framework layouts) when one is set.
	docRoot := ws.DocumentRoot
	if ws.DocrootSuffix != "" {
		docRoot = "/srv/epicpanel/websites/" + ws.ID.String() + "/" + strings.Trim(ws.DocrootSuffix, "/")
	}
	siteURL := "http://" + ws.PrimaryDomain
	if ws.PrimaryDomain == "" {
		siteURL = "http://localhost"
	}
	payload := map[string]any{
		"website_id":      ws.ID.String(),
		"unix_user":       ws.UnixUser,
		"document_root":   docRoot,
		"site_url":        siteURL,
		"title":           pend.Title,
		"admin_user":      pend.AdminUser,
		"admin_email":     pend.AdminEmail,
		"db_name":         db.Name,
		"db_user":         db.DBUser,
		"db_password_enc": base64.StdEncoding.EncodeToString(enc),
		"db_host":         "localhost",
	}
	if _, err := s.Jobs.Enqueue(ctx, db.ServerID, db.WebsiteID, jobs.TypeInstallWP, payload); err != nil {
		slog.Error("wp install enqueue failed", "database", dbID, "err", err)
		return
	}
	_ = s.WPPending.Delete(ctx, dbID)
	slog.Info("wordpress install chained", "website", ws.ID, "database", db.Name)
}

func (p dbProvisioner) CreateWPSiteDB(ctx context.Context, orgID, serverID, websiteID uuid.UUID, label string) (websites.DBInfo, error) {
	if len(label) > 30 {
		label = label[:30]
	}
	name, err := databases.DerivedName(orgID, label)
	if err != nil {
		return websites.DBInfo{}, err
	}
	user, err := databases.DerivedUser(orgID, label)
	if err != nil {
		return websites.DBInfo{}, err
	}
	createdBy := uuid.Nil
	db, err := p.dbs.Create(ctx, orgID, serverID, createdBy, &websiteID, databases.EngineMariaDB, name, user)
	if err != nil {
		return websites.DBInfo{}, err
	}
	_ = p.dbs.SetStatus(ctx, db.ID, databases.StatusCreating, "")
	_ = p.dbs.SetPurpose(ctx, db.ID, "wordpress")
	payload := databases.CreatePayload{DatabaseID: db.ID, Engine: "mariadb", Name: db.Name, DbUser: db.DBUser}
	if _, err := p.enqueueJob(ctx, serverID, websiteID, jobs.TypeCreateDatabase, payload); err != nil {
		return websites.DBInfo{}, err
	}
	return websites.DBInfo{ID: db.ID, Name: db.Name, User: db.DBUser, Status: string(databases.StatusCreating)}, nil
}

func (p dbProvisioner) enqueueJob(ctx context.Context, serverID, websiteID uuid.UUID, jobType jobs.Type, payload any) (*jobs.Job, error) {
	return p.js.Enqueue(ctx, serverID, &websiteID, jobType, payload)
}

// siteLimitsFor resolves plan limits for the per-site resource usage view:
// (memory cap bytes, cpu cores, disk allowance MB). Zero values = unlimited.
// Phase 9: routed through the unified engine so the displayed caps are the
// SAME numbers the agent enforces (cgroup memory.max = plan RAM, no
// headroom) — the anti-drift rule.
func (s *Server) siteLimitsFor(ctx context.Context, orgID, websiteID uuid.UUID) (int64, float64, int64) {
	if s.ResourceLimits == nil {
		return 0, 0, 0
	}
	limits, err := s.ResourceLimits.LimitsForOrg(ctx, orgID)
	if err != nil {
		return 0, 0, 0
	}
	var memBytes, diskMB int64
	var cpuCores float64
	if r, ok := limits.Get(resources.ResRAM); ok && r.Limit > 0 {
		memBytes = int64(r.Limit) * 1024 * 1024
	}
	if r, ok := limits.Get(resources.ResCPU); ok && r.Limit > 0 {
		cpuCores = r.Limit / 100
	}
	if r, ok := limits.Get(resources.ResDisk); ok && r.Limit > 0 {
		diskMB = int64(r.Limit)
	}
	return memBytes, cpuCores, diskMB
}

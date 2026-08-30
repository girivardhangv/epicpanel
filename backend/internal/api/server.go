// Package api wires the panel's HTTP surface. Handlers stay thin: decode,
// call a service, encode. Business rules live in the feature packages.
package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/config"
	"github.com/epicbyte/epicpanel/backend/internal/databases"
	"github.com/epicbyte/epicpanel/backend/internal/domains"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/installer"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/licensing"
	"github.com/epicbyte/epicpanel/backend/internal/metrics"
	"github.com/epicbyte/epicpanel/backend/internal/monitoring"
	"github.com/epicbyte/epicpanel/backend/internal/notifier"
	"github.com/epicbyte/epicpanel/backend/internal/rbac"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/epicbyte/epicpanel/backend/internal/users"
	"github.com/epicbyte/epicpanel/backend/internal/web"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
	"github.com/go-chi/chi/v5"
	mw "github.com/go-chi/chi/v5/middleware"
)

type Deps struct {
	Cfg      *config.Config
	Log      *slog.Logger
	Sessions *auth.Store
	Auth     *auth.Service
	RBAC     *rbac.Service
	Audit    *audit.Service
	Licensing licensing.Service
	Installer *installer.Service
	Servers  *servers.Service
	Settings *settings.Service
	UserManager *users.Manager

	// Phase 2 — hosting engine
	Agent    *agentclient.Client
	Domains  *domains.Service
	Websites *websites.Service
	Jobs     *jobs.Store

	// Phase 3 — monitoring & telemetry
	MonitoringIngest *monitoring.IngestService
	MonitoringQuery  *monitoring.QueryService
	Alerts           *monitoring.AlertService
	InternalMetrics  *metrics.Registry

	// Phase 5 — notifications
	Notifier *notifier.Service

	// Phase 6 — managed databases
	Databases *databases.Service

	Version    string
}

type Server struct {
	cfg       *config.Config
	log       *slog.Logger
	deps      *Deps
	authLimiter *httpx.RateLimiter
}

func NewServer(deps *Deps) *Server {
	return &Server{
		cfg:         deps.Cfg,
		log:         deps.Log,
		deps:        deps,
		authLimiter: httpx.NewRateLimiter(deps.Cfg.Security.LoginRatePerMinute, time.Minute),
	}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()

	// Internal observability first so every route is instrumented (§42).
	r.Use(func(next http.Handler) http.Handler { return metrics.Middleware(next) })

	r.Use(httpx.RequestID)
	r.Use(mw.Recoverer)
	r.Use(func(next http.Handler) http.Handler { return httpx.SecurityHeaders(s.cfg.IsProduction())(next) })
	r.Use(httpx.BodyLimit(s.cfg.Security.RequestBodyLimit))
	if s.cfg.Security.GlobalRatePerMinute > 0 {
		global := httpx.NewRateLimiter(s.cfg.Security.GlobalRatePerMinute, time.Minute)
		ipKey := s.ipKeyFn()
		r.Use(global.Middleware(ipKey))
	}

	s.mountPublic(r)
	s.mountV1(r)

	if !s.cfg.IsProduction() {
		// Dev-only echo webhook for testing notification delivery.
		r.HandleFunc("/dev/webhook", DevWebhookHandler)
	}

	if s.cfg.DistDir != "" {
		s.mountStatic(r, s.cfg.DistDir)
	} else {
		// Single-binary mode: serve the SPA embedded at build time.
		r.NotFound(web.Handler().ServeHTTP)
	}
	return r
}

func (s *Server) ipKeyFn() func(*http.Request) string {
	trusted := s.cfg.Server.TrustedProxy
	return func(req *http.Request) string { return httpx.ClientIP(req, trusted) }
}

// authenticated resolves the session cookie into an identity with fresh perms.
func (s *Server) authenticated(r *http.Request) (*auth.Identity, error) {
	token := auth.TokenFromRequest(r)
	if token == "" {
		return nil, auth.ErrNoSession
	}
	ctx := r.Context()
	id, err := s.deps.Sessions.Resolve(ctx, token, nil)
	if err != nil {
		return nil, err
	}
	perms, perr := s.deps.RBAC.PermissionsForUser(ctx, id.UserID)
	if perr != nil {
		return nil, perr
	}
	id.Permissions = perms
	s.deps.Sessions.Touch(ctx, id.SessionID)
	return id, nil
}

// injectIdentity attaches the identity when a valid cookie is present;
// anonymous requests simply continue.
func (s *Server) injectIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, err := s.authenticated(r); err == nil {
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mwRequireAuth rejects anonymous requests.
func (s *Server) mwRequireAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := auth.IdentityFrom(r.Context())
			if id == nil {
				Error(w, r, apierror.Unauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// mwCSRF protects unsafe methods once an identity exists.
func (s *Server) mwCSRF() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				id := auth.IdentityFrom(r.Context())
				if id != nil && !auth.CheckCSRF(r, id) {
					Error(w, r, apierror.CSRF)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// csrf wraps a handlerfunc with CSRF protection where applicable.
func (s *Server) csrf(h http.HandlerFunc) http.HandlerFunc {
	return s.mwCSRF()(http.HandlerFunc(h)).ServeHTTP
}

// requireAuth wraps a handlerfunc demanding an identity.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return s.mwRequireAuth()(http.HandlerFunc(h)).ServeHTTP
}

// RequirePermission enforces an RBAC code on the wrapped handler.
func (s *Server) RequirePermission(code string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := auth.IdentityFrom(r.Context())
		if id == nil {
			Error(w, r, apierror.Unauthorized)
			return
		}
		if !id.HasPermission(code) {
			s.deps.Audit.Log(r.Context(), audit.Entry{
				ActorType: "user", Label: id.Username,
				Action: "authz.denied", Resource: "permission", ResourceID: code,
				IP:         httpx.ClientIP(r, s.cfg.Server.TrustedProxy),
				UserAgent:  r.UserAgent(),
			})
			Error(w, r, apierror.Forbidden)
			return
		}
		h(w, r)
	}
}

// installerGate refuses calls once installation has completed.
func (s *Server) installerGate(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.deps.Installer.RequireNotLocked(r.Context()); err != nil {
			Error(w, r, err)
			return
		}
		h(w, r)
	}
}

// InternalMetricsSnapshot exposes the panel-internal metrics registry.
func (s *Server) InternalMetricsSnapshot() map[string]any {
	return s.deps.InternalMetrics.Snapshot()
}

// mwInstallerUnlocked is the middleware form of the installer lock check.
func (s *Server) mwInstallerUnlocked() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := s.deps.Installer.RequireNotLocked(r.Context()); err != nil {
				if errors.Is(err, installer.ErrInstallerLocked) {
					Error(w, r, apierror.InstallerLocked)
					return
				}
				Error(w, r, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

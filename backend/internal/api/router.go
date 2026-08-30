package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/go-chi/chi/v5"
)

func (s *Server) mountPublic(r chi.Router) {
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
}

func (s *Server) mountV1(r chi.Router) {
	r.Route("/api/v1", func(v1 chi.Router) {
		v1.Use(s.injectIdentity)

		// Public auth surface; login and password-reset requests are IP rate limited.
		v1.With(s.authLimiter.Middleware(s.ipKeyFn())).Post("/auth/login", s.csrf(s.handleLogin))
		v1.Post("/auth/logout", s.handleLogout) // cookie-only revocation works even with bad CSRF
		v1.Post("/auth/refresh", s.csrf(s.handleRefresh))
		v1.With(s.mwRequireAuth()).Get("/auth/me", s.handleMe)
		v1.With(s.mwRequireAuth()).Post("/auth/change-password", s.csrf(s.handleChangePassword))

		resetLimiter := httpx.NewRateLimiter(5, time.Minute)
		v1.With(resetLimiter.Middleware(s.ipKeyFn())).Post("/auth/forgot-password", s.handleForgotPassword)
		v1.With(resetLimiter.Middleware(s.ipKeyFn())).Post("/auth/reset-password", s.handleResetPassword)

		// Installer: status readable always, mutations gated and locked server-side.
		v1.Get("/installer/status", s.handleInstallerStatus)
		v1.With(s.mwInstallerUnlocked(), s.mwInstallerCSRF()).Post("/installer/requirements", s.handleInstallerRequirements)
		v1.With(s.mwInstallerUnlocked(), s.mwInstallerCSRF()).Post("/installer/database/test", s.handleInstallerDatabaseTest)
		v1.With(s.mwInstallerUnlocked(), s.mwInstallerCSRF()).Post("/installer/database/config", s.handleInstallerDatabaseConfig)
		v1.With(s.mwInstallerUnlocked(), s.mwInstallerCSRF()).Post("/installer/license", s.handleInstallerLicense)
		v1.With(s.mwInstallerUnlocked(), s.mwInstallerCSRF()).Post("/installer/configuration", s.handleInstallerConfiguration)
		v1.With(s.mwInstallerUnlocked(), s.mwInstallerCSRF()).Post("/installer/administrator", s.handleInstallerAdministrator)
		v1.With(s.mwInstallerUnlocked(), s.mwInstallerCSRF()).Post("/installer/security", s.handleInstallerSecurity)
		v1.With(s.mwInstallerUnlocked(), s.mwInstallerCSRF()).Post("/installer/complete", s.handleInstallerComplete)

		// Agent protocol (no browser session).
		v1.With(s.agentRegistrationGate()).Post("/servers/register", s.handleAgentRegister)
		v1.With(s.agentTokenAuth()).Post("/servers/heartbeat", s.handleAgentHeartbeat)
		v1.With(s.agentTokenAuth()).Post("/servers/telemetry", s.handleTelemetryIngest)

		// Monitoring (Phase 3). RBAC is enforced server-side; frontend
		// hiding is only UX.
		v1.Get("/monitoring/fleet", s.requireAuth(s.RequirePermission("monitoring.view", s.handleFleetOverview)))
		v1.Get("/servers/{id}/metrics/current", s.requireAuth(s.RequirePermission("monitoring.server.view", s.handleMetricsCurrent)))
		v1.Get("/servers/{id}/metrics/history", s.requireAuth(s.RequirePermission("monitoring.server.view", s.handleMetricsHistory)))
		v1.Get("/servers/{id}/metrics/network", s.requireAuth(s.RequirePermission("monitoring.server.view", s.handleMetricsNetwork)))
		v1.Get("/servers/{id}/metrics/disk", s.requireAuth(s.RequirePermission("monitoring.server.view", s.handleMetricsDisk)))
		v1.Get("/servers/{id}/metrics/services", s.requireAuth(s.RequirePermission("monitoring.services.view", s.handleMetricsServices)))
		v1.Get("/servers/{id}/metrics/processes", s.requireAuth(s.RequirePermission("monitoring.server.view", s.handleMetricsProcesses)))

		v1.Get("/alerts", s.requireAuth(s.RequirePermission("monitoring.view", s.handleAlertsList)))
		v1.Get("/alerts/rules", s.requireAuth(s.RequirePermission("monitoring.view", s.handleAlertsRulesList)))
		v1.Patch("/alerts/rules/{id}", s.requireAuth(s.RequirePermission("settings.manage", s.csrf(s.handleAlertsRuleUpdate))))
		v1.Post("/alerts/{id}/acknowledge", s.requireAuth(s.RequirePermission("monitoring.view", s.csrf(s.handleAlertsAcknowledge))))
		v1.Get("/system/internal-metrics", s.requireAuth(s.RequirePermission("settings.manage", s.handleInternalMetrics)))

		// Authenticated product APIs (session + CSRF + permission).
		v1.Get("/dashboard/summary", s.requireAuth(s.RequirePermission("dashboard.view", s.handleDashboardSummary)))

		// Servers. Listing/viewing needs server.view; management actions are
		// split across the granular codes seeded in migration 0003.
		v1.Get("/servers", s.requireAuth(s.RequirePermission("server.view", s.handleServersList)))
		v1.Get("/servers/{id}", s.requireAuth(s.RequirePermission("server.view", s.handleServersGet)))
		v1.Delete("/servers/{id}", s.requireAuth(s.RequirePermission("servers.delete", s.csrf(s.handleServersRevoke))))

		v1.Post("/servers/registration-tokens", s.requireAuth(s.RequirePermission("servers.create", s.csrf(s.handleRegistrationTokenCreate))))
		v1.Get("/servers/registration-tokens", s.requireAuth(s.RequirePermission("server.view", s.handleRegistrationTokenList)))
		v1.Delete("/servers/registration-tokens/{id}", s.requireAuth(s.RequirePermission("servers.delete", s.csrf(s.handleRegistrationTokenRevoke))))

		v1.Get("/servers/{id}/capabilities", s.requireAuth(s.RequirePermission("server.view", s.handleServerCapabilities)))
		v1.Post("/servers/{id}/capabilities", s.requireAuth(s.RequirePermission("server.manage", s.csrf(s.handleServerCapabilitiesProbe))))
		v1.Get("/servers/{id}/php-versions", s.requireAuth(s.RequirePermission("server.view", s.handleServerPHPVersions)))
		v1.Post("/servers/{id}/install/nginx", s.requireAuth(s.RequirePermission("server.manage", s.csrf(s.handleInstallNginx))))
		v1.Post("/servers/{id}/install/php", s.requireAuth(s.RequirePermission("server.manage", s.csrf(s.handleInstallPHP))))

		// Domains.
		v1.Get("/domains", s.requireAuth(s.RequirePermission("domains.view", s.handleDomainsList)))
		v1.Post("/domains", s.requireAuth(s.RequirePermission("domains.create", s.csrf(s.handleDomainsCreate))))
		v1.Get("/domains/{id}", s.requireAuth(s.RequirePermission("domains.view", s.handleDomainsGet)))
		v1.Delete("/domains/{id}", s.requireAuth(s.RequirePermission("domains.delete", s.csrf(s.handleDomainsDelete))))

		// Websites. Provisioning/state changes are long-running where needed;
		// handlers return 202 + job for those.
		v1.Get("/websites", s.requireAuth(s.RequirePermission("websites.view", s.handleWebsitesList)))
		v1.Post("/websites", s.requireAuth(s.RequirePermission("websites.create", s.csrf(s.handleWebsitesCreate))))
		v1.Get("/websites/{id}", s.requireAuth(s.RequirePermission("websites.view", s.handleWebsitesGet)))
		v1.Patch("/websites/{id}", s.requireAuth(s.RequirePermission("websites.edit", s.csrf(s.handleWebsitesUpdate))))
		v1.Delete("/websites/{id}", s.requireAuth(s.RequirePermission("websites.delete", s.csrf(s.handleWebsitesDelete))))
		v1.Post("/websites/{id}/enable", s.requireAuth(s.RequirePermission("websites.edit", s.csrf(s.handleWebsitesEnable))))
		v1.Post("/websites/{id}/disable", s.requireAuth(s.RequirePermission("websites.edit", s.csrf(s.handleWebsitesDisable))))
		v1.Post("/websites/{id}/reload", s.requireAuth(s.RequirePermission("websites.edit", s.csrf(s.handleWebsitesReload))))
		v1.Post("/websites/{id}/retry", s.requireAuth(s.RequirePermission("websites.edit", s.csrf(s.handleWebsitesRetry))))
		v1.Get("/websites/{id}/logs", s.requireAuth(s.RequirePermission("websites.logs.view", s.handleWebsitesLogs)))
		v1.Get("/websites/{id}/health", s.requireAuth(s.RequirePermission("websites.view", s.handleWebsitesHealth)))

		// Phase 4 — SSL certificates.
		v1.Get("/websites/{id}/certificate", s.requireAuth(s.RequirePermission("websites.view", s.handleCertificateGet)))
		v1.Post("/websites/{id}/certificate", s.requireAuth(s.RequirePermission("websites.config.manage", s.csrf(s.handleCertificateRequest))))
		v1.Delete("/websites/{id}/certificate", s.requireAuth(s.RequirePermission("websites.config.manage", s.csrf(s.handleCertificateRemove))))

		// Phase 5 — notification channels (operator configuration).
		v1.Get("/notifications/channels", s.requireAuth(s.RequirePermission("settings.view", s.handleChannelsList)))
		v1.Post("/notifications/channels", s.requireAuth(s.RequirePermission("settings.manage", s.csrf(s.handleChannelCreate))))
		v1.Patch("/notifications/channels/{id}", s.requireAuth(s.RequirePermission("settings.manage", s.csrf(s.handleChannelUpdate))))
		v1.Delete("/notifications/channels/{id}", s.requireAuth(s.RequirePermission("settings.manage", s.csrf(s.handleChannelDelete))))
		v1.Post("/notifications/channels/{id}/test", s.requireAuth(s.RequirePermission("settings.manage", s.csrf(s.handleChannelTest))))

		// Operator settings (ACME mode, thresholds, retention).
		v1.Get("/settings", s.requireAuth(s.RequirePermission("settings.view", s.handleSettingsGet)))
		v1.Patch("/settings", s.requireAuth(s.RequirePermission("settings.manage", s.csrf(s.handleSettingsPatch))))

		// Phase 6 — managed databases.
		v1.Get("/databases", s.requireAuth(s.RequirePermission("databases.view", s.handleDatabasesList)))
		v1.Post("/databases", s.requireAuth(s.RequirePermission("databases.create", s.csrf(s.handleDatabasesCreate))))
		v1.Get("/databases/{id}", s.requireAuth(s.RequirePermission("databases.view", s.handleDatabasesGet)))
		v1.Delete("/databases/{id}", s.requireAuth(s.RequirePermission("databases.delete", s.csrf(s.handleDatabasesDelete))))
		v1.Post("/databases/{id}/users", s.requireAuth(s.RequirePermission("databases.users.manage", s.csrf(s.handleDatabaseUsersCreate))))
		v1.Delete("/databases/{id}/users/{userId}", s.requireAuth(s.RequirePermission("databases.users.manage", s.csrf(s.handleDatabaseUsersDelete))))
		v1.Post("/databases/{id}/users/{userId}/password", s.requireAuth(s.RequirePermission("databases.users.manage", s.csrf(s.handleDatabaseUserPassword))))
		v1.Get("/servers/{id}/db-engines", s.requireAuth(s.RequirePermission("server.view", s.handleServerDBEngines)))

		// Phase 7 — software manager.
		v1.Get("/servers/{id}/software", s.requireAuth(s.RequirePermission("server.view", s.handleSoftwareList)))
		v1.Post("/servers/{id}/software/install", s.requireAuth(s.RequirePermission("server.manage", s.csrf(s.handleSoftwareInstall))))
		v1.Post("/servers/{id}/software/remove", s.requireAuth(s.RequirePermission("server.manage", s.csrf(s.handleSoftwareRemove))))
		v1.Post("/servers/{id}/software/service", s.requireAuth(s.RequirePermission("server.manage", s.csrf(s.handleSoftwareService))))

		// Background jobs (provisioning progress). Viewing a job leaks only
		// progress; the underlying resource permissions gate the UI flows.
		v1.Get("/jobs/{id}", s.requireAuth(s.RequirePermission("websites.view", s.handleJobsGet)))

		v1.Get("/license/status", s.requireAuth(s.RequirePermission("license.view", s.handleLicenseStatus)))
		v1.Post("/license/refresh", s.requireAuth(s.RequirePermission("license.manage", s.csrf(s.handleLicenseRefresh))))
		v1.Post("/license/deactivate", s.requireAuth(s.RequirePermission("license.manage", s.csrf(s.handleLicenseDeactivate))))

		v1.Get("/roles", s.requireAuth(s.RequirePermission("roles.view", s.handleRolesList)))
		v1.Get("/permissions", s.requireAuth(s.RequirePermission("roles.view", s.handlePermissionsList)))
		v1.Get("/roles/detail", s.requireAuth(s.RequirePermission("roles.view", s.handleRolesListDetail)))
		v1.Post("/roles", s.requireAuth(s.RequirePermission("roles.create", s.csrf(s.handleRolesCreate))))
		v1.Patch("/roles/{id}", s.requireAuth(s.RequirePermission("roles.edit", s.csrf(s.handleRolesUpdate))))
		v1.Delete("/roles/{id}", s.requireAuth(s.RequirePermission("roles.delete", s.csrf(s.handleRolesDelete))))

		v1.Get("/users", s.requireAuth(s.RequirePermission("users.view", s.handleUsersList)))
		v1.Post("/users", s.requireAuth(s.RequirePermission("users.create", s.csrf(s.handleUsersCreate))))
		v1.Get("/users/{id}", s.requireAuth(s.RequirePermission("users.view", s.handleUsersGet)))
		v1.Patch("/users/{id}", s.requireAuth(s.RequirePermission("users.edit", s.csrf(s.handleUsersUpdate))))
		v1.Delete("/users/{id}", s.requireAuth(s.RequirePermission("users.delete", s.csrf(s.handleUsersDelete))))

		v1.Get("/system/info", s.handleSystemInfo)
	})
}

// installer endpoints are anonymous by design but CSRF-safe once a session
// cookie exists on the browser (an installed-elsewhere session in same jar
// cannot be hijacked to tamper with someone else's install flow).
func (s *Server) mwInstallerCSRF() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if auth.IdentityFrom(r.Context()) != nil && !auth.CheckCSRF(r, auth.IdentityFrom(r.Context())) {
				Error(w, r, apierror.CSRF)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) mountStatic(r chi.Router, dir string) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}
	fileServer := http.FileServer(http.Dir(absDir))

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/api/") {
			Error(w, req, apierror.NotFound("route"))
			return
		}
		clean := filepath.Clean("/" + req.URL.Path)
		full := filepath.Join(absDir, clean)
		if !strings.HasPrefix(full, absDir+string(os.PathSeparator)) && full != absDir {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if info, serr := os.Stat(full); serr == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, req)
			return
		}
		index := filepath.Join(absDir, "index.html")
		if _, ierr := os.Stat(index); ierr != nil {
			http.Error(w, "frontend not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		http.ServeFile(w, req, index)
	})
}

func (s *Server) sessionCookieOpts() auth.CookieOpts {
	return auth.CookieOpts{Secure: s.cfg.Security.CookieSecure}
}

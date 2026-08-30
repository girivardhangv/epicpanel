// EpicPanel API server entrypoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
	"github.com/epicbyte/epicpanel/backend/internal/api"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/config"
	"github.com/epicbyte/epicpanel/backend/internal/databases"
	"github.com/epicbyte/epicpanel/backend/internal/db"
	"github.com/epicbyte/epicpanel/backend/internal/domains"
	"github.com/epicbyte/epicpanel/backend/internal/installer"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/licensing"
	"github.com/epicbyte/epicpanel/backend/internal/logger"
	"github.com/epicbyte/epicpanel/backend/internal/metrics"
	"github.com/epicbyte/epicpanel/backend/internal/monitoring"
	"github.com/epicbyte/epicpanel/backend/internal/notifier"
	"github.com/epicbyte/epicpanel/backend/internal/rbac"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/epicbyte/epicpanel/backend/internal/software"
	"github.com/epicbyte/epicpanel/backend/internal/users"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
	"github.com/epicbyte/epicpanel/backend/migrations"
)

// Version is injected at build time via -ldflags "-X main.Version=...".
var Version = "0.1.0-dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(2)
	}
	log := logger.New(cfg.Logging.Level, cfg.Logging.JSONOutput || cfg.IsProduction())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.Database.DSN, cfg.Database.MaxConns)
	if err != nil {
		log.Error("database unavailable; panel cannot start", "err", err)
		os.Exit(1)
	}
	if err := db.Migrate(ctx, pool, migrations.FS(), log); err != nil {
		log.Error("migration failure", "err", err)
		os.Exit(1)
	}

	settingSvc := settings.New(pool, log)
	auditSvc := audit.New(pool, log)
	rbacSvc := rbac.New(pool)
	sessionsStore := auth.NewStore(pool, cfg.Security.SessionLifetime)

	fingerprintProvider := newFingerprintProvider(pool, log)

	licClient := &licensing.RemoteClient{
		BaseURL: cfg.Licensing.APIBaseURL,
		Harness: &http.Client{Timeout: cfg.Licensing.Timeout},
	}
	licSvc := licensing.New(pool, licClient,
		licensing.Policy{GraceEnabled: cfg.Licensing.GraceEnabled, GracePeriod: cfg.Licensing.GracePeriod},
		cfg.Licensing.RevalidateEvery, fingerprintProvider, log)

	authSvc := auth.NewService(auth.Deps{
		Pool:            pool,
		Sessions:        sessionsStore,
		Perms:           rbacSvc,
		Audit:           auditSvc,
		Settings:        settingSvc,
		MaxFailedLogins: cfg.Security.MaxFailedLogins,
		Lockout:         cfg.Security.AccountLockout,
		SessionLifetime: cfg.Security.SessionLifetime,
		PasswordMinLen:  cfg.Security.PasswordMinLength,
		PasswordClasses: cfg.Security.PasswordRequireClasses,
		Development:     !cfg.IsProduction(),
	}, func(format string, args ...any) {
		if !cfg.IsProduction() {
			log.Warn("password reset requested (development delivery)", "hint", format, "args", fmt.Sprint(args...))
			log.Info("DEV RESET TOKEN — use via POST /api/v1/auth/reset-password", "token", args[0])
		}
	})

	instSvc := installer.New(installer.Deps{
		Pool:       pool,
		Licensing:  licSvc,
		Audit:      auditSvc,
		Settings:   settingSvc,
		UserStore:  &adminProvisioner{pool: pool, perms: rbacSvc, auditSvc: auditSvc, settings: settingSvc},
		DBVerifier: func(ictx context.Context) error { return pingDatabase(ictx, pool) },
		DSNPersister: func(dsn string) error {
			next := *cfg
			next.Database.DSN = dsn
			return config.Persist(&next, "")
		},
		Version: Version,
	})

	srvDeps := &api.Deps{
		Cfg:         cfg,
		Log:         log,
		Sessions:    sessionsStore,
		Auth:        authSvc,
		RBAC:        rbacSvc,
		Audit:       auditSvc,
		Licensing:   licSvc,
		Installer:   instSvc,
		Servers:     servers.NewService(pool),
		Settings:    settingSvc,
		UserManager: users.NewManager(pool),
		Version:     Version,
	}

	// Phase 2 — website hosting engine.
	agentCli := agentclient.New()
	domainsSvc := domains.NewService(pool)
	jobsStore := jobs.NewStore(pool)
	websiteSvc := websites.New(websites.Deps{
		Pool:     pool,
		Log:      log,
		Agent:    agentCli,
		Servers:  srvDeps.Servers,
		Domains:  domainsSvc,
		Settings: settingSvc,
		Jobs:     jobsStore,
		Audit:    auditSvc,
	})
	runner := jobs.NewRunner(jobsStore, nil, func(format string, args ...any) {
		log.Warn("jobs: "+format, args...)
	})
	websiteSvc.RegisterHandlers(runner)
	runner.Start(ctx)

	srvDeps.Agent = agentCli
	srvDeps.Domains = domainsSvc
	srvDeps.Websites = websiteSvc
	srvDeps.Jobs = jobsStore

	// Phase 6 — managed databases (MySQL + PostgreSQL via the agent).
	databasesSvc := databases.New(databases.Deps{
		Pool:    pool,
		Log:     log,
		Agent:   agentCli,
		Servers: srvDeps.Servers,
		Jobs:    jobsStore,
		Audit:   auditSvc,
	})
	databasesSvc.RegisterHandlers(runner)
	srvDeps.Databases = databasesSvc

	// Phase 7 — software manager (installs run as jobs; state detected live).
	softwareSvc := software.New(software.Deps{
		Pool:    pool,
		Log:     log,
		Agent:   agentCli,
		Servers: srvDeps.Servers,
		Jobs:    jobsStore,
		Audit:   auditSvc,
	})
	softwareSvc.RegisterHandlers(runner)
	srvDeps.Software = softwareSvc

	// Phase 3 — monitoring, telemetry & server health.
	internalMetrics := metrics.Default()
	monitoringIngest := monitoring.NewIngestService(pool, log)
	monitoringQuery := monitoring.NewQueryService(pool, settingSvc)
	alertsSvc := monitoring.NewAlertService(pool, auditSvc, log)

	// Phase 5 — notifications. Channel CRUD + delivery jobs.
	notifierSvc := notifier.NewService(pool, auditSvc, log)
	runner.Register(jobs.TypeNotifyAlert, notifierSvc.HandleNotifyAlert)
	alertsSvc.Notifier = func(ctx context.Context, event, ruleName, ruleType, severity, serverID, serverName, message string, metricValue, threshold *float64, triggeredAt string) {
		enqueueNotifications(ctx, jobsStore, notifierSvc, event, ruleName, ruleType, severity, serverID, serverName, message, metricValue, threshold, triggeredAt)
	}

	// Retention is operator-tunable via settings with spec defaults
	// (raw 7d / hourly 30d / daily 365d).
	retentionRaw := settingSvc.Int(ctx, "monitoring.retention_raw_days", 7, 1, 365)
	retentionHourly := settingSvc.Int(ctx, "monitoring.retention_hourly_days", 30, 1, 3650)
	retentionDaily := settingSvc.Int(ctx, "monitoring.retention_daily_days", 365, 1, 36500)
	maintenance := monitoring.NewMaintenanceWorker(pool, log, retentionRaw, retentionHourly, retentionDaily)
	maintenance.Start(ctx)
	alertsSvc.Start(ctx)
	settingSvc.Set(ctx, "monitoring.enabled", true)

	srvDeps.MonitoringIngest = monitoringIngest
	srvDeps.MonitoringQuery = monitoringQuery
	srvDeps.Alerts = alertsSvc
	srvDeps.InternalMetrics = internalMetrics
	srvDeps.Notifier = notifierSvc

	srv := api.NewServer(srvDeps)

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Handler(),
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
		MaxHeaderBytes:    1 << 16,
	}
	go func() {
		log.Info("epicpanel listening", "addr", cfg.Server.Addr,
			"env", cfg.Server.Environment, "version", Version, "goos", runtime.GOOS)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	pool.Close()
	log.Info("stopped")
}

// enqueueNotifications creates a notify_alert job per enabled channel whose
// severity threshold is met. Runs inside the alert evaluation loop.
func enqueueNotifications(ctx context.Context, store *jobs.Store, notifierSvc *notifier.Service,
	event, ruleName, ruleType, severity, serverID, serverName, message string,
	metricValue, threshold *float64, triggeredAt string) {
	channels, err := notifierSvc.ListChannels(ctx)
	if err != nil {
		return
	}
	for _, ch := range channels {
		if !ch.Enabled || !meetsSeverity(ch.Severity, severity) {
			continue
		}
		payload := map[string]any{
			"channel_id":   ch.ID,
			"event":        event,
			"severity":     severity,
			"server_id":    serverID,
			"server_name":  serverName,
			"rule":         ruleName,
			"rule_type":    ruleType,
			"message":      message,
			"triggered_at": triggeredAt,
		}
		if metricValue != nil {
			payload["metric_value"] = *metricValue
		}
		if threshold != nil {
			payload["threshold"] = *threshold
		}
		_, _ = store.Create(ctx, jobs.TypeNotifyAlert, nil, nil, payload)
	}
}

// meetsSeverity reports whether a channel threshold accepts an alert severity.
// critical channels only get critical alerts; warning channels get both.
func meetsSeverity(channelThreshold, alertSeverity string) bool {
	if channelThreshold == "critical" {
		return alertSeverity == "critical"
	}
	return true
}

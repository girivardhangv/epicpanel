package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/api"
	"github.com/epicbyte/epicpanel/backend/internal/apitokens"
	"github.com/epicbyte/epicpanel/backend/internal/apps"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/backups"
	"github.com/epicbyte/epicpanel/backend/internal/bandwidthhistory"
	"github.com/epicbyte/epicpanel/backend/internal/config"
	"github.com/epicbyte/epicpanel/backend/internal/crons"
	"github.com/epicbyte/epicpanel/backend/internal/databases"
	"github.com/epicbyte/epicpanel/backend/internal/db"
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

	agentproto "github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/epicbyte/epicpanel/backend/internal/packages"
	"github.com/epicbyte/epicpanel/backend/internal/resourcelimits"
	"github.com/epicbyte/epicpanel/backend/internal/runtimes"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/epicbyte/epicpanel/backend/internal/sshkeys"
	"github.com/epicbyte/epicpanel/backend/internal/terminal"
	"github.com/epicbyte/epicpanel/backend/internal/traffic"
	"github.com/epicbyte/epicpanel/backend/internal/users"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
	"github.com/google/uuid"
)

func main() {
	// Subcommand: `epicpanel-api setup-token` mints a one-time bootstrap
	// link (1h) and prints the full URL — used by the installer.
	if len(os.Args) > 1 && os.Args[1] == "setup-token" {
		if err := setupTokenCmd(); err != nil {
			slog.Error("setup-token failed", "err", err)
			os.Exit(1)
		}
		return
	}
	// Subcommand: `epicpanel-api reset-password --email a@b --password X`
	// Operator recovery when no admin can log in. Root-only on the box; also
	// disables MFA on the account (TOTP may be un-recoverable) and revokes
	// every session (Phase 12 rule: password change ⇒ session revocation).
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		if err := resetPasswordCmd(os.Args[2:]); err != nil {
			slog.Error("reset-password failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func setupTokenCmd() error {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	h := &settings.SetupHandler{Settings: &settings.Store{Pool: pool}}
	token, expires, err := h.MintSetupToken(ctx)
	if err != nil {
		return err
	}
	host := cfg.PublicURL
	if host == "" {
		host = outboundIP()
	}
	fmt.Printf("PANEL_SETUP_URL=%s/setup?token=%s\n", host, token)
	fmt.Fprintf(os.Stderr, "setup link valid until %s (single use)\n", expires.Format(time.RFC1123))
	return nil
}

func httpPort(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return "8080"
}

// outboundIP returns the primary non-loopback IPv4 of this machine.
func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			return addr.IP.String()
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "127.0.0.1"
}

func run() error {
	cfg := config.Load()

	var log *slog.Logger
	if cfg.Environment == "production" {
		log = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	} else {
		log = slog.New(slog.NewTextHandler(os.Stdout, nil))
	}
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	log.Info("migrations applied")

	srv := &api.Server{
		Cfg:            cfg,
		Pool:           pool,
		Sessions:       &auth.SessionStore{Pool: pool},
		Users:          &users.Store{Pool: pool},
		Orgs:           &organizations.Store{Pool: pool},
		Servers:        &servers.Store{Pool: pool},
		Websites:       &websites.Store{Pool: pool},
		Jobs:           &jobs.Store{Pool: pool},
		Runtimes:       &runtimes.Store{Pool: pool},
		Databases:      &databases.Store{Pool: pool},
		Domains:        &domains.Store{Pool: pool},
		Packages:       &packages.Store{Pool: pool},
		Crons:          &crons.Store{Pool: pool},
		Apps:           &apps.Store{Pool: pool},
		SSHKeys:        &sshkeys.Store{Pool: pool},
		Settings:       &settings.Store{Pool: pool},
		TerminalHub:    terminal.NewHub(),
		Deployments:    &deployments.Store{Pool: pool},
		Backups:        &backups.Store{Pool: pool},
		Tokens:         &apitokens.Store{Pool: pool},
		Limiter:        httpapi.NewTokenBucket(),
		FTPAccounts:    &ftpaccounts.Store{Pool: pool},
		DNS:            &dns.Store{Pool: pool},
		Audit:          &audit.Store{Pool: pool},
		WPPending:      &websites.WPPendingStore{Pool: pool},
		MFA:            &auth.MFAStore{Pool: pool},
		Accounts:       &apitokens.ServiceAccountStore{Pool: pool},
		ResourceLimits: resourcelimits.New(&packages.Store{Pool: pool}, pool),
	}

	// Event bus: Redis pub/sub when EPICPANEL_REDIS_URL is set, otherwise the
	// Postgres LISTEN/NOTIFY driver (zero extra infrastructure, boring).
	httpapi.ConfigureTrustedProxies(cfg.TrustedProxies)
	wsHub := events.NewHub(events.WSOptions{
		AllowedOrigins: cfg.CORSOrigins,
		UserOrgs: func(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
			return organizations.NewStore(pool).OrganizationsForUser(ctx, userID)
		},
	})
	bus := &events.Bus{Pool: pool}
	if cfg.RedisURL != "" {
		if rd, err := events.NewRedisDriver(ctx, cfg.RedisURL); err == nil {
			bus.Driver = rd
			defer rd.Close(ctx)
			go rd.Subscribe(ctx, wsHub.Dispatch)
			log.Info("event driver", "kind", "redis")
		} else {
			log.Warn("redis driver unavailable; falling back to postgres LISTEN/NOTIFY", "err", err)
		}
	}
	if bus.Driver == nil {
		if pd, err := events.NewPGDriver(ctx, pool); err == nil {
			bus.Driver = pd
			defer pd.Close(ctx)
			go pd.Subscribe(ctx, wsHub.Dispatch)
			log.Info("event driver", "kind", "postgres_notify")
		} else {
			log.Warn("event driver disabled (local delivery only)", "err", err)
		}
	}
	srv.Events = bus
	srv.WSHub = wsHub

	// Phase 3 real-time metrics pipeline: in-memory live store, WS fan-out
	// (no DB writes in the live path) and the async batched history writer.
	liveStore := metrics.NewLiveStore()
	liveStore.OnSample = func(serverID uuid.UUID, frame map[string]any) {
		if frame != nil {
			wsHub.BroadcastMetrics(frame)
		}
	}
	srv.LiveStore = liveStore
	// Dynamic resources: completed per-site traffic windows ride the metrics
	// stream; decouple ingest from the store lock with a bounded hand-off.
	srv.Traffic = traffic.NewStore()
	// Per-site bandwidth history (migration 0050): the same completed
	// access-log windows feed the hourly/daily bucket tables alongside the
	// in-memory dynamic-resources store.
	bwHistory := bandwidthhistory.New(pool)
	liveStore.OnTraffic = func(frames []agentproto.SiteTraffic) {
		go func(f []agentproto.SiteTraffic) {
			defer func() { _ = recover() }() // never let analytics kill ingest
			srv.Traffic.Ingest(f)
			bwHistory.Ingest(f)
		}(frames)
	}
	historyWriter := metrics.NewWriter(pool, liveStore)
	srv.History = historyWriter
	go historyWriter.Run(ctx)
	metrics.StartRollupJob(ctx, pool)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	srv.StartBackground(ctx)

	checker := monitoring.NewChecker(pool, cfg.HealthCheckInterval)
	defer checker.Stop()
	go checker.Run(ctx)

	errCh := make(chan error, 1)
	go func() {
		log.Info("epicpanel api listening", "addr", cfg.HTTPAddr, "env", cfg.Environment)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
		log.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}

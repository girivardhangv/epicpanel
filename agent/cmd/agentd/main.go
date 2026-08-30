// EpicPanel agent: registers the host with the panel, reports inventory
// snapshots and serves a strictly typed management endpoint for the panel.
// There is deliberately no command execution surface; every operation is an
// explicit, validated capability behind the ops bearer token.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/epicbyte/epicpanel/agent/internal/client"
	"github.com/epicbyte/epicpanel/agent/internal/dbops"
	"github.com/epicbyte/epicpanel/agent/internal/monitoring"
	"github.com/epicbyte/epicpanel/agent/internal/ops"
	"github.com/epicbyte/epicpanel/agent/internal/platform"
	"github.com/epicbyte/epicpanel/agent/internal/software"
)

var (
	version = "0.2.0-dev"

	// AgentVersion is reported to the panel for upgrade tracking.
	AgentVersion = version
)

func main() {
	var (
		panelURL     = flag.String("url", envOr("EPICPANEL_URL", "http://127.0.0.1:8080"), "EpicPanel base URL")
		agentKey     = flag.String("key", envOr("EPICPANEL_AGENT_KEY", ""), "Registration token (X-Agent-Key) created in the panel")
		label        = flag.String("label", envOr("EPICPANEL_AGENT_LABEL", ""), "Friendly label shown in the panel")
		intervalSecs = flag.Int("interval", 60, "Heartbeat interval in seconds")
		caFile       = flag.String("ca-file", os.Getenv("EPICPANEL_CA_FILE"), "PEM file with private CA chain (optional)")
		dataDir      = flag.String("dir", envOr("EPICPANEL_AGENT_DIR",
			filepath.Join(".", ".epicpanel-agent")), "Directory holding agent credentials")
		reEnroll = flag.Bool("enroll", false, "Force re-enrollment even if credentials exist")
		listen   = flag.String("listen", envOr("EPICPANEL_AGENT_LISTEN", ":9200"),
			"Management endpoint listen address (panel -> agent ops channel)")
		advertise = flag.String("advertise", envOr("EPICPANEL_AGENT_ADVERTISE", ""),
			"URL the panel uses to reach the management endpoint (default: http://<outbound-ip>:<port>)")
		sitesRoot = flag.String("sites-root", envOr("EPICPANEL_SITES_ROOT", platform.DefaultSitesRoot()),
			"Root directory that holds all website files; the panel cannot touch anything outside it")
		softwareDir = flag.String("software-dir", envOr("EPICPANEL_SOFTWARE_DIR", software.DefaultSoftwareDir()),
			"EpicPanel-owned software root; self-contained installs live here")
		collectSecs = flag.Int("collect-interval", envIntOr("EPICPANEL_AGENT_COLLECT_INTERVAL", 15),
			"Telemetry collection interval in seconds (clamped to 10..300)")
		nginxDir = flag.String("nginx-dir", envOr("EPICPANEL_NGINX_DIR", ""),
			"Windows nginx install directory (default C:\\nginx)")
		phpDirs = flag.String("php-dirs", envOr("EPICPANEL_PHP_DIRS", ""),
			"Windows PHP install directories, semicolon-separated (default C:\\PHP;C:\\Program Files\\PHP)")
		mysqlHost = flag.String("mysql-host", envOr("EPICPANEL_MYSQL_HOST", "127.0.0.1"), "MySQL/MariaDB admin host (empty user disables the engine)")
		mysqlPort = flag.Int("mysql-port", envIntOr("EPICPANEL_MYSQL_PORT", 3306), "MySQL/MariaDB admin port")
		mysqlUser = flag.String("mysql-user", envOr("EPICPANEL_MYSQL_USER", ""), "MySQL/MariaDB admin user (blank = engine disabled)")
		mysqlPass = flag.String("mysql-password", envOr("EPICPANEL_MYSQL_PASSWORD", ""), "MySQL/MariaDB admin password")
		pgHost = flag.String("pg-host", envOr("EPICPANEL_PG_HOST", "127.0.0.1"), "PostgreSQL admin host")
		pgPort = flag.Int("pg-port", envIntOr("EPICPANEL_PG_PORT", 5432), "PostgreSQL admin port")
		pgUser = flag.String("pg-user", envOr("EPICPANEL_PG_USER", ""), "PostgreSQL admin user (blank = engine disabled)")
		pgPass = flag.String("pg-password", envOr("EPICPANEL_PG_PASSWORD", ""), "PostgreSQL admin password")
		pgSSL  = flag.String("pg-sslmode", envOr("EPICPANEL_PG_SSLMODE", "disable"), "PostgreSQL sslmode")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	opts := &client.Options{
		BaseURL:   *panelURL,
		CAFile:    *caFile,
		UserAgent: "EpicPanelAgent/" + AgentVersion + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")",
	}

	credsPath := client.CredentialsPath(*dataDir)
	var creds client.Credentials

	if !*reEnroll {
		if c, err := client.LoadCredentials(credsPath); err == nil {
			creds = c
			log.Info("using stored credentials", "server_id", creds.ServerID)
		}
	}

	if creds.AgentToken == "" {
		if *agentKey == "" {
			log.Error("no stored agent token and -key not provided; cannot enroll")
			os.Exit(2)
		}
		info, err := platform.Collect(platform.Current())
		if err != nil {
			log.Error("collect failed during enrollment", "err", err)
			os.Exit(1)
		}
		adv := *advertise
		if adv == "" {
			adv = defaultAdvertise(*listen)
		}
		id, token, opsToken, err := opts.Enroll(*agentKey, toRequest(info, *label, adv))
		if err != nil {
			log.Error("enrollment failed", "err", err)
			os.Exit(1)
		}
		creds = client.Credentials{ServerID: id, AgentToken: token, OpsToken: opsToken}
		if err := client.SaveCredentials(credsPath, creds.ServerID, creds.AgentToken, creds.OpsToken); err != nil {
			log.Error("credential persistence failed", "path", credsPath, "err", err)
			os.Exit(1)
		}
		log.Info("enrolled successfully", "server_id", creds.ServerID,
			"credentials", credsPath, "note", "tokens are shown never again; rotate via panel re-enroll if lost")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Management endpoint (panel -> agent). Requires the ops token that the
	// panel issued at enrollment; without it the channel stays disabled and
	// the operator is told to re-enroll.
	if creds.OpsToken != "" {
		// Point the platform web server / PHP runtime at our self-contained
		// installs by default so the panel manages copies it owns, never the
		// host's existing software.
		nginxDir := *nginxDir
		if nginxDir == "" {
			nginxDir = filepath.Join(*softwareDir, "nginx")
		}
		phpDirs := *phpDirs
		if phpDirs == "" {
			phpDirs = filepath.Join(*softwareDir, "php")
		}
		opsSrv, err := ops.New(ops.Options{
			Log:         log,
			OpsToken:    creds.OpsToken,
			SitesRoot:   *sitesRoot,
			NginxDir:    nginxDir,
			PHPDirs:     phpDirs,
			SoftwareDir: *softwareDir,
			CertsDir:    "", // ops defaults to <nginx>/conf/ssl
			AcctDir:     filepath.Join(*dataDir, "acme"),
			MySQL: dbops.AdminConfig{
				Host: *mysqlHost, Port: *mysqlPort, User: *mysqlUser, Password: *mysqlPass,
			},
			Postgres: dbops.AdminConfig{
				Host: *pgHost, Port: *pgPort, User: *pgUser, Password: *pgPass, SSLMode: *pgSSL,
			},
			Version: AgentVersion,
		})
		if err != nil {
			log.Error("management endpoint unavailable", "err", err)
			os.Exit(1)
		}
		go func() {
			log.Info("management endpoint listening", "addr", *listen, "sites_root", *sitesRoot)
			if err := opsSrv.ListenAndServe(*listen); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("management endpoint failed", "err", err)
				stop()
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = opsSrv.Shutdown(shutdownCtx)
		}()
	} else {
		log.Warn("no ops token in credentials; remote management disabled (re-enroll to enable)")
	}

	// Telemetry subsystem (Phase 3): periodic bounded collection with a
	// persisted sequence counter, drop-oldest buffering and backoff-driven
	// delivery over the same bearer token as heartbeats. Hosting operations
	// are unaffected by telemetry failures.
	collector, err := monitoring.NewCollector()
	if err != nil {
		log.Error("monitoring collector unavailable", "err", err)
		os.Exit(1)
	}
	telemetrySender := func(ctx context.Context, samples []*monitoring.Sample) error {
		raws := make([]json.RawMessage, 0, len(samples))
		for _, s := range samples {
			raw, err := json.Marshal(s)
			if err != nil {
				continue // one unserializable sample must not drop the batch
			}
			raws = append(raws, raw)
		}
		err := opts.SendTelemetry(creds.AgentToken, raws)
		if err != nil {
			log.Warn("telemetry send failed (backing off)",
				"samples", len(raws), "err", err)
			return err
		}
		log.Info("telemetry delivered", "samples", len(raws),
			"sequence", samples[len(samples)-1].Sequence)
		return nil
	}
	telemetryRunner := monitoring.NewRunner(collector, monitoring.NewSequence(*dataDir),
		telemetrySender, time.Duration(*collectSecs)*time.Second, AgentVersion)
	go telemetryRunner.Run(ctx)

	ticker := time.NewTicker(time.Duration(*intervalSecs) * time.Second)
	defer ticker.Stop()

	adv := *advertise
	if adv == "" {
		adv = defaultAdvertise(*listen)
	}

	log.Info("agent running", "url", *panelURL, "interval_seconds", *intervalSecs)

	sendOnce := func() bool {
		info, err := platform.Collect(platform.Current())
		if err != nil {
			log.Warn("collect failed", "err", err)
			return true // transient; keep looping
		}
		err = opts.Heartbeat(creds.AgentToken, toRequest(info, *label, adv))
		switch {
		case err == nil:
			return true
		case errors.Is(err, client.ErrTokenInvalid):
			return false // token revoked; operator must re-enroll manually
		default:
			log.Warn("heartbeat error (will retry)", "err", err)
			return true
		}
	}
	_ = sendOnce() // immediate first report

	for {
		select {
		case <-ticker.C:
			if !sendOnce() {
				log.Error("agent token rejected by panel; stopping (remove credentials and re-enroll)")
				os.Exit(3)
			}
		case <-ctx.Done():
			log.Info("agent stopped")
			return
		}
	}
}

// defaultAdvertise derives the URL the panel should use: the preferred
// outbound IPv4 plus the listen port.
func defaultAdvertise(listen string) string {
	host := outboundIP()
	port := listenPort(listen)
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return ""
	}
	return addr.IP.String()
}

func listenPort(listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil && port != "" {
		return port
	}
	if !strings.Contains(listen, ":") {
		return listen
	}
	return "9200"
}

func toRequest(info *platform.Info, label, advertiseURL string) client.EnrollRequest {
	return client.EnrollRequest{
		Label:        label,
		Hostname:     info.Hostname,
		OS:           string(info.OS),
		OSVersion:    info.Version,
		Arch:         info.Arch,
		AgentVersion: AgentVersion,
		OpsAddr:      advertiseURL,
		Specs: map[string]any{
			"cpu":    info.CPU,
			"memory": info.Memory,
			"disks":  info.Disks,
		},
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

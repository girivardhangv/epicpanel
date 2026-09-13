package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agent"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "enroll" {
		if err := enrollCmd(os.Args[2:]); err != nil {
			slog.Error("enroll failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// enrollCmd implements `epicpanel-agent enroll --url <panel> --token <reg>`.
// After a successful enrollment the agent persists its credentials and
// (unless --no-run) starts serving immediately.
func enrollCmd(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	url := fs.String("url", "", "control plane URL, e.g. http://panel.internal:8080")
	token := fs.String("token", "", "one-time registration token from the panel")
	noRun := fs.Bool("no-run", false, "persist credentials and exit without starting the agent")
	_ = fs.Parse(args)

	if *token == "" {
		return fmt.Errorf("--token is required (generate it in the panel: Servers -> Connect Server)")
	}
	baseURL := *url
	if baseURL == "" {
		baseURL = os.Getenv("EPICPANEL_CONTROL_PLANE_URL")
	}
	if baseURL == "" {
		return fmt.Errorf("--url is required (or set EPICPANEL_CONTROL_PLANE_URL)")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(log)

	agentToken, serverID, err := agent.Enroll(baseURL, *token)
	if err != nil {
		return err
	}
	log.Info("enrolled successfully", "server_id", serverID, "config", agent.EnvFilePath())

	if *noRun {
		log.Info("run the agent with: epicpanel-agent run")
		return nil
	}

	cfg := agent.Config{
		ControlPlaneURL: baseURL,
		AgentToken:      agentToken,
		PollInterval:    agent.PollIntervalFromEnv(),
	}
	return serve(cfg, log)
}

// run starts the agent from env vars or the persisted config file.
func run() error {
	cfg := agent.ConfigFromEnv()
	if cfg.ControlPlaneURL == "" || cfg.AgentToken == "" {
		if url, token := agent.LoadEnvFile(); url != "" && token != "" {
			cfg.ControlPlaneURL = url
			cfg.AgentToken = token
		}
	}
	if cfg.ControlPlaneURL == "" || cfg.AgentToken == "" {
		return fmt.Errorf("not enrolled: run `epicpanel-agent enroll --url <panel> --token <reg-token>` or set EPICPANEL_CONTROL_PLANE_URL + EPICPANEL_AGENT_TOKEN")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(log)
	return serve(cfg, log)
}

// serve runs the three decoupled agent loops (the Phase 3 architecture):
//
//	metrics stream goroutine — high-frequency samples + liveness, never
//	                          blocked by jobs (audit root cause §★1)
//	job worker goroutine     — claim/execute/report loop (unchanged contract)
//	liveness goroutine       — legacy HTTP heartbeat fallback when the
//	                          stream is down (older control planes, strict
//	                          egress firewalls)
func serve(cfg agent.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := agent.NewClient(cfg.ControlPlaneURL, cfg.AgentToken)
	exec := agent.NewExecutor()
	streamer := agent.NewStreamer(cfg.ControlPlaneURL, cfg.AgentToken, agent.AgentVersion, 0)

	// Realtime console + state events ride the same persistent stream.
	agent.WireRealtime(streamer)

	log.Info("epicpanel agent starting", "control_plane", cfg.ControlPlaneURL,
		"poll_interval", cfg.PollInterval, "metrics_interval", agent.MetricsIntervalForLog())

	go streamer.Run(ctx)

	// Job loop: claims and executes typed jobs. Kept exactly as before
	// (KEEP contract) — but now isolated so slow jobs cannot starve
	// heartbeats or metrics.
	go func() {
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for {
					processed, err := agent.PollAndExecute(ctx, client, exec, cfg)
					if err != nil {
						log.Warn("job poll failed", "err", err)
					}
					if ctx.Err() != nil || !processed {
						break
					}
				}
			}
		}
	}()

	// Legacy liveness: only while the stream is NOT connected, so the
	// server never goes offline on a control plane without the stream
	// endpoint (older version) and does not double-write when it is.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if streamer.Connected() {
					continue
				}
				if err := agent.HeartbeatOnce(ctx, client, cfg); err != nil {
					log.Warn("heartbeat failed", "err", err)
				}
			}
		}
	}()

	<-ctx.Done()
	log.Info("agent shutting down")
	// Wait briefly so the stream goroutine can close cleanly.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	return nil
}

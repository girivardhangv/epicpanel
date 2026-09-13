package agent

import (
	"os"
	"strconv"
	"time"

	"github.com/go-acme/lego/v4/lego"
)

type Config struct {
	ControlPlaneURL string
	AgentToken      string
	PollInterval    time.Duration
	// ACME settings for Let's Encrypt issuance.
	ACMEEmail     string
	ACMEDirectory string
}

func PollIntervalFromEnv() time.Duration {
	// Snappy default: the agent claims jobs sub-second so console/lifecycle
	// actions reflect almost immediately (Pterodactyl-like responsiveness).
	interval := 1 * time.Second
	if v := os.Getenv("EPICPANEL_AGENT_POLL_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	return interval
}

func ConfigFromEnv() Config {
	interval := PollIntervalFromEnv()
	directory := os.Getenv("EPICPANEL_ACME_DIRECTORY")
	if directory == "" {
		directory = lego.LEDirectoryProduction
	}
	return Config{
		ControlPlaneURL: os.Getenv("EPICPANEL_CONTROL_PLANE_URL"),
		AgentToken:      os.Getenv("EPICPANEL_AGENT_TOKEN"),
		PollInterval:    interval,
		ACMEEmail:       os.Getenv("EPICPANEL_ACME_EMAIL"),
		ACMEDirectory:   directory,
	}
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

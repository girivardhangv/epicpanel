package config

import (
	"os"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL         string
	HTTPAddr            string
	Environment         string
	CookieName          string
	CookieSecure        bool
	SessionTTL          time.Duration
	HealthCheckInterval time.Duration
	CORSOrigins         []string
	// TrustedProxies: CIDRs allowed to set X-Forwarded-For. Empty = never
	// trust the header (direct connections only).
	TrustedProxies []string
	// RedisURL enables the Redis pub/sub event driver when set; the default
	// driver is Postgres LISTEN/NOTIFY (boring, zero extra moving parts).
	RedisURL string
}

func Load() Config {
	c := Config{
		DatabaseURL: env("EPICPANEL_DATABASE_URL", "postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel?sslmode=disable"),
		HTTPAddr:    env("EPICPANEL_HTTP_ADDR", "127.0.0.1:8080"),
		Environment: env("EPICPANEL_ENV", "development"),
		CookieName:  "epicpanel_session",
		SessionTTL:  30 * 24 * time.Hour,
	}
	c.CookieSecure = c.Environment == "production"
	if v := os.Getenv("EPICPANEL_SESSION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.SessionTTL = d
		}
	}
	if v := os.Getenv("EPICPANEL_COOKIE_SECURE"); v != "" {
		c.CookieSecure = v == "true" || v == "1"
	}
	c.HealthCheckInterval = 60 * time.Second
	if v := os.Getenv("EPICPANEL_HEALTH_CHECK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.HealthCheckInterval = d
		}
	}
	if v := os.Getenv("EPICPANEL_CORS_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				c.CORSOrigins = append(c.CORSOrigins, o)
			}
		}
	} else {
		c.CORSOrigins = []string{"http://localhost:5173", "http://127.0.0.1:5173"}
	}
	if v := os.Getenv("EPICPANEL_TRUSTED_PROXIES"); v != "" {
		c.TrustedProxies = strings.Split(v, ",")
	}
	c.RedisURL = os.Getenv("EPICPANEL_REDIS_URL")
	return c
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

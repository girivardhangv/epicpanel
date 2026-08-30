// Package config loads EpicPanel server configuration.
//
// Configuration resolution order (later wins):
//  1. Built-in defaults
//  2. Optional JSON config file (EPICPANEL_CONFIG_FILE, default <data-dir>/panel.json)
//  3. Environment variables prefixed with EPICPANEL_
//
// Secrets are never hard-coded and never written to logs.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds all server settings. Every field has a safe default where possible;
// Database.DSN has no default and must be supplied for normal operation.
type Config struct {
	Server   ServerConfig   `json:"server"`
	Database DatabaseConfig `json:"database"`
	Security SecurityConfig `json:"security"`
	Licensing LicensingConfig `json:"licensing"`
	Logging  LoggingConfig  `json:"logging"`

	DataDir string `json:"data_dir"` // directory for panel-managed files (config overlay, uploads, later keys)
	DistDir string `json:"dist_dir"` // optional built frontend served by the API in production
}

type ServerConfig struct {
	Addr         string        `json:"addr"`           // listen address, e.g. ":8080"
	PublicURL    string        `json:"public_url"`     // externally visible URL (used in links, license identity)
	Environment  string        `json:"environment"`    // development | production
	TrustedProxy string        `json:"trusted_proxy"`  // CIDR; when empty, RemoteAddr is used directly
	ReadTimeout  time.Duration `json:"read_timeout"`
	WriteTimeout time.Duration `json:"write_timeout"`
	IdleTimeout  time.Duration `json:"idle_timeout"`
}

type DatabaseConfig struct {
	DSN             string        `json:"dsn"` // postgres:// URL; provided via env or set during installation
	MaxConns        int32         `json:"max_conns"`
	MinConns        int32         `json:"min_conns"`
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time"`
}

type SecurityConfig struct {
	CookieSecure            bool          `json:"cookie_secure"` // set automatically based on environment unless overridden
	SessionLifetime         time.Duration `json:"session_lifetime"`
	SessionSlidingRefresh   bool          `json:"session_sliding_refresh"`
	MaxFailedLogins         int           `json:"max_failed_logins"` // per-account lockout threshold
	AccountLockout          time.Duration `json:"account_lockout"`
	LoginRatePerMinute      int           `json:"login_rate_per_minute"` // per IP, POST /auth/login
	GlobalRatePerMinute     int           `json:"global_rate_per_minute"`
	RequestBodyLimit        int64         `json:"request_body_limit"`
	PasswordMinLength       int           `json:"password_min_length"`
	PasswordRequireClasses  int           `json:"password_require_classes"` // character classes required out of 4
}

type LicensingConfig struct {
	APIBaseURL      string        `json:"api_base_url"` // licensing server root, e.g. https://licenses.example.com
	Timeout         time.Duration `json:"timeout"`
	GraceEnabled    bool          `json:"grace_enabled"`    // allow short-lived outages after prior validation
	GracePeriod     time.Duration `json:"grace_period"`     // max time panel remains usable without successful validation
	RevalidateEvery time.Duration `json:"revalidate_every"` // background validation cadence
}

type LoggingConfig struct {
	Level      string `json:"level"` // debug | info | warn | error
	JSONOutput bool   `json:"json_output"`
}

func Defaults() Config {
	return Config{
		Server: ServerConfig{
			Addr:         ":8080",
			PublicURL:    "",
			Environment:  "development",
			ReadTimeout:  15 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		Database: DatabaseConfig{
			DSN:             "",
			MaxConns:        20,
			MinConns:        2,
			ConnMaxIdleTime: 5 * time.Minute,
		},
		Security: SecurityConfig{
			CookieSecure:           false,
			SessionLifetime:        24 * time.Hour,
			SessionSlidingRefresh:  true,
			MaxFailedLogins:        10,
			AccountLockout:         15 * time.Minute,
			LoginRatePerMinute:     20,
			GlobalRatePerMinute:    600,
			RequestBodyLimit:       1 << 20, // 1 MiB
			PasswordMinLength:      12,
			PasswordRequireClasses: 3,
		},
		Licensing: LicensingConfig{
			APIBaseURL:      "",
			Timeout:         10 * time.Second,
			GraceEnabled:    true,
			GracePeriod:     72 * time.Hour,
			RevalidateEvery: 6 * time.Hour,
		},
		Logging: LoggingConfig{
			Level:      "info",
			JSONOutput: false,
		},
		DataDir: "data",
		DistDir: "",
	}
}

func (c *Config) IsProduction() bool { return c.Server.Environment == "production" }

var errNotFound = errors.New("not found")

// Load resolves configuration from defaults -> config file -> environment.
func Load() (*Config, error) {
	cfg := Defaults()

	if v := os.Getenv("EPICPANEL_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}

	configFile := os.Getenv("EPICPANEL_CONFIG_FILE")
	if configFile == "" {
		configFile = filepath.Join(cfg.DataDir, "panel.json")
	}
	if raw, err := os.ReadFile(configFile); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", configFile, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read config file %s: %w", configFile, err)
	}

	applyEnv(&cfg)

	// Cookie security follows the environment unless production operators force it.
	if v := os.Getenv("EPICPANEL_SERVER_ENVIRONMENT"); v != "" {
		cfg.Server.Environment = strings.ToLower(v)
	}
	if !cfg.IsProduction() {
		// Development on localhost typically has no TLS terminator.
		if os.Getenv("EPICPANEL_SECURITY_COOKIE_SECURE") == "" {
			cfg.Security.CookieSecure = false
		}
	} else if os.Getenv("EPICPANEL_SECURITY_COOKIE_SECURE") == "" {
		cfg.Security.CookieSecure = true
	}

	if cfg.IsProduction() && cfg.Database.DSN == "" {
		return nil, errors.New("EPICPANEL_DATABASE_DSN is required in production")
	}
	return &cfg, nil
}

func applyEnv(c *Config) {
	str := func(dst *string, key string) {
		if v := os.Getenv("EPICPANEL_" + key); v != "" {
			*dst = v
		}
	}
	dur := func(dst *time.Duration, key string) {
		if v := os.Getenv("EPICPANEL_" + key); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				*dst = d
			}
		}
	}
	intVal := func(dst *int, key string) {
		if v := os.Getenv("EPICPANEL_" + key); v != "" {
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				*dst = n
			}
		}
	}
	int32Val := func(dst *int32, key string) {
		if v := os.Getenv("EPICPANEL_" + key); v != "" {
			var n int32
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
				*dst = n
			}
		}
	}
	boolVal := func(dst *bool, key string) {
		switch strings.ToLower(os.Getenv("EPICPANEL_" + key)) {
		case "1", "true", "yes", "on":
			*dst = true
		case "0", "false", "no", "off":
			*dst = false
		}
	}

	str(&c.Server.Addr, "SERVER_ADDR")
	str(&c.Server.PublicURL, "SERVER_PUBLIC_URL")
	str(&c.Server.Environment, "SERVER_ENVIRONMENT")
	str(&c.Server.TrustedProxy, "SERVER_TRUSTED_PROXY")

	str(&c.Database.DSN, "DATABASE_DSN")
	int32Val(&c.Database.MaxConns, "DATABASE_MAX_CONNS")

	boolVal(&c.Security.CookieSecure, "SECURITY_COOKIE_SECURE")
	dur(&c.Security.SessionLifetime, "SECURITY_SESSION_LIFETIME")
	intVal(&c.Security.MaxFailedLogins, "SECURITY_MAX_FAILED_LOGINS")
	dur(&c.Security.AccountLockout, "SECURITY_ACCOUNT_LOCKOUT")
	intVal(&c.Security.LoginRatePerMinute, "SECURITY_LOGIN_RATE_PER_MINUTE")
	intVal(&c.Security.GlobalRatePerMinute, "SECURITY_GLOBAL_RATE_PER_MINUTE")
	intVal(&c.Security.PasswordMinLength, "SECURITY_PASSWORD_MIN_LENGTH")
	intVal(&c.Security.PasswordRequireClasses, "SECURITY_PASSWORD_REQUIRE_CLASSES")

	str(&c.Licensing.APIBaseURL, "LICENSE_API_URL")
	dur(&c.Licensing.Timeout, "LICENSE_TIMEOUT")
	boolVal(&c.Licensing.GraceEnabled, "LICENSE_GRACE_ENABLED")
	dur(&c.Licensing.GracePeriod, "LICENSE_GRACE_PERIOD")
	dur(&c.Licensing.RevalidateEvery, "LICENSE_REVALIDATE_EVERY")

	str(&c.Logging.Level, "LOG_LEVEL")

	str(&c.DistDir, "DIST_DIR")
}

// Persist writes the subset of configuration the panel owns to disk with
// restrictive permissions. Used by the installer to remember the selected
// database DSN without exposing it again afterwards.
func Persist(cfg *Config, path string) error {
	if path == "" {
		path = filepath.Join(cfg.DataDir, "panel.json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

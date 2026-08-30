// Package settings provides typed, cached access to the system_settings
// table. The panel itself never hard-codes operator-tunable values; defaults
// live here and can be overridden via the installer/security step.
package settings

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	mu    sync.Mutex
	cache map[string]cacheEntry
	ttl   time.Duration
}

type cacheEntry struct {
	value   any
	expires time.Time
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, log: log, cache: map[string]cacheEntry{}, ttl: 15 * time.Second}
}

// Canonical setting keys used across the panel.
const (
	KeyPasswordMinLength      = "security.password_min_length"
	KeyPasswordRequireClasses = "security.password_require_classes"
	KeySessionLifetimeMinutes = "security.session_lifetime_minutes"
	KeyMaxFailedLogins        = "security.max_failed_logins"
	KeyAccountLockoutMinutes  = "security.account_lockout_minutes"
	KeyAgentRegistrationKey   = "servers.agent_registration_key"
	KeyServerOfflineMinutes   = "servers.offline_threshold_minutes"

	// Phase 4 — SSL/ACME
	KeyACMEMode              = "ssl.acme_mode"               // mock | staging | production
	KeyACMEEmail             = "ssl.acme_email"              // optional account contact
	KeyACMEAutoRenewDays     = "ssl.auto_renew_days"         // renew when <= this many days remain
	KeyACMEStagingDir        = "ssl.acme_staging_directory"  // ACME staging directory URL
	KeyACMEProductionDir     = "ssl.acme_production_directory" // ACME production directory URL
)

func (s *Service) raw(ctx context.Context, key string) (string, bool) {
	s.mu.Lock()
	if e, ok := s.cache[key]; ok && time.Now().Before(e.expires) {
		v := e.value.(string)
		s.mu.Unlock()
		return v, true
	}
	s.mu.Unlock()

	var val []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&val)
	if err != nil {
		return "", false
	}
	var decoded any
	if err := json.Unmarshal(val, &decoded); err != nil {
		s.log.Warn("settings: unmarshal failed", "key", key)
		return "", false
	}
	str, _ := decoded.(string)
	s.mu.Lock()
	s.cache[key] = cacheEntry{value: str, expires: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return str, true
}

// String returns a stored value or the provided default.
func (s *Service) String(ctx context.Context, key, def string) string {
	if v, ok := s.raw(ctx, key); ok && v != "" {
		return v
	}
	return def
}

// Int returns an integer override clamped to [min,max], or def when unset/invalid.
func (s *Service) Int(ctx context.Context, key string, def, min, max int) int {
	if v, ok := s.raw(ctx, key); ok && v != "" {
		var n int
		if err := json.Unmarshal([]byte(v), &n); err == nil {
			if n < min {
				n = min
			}
			if n > max {
				n = max
			}
			return n
		}
		s.log.Warn("settings: invalid integer", "key", key)
	}
	return def
}

// Set upserts a JSON-encoded value and busts the local cache.
func (s *Service) Set(ctx context.Context, key string, jsonValue any) error {
	raw, err := json.Marshal(jsonValue)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO system_settings (key, value, updated_at)
		VALUES ($1, $2::jsonb, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		key, string(raw))
	if err == nil {
		s.mu.Lock()
		delete(s.cache, key)
		s.mu.Unlock()
	}
	return err
}

// EnsureAgentKey returns the agent registration secret, creating one on first use.
func (s *Service) EnsureAgentKey(ctx context.Context) (string, error) {
	if existing := s.String(ctx, KeyAgentRegistrationKey, ""); existing != "" {
		return existing, nil
	}
	k, err := generateSecret(32)
	if err != nil {
		return "", err
	}
	if err := s.Set(ctx, KeyAgentRegistrationKey, k); err != nil {
		return "", err
	}
	return k, nil
}

func generateSecret(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, c := range b {
		out[i] = alphabet[int(c)%len(alphabet)]
	}
	return string(out), nil
}

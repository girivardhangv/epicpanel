package websites

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/nginxcfg"
)

var ErrConfigNotFound = errors.New("website config not found")

// WebsiteConfig is the per-site web-server configuration row: the legacy
// rewrite_rules string (still the source for sites that never saved a
// structured config) plus the structured, versioned nginxcfg document.
type WebsiteConfig struct {
	WebsiteID    uuid.UUID            `json:"website_id"`
	RewriteRules string               `json:"rewrite_rules"`
	Config       *nginxcfg.SiteConfig `json:"config,omitempty"`
	Version      int                  `json:"version"`
	UpdatedAt    time.Time            `json:"updated_at"`
}

type ConfigStore struct {
	Pool *pgxpool.Pool
}

const configCols = `website_id, rewrite_rules, config_json, version, updated_at`

func scanConfig(row pgx.Row) (*WebsiteConfig, error) {
	var c WebsiteConfig
	var raw []byte
	if err := row.Scan(&c.WebsiteID, &c.RewriteRules, &raw, &c.Version, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if len(raw) > 0 && string(raw) != "{}" {
		cfg := &nginxcfg.SiteConfig{}
		if err := json.Unmarshal(raw, cfg); err == nil && cfg.Schema > 0 {
			c.Config = cfg
		}
	}
	return &c, nil
}

// GetConfig returns the config for a website; an empty (not-created) config
// is returned as an empty struct, not an error.
func (s *ConfigStore) Get(ctx context.Context, websiteID uuid.UUID) (*WebsiteConfig, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+configCols+` FROM website_configs WHERE website_id = $1`, websiteID)
	c, err := scanConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return &WebsiteConfig{WebsiteID: websiteID, Version: 1}, nil
	}
	return c, err
}

// SetRules upserts the legacy rewrite-rules snippet.
func (s *ConfigStore) SetRules(ctx context.Context, websiteID uuid.UUID, rules string) (*WebsiteConfig, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO website_configs (website_id, rewrite_rules, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (website_id) DO UPDATE SET rewrite_rules = $2, updated_at = now()
		RETURNING `+configCols, websiteID, rules)
	return scanConfig(row)
}

// SetConfig stores the structured config as a NEW version (append-only
// history), returning the updated row. Caller has validated the document.
func (s *ConfigStore) SetConfig(ctx context.Context, websiteID uuid.UUID, cfg *nginxcfg.SiteConfig, createdBy *uuid.UUID) (*WebsiteConfig, error) {
	blob, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var version int
	if err := tx.QueryRow(ctx, `
		INSERT INTO website_configs (website_id, rewrite_rules, config_json, version, updated_at)
		VALUES ($1, '', $2, 1, now())
		ON CONFLICT (website_id) DO UPDATE
		    SET config_json = $2, version = website_configs.version + 1, updated_at = now()
		RETURNING version`, websiteID, blob).Scan(&version); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO website_config_versions (website_id, version, config_json, created_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (website_id, version) DO UPDATE SET config_json = EXCLUDED.config_json`,
		websiteID, version, blob, createdBy); err != nil {
		return nil, err
	}
	// Keep the last 20 versions (the table is a safety net, not an audit log).
	if _, err := tx.Exec(ctx, `
		DELETE FROM website_config_versions
		WHERE website_id = $1 AND version <= $2 - 20`, websiteID, version); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, websiteID)
}

// ListVersions returns the stored version history (newest first).
func (s *ConfigStore) ListVersions(ctx context.Context, websiteID uuid.UUID, limit int) ([]ConfigVersion, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT version, config_json, created_at
		FROM website_config_versions WHERE website_id = $1
		ORDER BY version DESC LIMIT $2`, websiteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigVersion
	for rows.Next() {
		var v ConfigVersion
		var raw []byte
		if err := rows.Scan(&v.Version, &raw, &v.CreatedAt); err != nil {
			return nil, err
		}
		cfg := &nginxcfg.SiteConfig{}
		if err := json.Unmarshal(raw, cfg); err == nil {
			v.Config = cfg
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVersion returns one historical version document.
func (s *ConfigStore) GetVersion(ctx context.Context, websiteID uuid.UUID, version int) (*nginxcfg.SiteConfig, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT config_json FROM website_config_versions
		WHERE website_id = $1 AND version = $2`, websiteID, version).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConfigNotFound
	}
	if err != nil {
		return nil, err
	}
	cfg := &nginxcfg.SiteConfig{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ConfigVersion is one history entry.
type ConfigVersion struct {
	Version   int                  `json:"version"`
	Config    *nginxcfg.SiteConfig `json:"config,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
}

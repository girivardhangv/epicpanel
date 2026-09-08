package websites

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrConfigNotFound = errors.New("website config not found")

// WebsiteConfig holds per-site nginx directive snippets (rewrite rules and
// other safe config) applied to every server block of the vhost.
type WebsiteConfig struct {
	WebsiteID    uuid.UUID `json:"website_id"`
	RewriteRules string    `json:"rewrite_rules"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ConfigStore struct {
	Pool *pgxpool.Pool
}

const configCols = `website_id, rewrite_rules, updated_at`

func scanConfig(row pgx.Row) (*WebsiteConfig, error) {
	var c WebsiteConfig
	err := row.Scan(&c.WebsiteID, &c.RewriteRules, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// GetConfig returns the config for a website; an empty (not-created) config
// is returned as an empty struct, not an error.
func (s *ConfigStore) Get(ctx context.Context, websiteID uuid.UUID) (*WebsiteConfig, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+configCols+` FROM website_configs WHERE website_id = $1`, websiteID)
	c, err := scanConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return &WebsiteConfig{WebsiteID: websiteID, RewriteRules: ""}, nil
	}
	return c, err
}

// SetRules upserts the rewrite rules snippet.
func (s *ConfigStore) SetRules(ctx context.Context, websiteID uuid.UUID, rules string) (*WebsiteConfig, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO website_configs (website_id, rewrite_rules, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (website_id) DO UPDATE SET rewrite_rules = $2, updated_at = now()
		RETURNING `+configCols, websiteID, rules)
	return scanConfig(row)
}

package settings

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("setting not found")

type Store struct {
	Pool *pgxpool.Pool
}

// Get returns the value for a key, or ErrNotFound.
func (s *Store) Get(ctx context.Context, key string) (string, error) {
	var val string
	err := s.Pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&val)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return val, err
}

// GetBool returns a boolean setting (defaults to false on missing/error).
func (s *Store) GetBool(ctx context.Context, key string) bool {
	v, err := s.Get(ctx, key)
	return err == nil && v == "true"
}

// Set upserts a key-value pair.
func (s *Store) Set(ctx context.Context, key, value string) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO system_settings (key, value, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = now()
	`, key, value)
	return err
}

// Package db manages the PostgreSQL connection pool.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect builds a pool and verifies reachability before returning.
func Connect(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database DSN: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database unreachable: %w", err)
	}
	return pool, nil
}

// Replace atomically swaps the pool pointer used by repositories after the
// installer re-points the panel at a new database. Callers must hold the
// returned close function until shutdown to avoid use-after-close races on
// restarts of the swap operation itself.
func Replace(dst **pgxpool.Pool, newPool *pgxpool.Pool) (old *pgxpool.Pool) {
	old = *dst
	*dst = newPool
	return old
}

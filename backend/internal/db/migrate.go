package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type migration struct {
	version int
	name    string
	sql     string
}

// Migrate applies all pending migrations in lexicographic order inside
// individual transactions recorded in schema_migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool, sqlFS fs.FS, log *slog.Logger) error {
	if sqlFS == nil {
		return errors.New("db: nil migration FS")
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	migrations, err := load(sqlFS)
	if err != nil {
		return err
	}

	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %04d_%s failed: %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
			m.version, m.name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		log.Info("applied migration", "version", m.version, "name", m.name)
	}
	return nil
}

func load(sqlFS fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(sqlFS, ".")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make([]migration, 0, len(names))
	for _, n := range names {
		raw, err := fs.ReadFile(sqlFS, n)
		if err != nil {
			return nil, err
		}
		base := strings.TrimSuffix(n, ".sql")
		idx := strings.Index(base, "_")
		if idx < 0 {
			return nil, fmt.Errorf("invalid migration name %q (want NNNN_name.sql)", n)
		}
		v, err := strconv.Atoi(base[:idx])
		if err != nil {
			return nil, fmt.Errorf("invalid migration version in %q: %w", n, err)
		}
		out = append(out, migration{version: v, name: base[idx+1:], sql: string(raw)})
	}
	return out, nil
}

package runtimes

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrExtNotFound = errors.New("extension not found")

type ExtensionStatus string

const (
	ExtInstalling ExtensionStatus = "installing"
	ExtAvailable  ExtensionStatus = "available"
	ExtFailed     ExtensionStatus = "failed"
	ExtRemoving   ExtensionStatus = "removing"
)

// Extension is a managed PHP extension of one installed PHP runtime.
type Extension struct {
	ID           uuid.UUID `json:"id"`
	RuntimeID    uuid.UUID `json:"runtime_id"`
	Name         string    `json:"name"`
	PhpVersion   string    `json:"php_version,omitempty"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ExtensionStore struct {
	Pool *pgxpool.Pool
}

const extCols = `id, runtime_id, name, php_version, status, error_message, created_at, updated_at`

func scanExt(row pgx.Row) (*Extension, error) {
	var e Extension
	err := row.Scan(&e.ID, &e.RuntimeID, &e.Name, &e.PhpVersion, &e.Status, &e.ErrorMessage, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// Upsert marks an extension installing (idempotent across retries).
func (s *ExtensionStore) UpsertInstalling(ctx context.Context, runtimeID uuid.UUID, name, phpVersion string) (*Extension, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO php_extensions (runtime_id, name, php_version, status)
		VALUES ($1, $2, $3, 'installing')
		ON CONFLICT (runtime_id, name, php_version) DO UPDATE SET status = 'installing', error_message = '', updated_at = now()
		RETURNING `+extCols, runtimeID, name, phpVersion)
	return scanExt(row)
}

// SetStatus flips an extension's state machine.
func (s *ExtensionStore) SetStatus(ctx context.Context, id uuid.UUID, status string, errMsg string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE php_extensions SET status = $2, error_message = $3, updated_at = now() WHERE id = $1
	`, id, status, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrExtNotFound
	}
	return nil
}

// GetByTypeVersion resolves an extension by runtime type+version+name (the
// agent only knows type/version, not the runtime UUID).
func (s *ExtensionStore) GetByRuntimeVersion(ctx context.Context, serverID uuid.UUID, rtType, version, name string) (*Extension, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT e.`+extCols+` FROM php_extensions e
		JOIN runtimes r ON r.id = e.runtime_id
		WHERE r.server_id = $1 AND r.type = $2 AND r.version = $3 AND e.name = $4
	`, serverID, rtType, version, name)
	return scanExt(row)
}

// ListForRuntime returns all managed extensions of a runtime.
func (s *ExtensionStore) ListForRuntime(ctx context.Context, runtimeID uuid.UUID) ([]Extension, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+extCols+` FROM php_extensions WHERE runtime_id = $1 ORDER BY name`, runtimeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Extension
	for rows.Next() {
		e, err := scanExt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// Delete drops an extension row (after successful removal on the server).
func (s *ExtensionStore) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM php_extensions WHERE id = $1`, id)
	return err
}

// RuntimeLookup resolves (server, type, version) → runtime row for handlers.
func (s *Store) GetByTypeVersionAny(ctx context.Context, serverID uuid.UUID, t, version string) (*Runtime, error) {
	row := s.Pool.QueryRow(ctx, `SELECT id, server_id, type, version, status, error_message, created_at FROM runtimes
		WHERE server_id = $1 AND type = $2 AND version = $3`, serverID, t, version)
	var r Runtime
	err := row.Scan(&r.ID, &r.ServerID, &r.Type, &r.Version, &r.Status, &r.ErrorMessage, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

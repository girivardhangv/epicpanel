package backups

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("backup not found")

type Status string

const (
	StatusPending    Status = "pending"
	StatusRunning    Status = "running"
	StatusSuccessful Status = "successful"
	StatusFailed     Status = "failed"
)

type Backup struct {
	ID           uuid.UUID `json:"id"`
	Organization uuid.UUID `json:"organization_id"`
	WebsiteID    uuid.UUID `json:"website_id"`
	Type         string    `json:"type"`
	Status       Status    `json:"status"`
	TriggerType  string    `json:"trigger_type"`
	SizeBytes    int64     `json:"size_bytes"`
	Databases    []string  `json:"databases"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, organization_id, website_id, type, status, trigger_type, size_bytes, databases, error, created_at`

func scanRow(row pgx.Row) (*Backup, error) {
	var b Backup
	var dbs json.RawMessage
	err := row.Scan(&b.ID, &b.Organization, &b.WebsiteID, &b.Type, &b.Status, &b.TriggerType,
		&b.SizeBytes, &dbs, &b.Error, &b.CreatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(dbs, &b.Databases)
	return &b, nil
}

func (s *Store) Create(ctx context.Context, orgID, websiteID, createdBy uuid.UUID, triggerType string) (*Backup, error) {
	var by *uuid.UUID
	if createdBy != uuid.Nil {
		by = &createdBy
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO backups (organization_id, website_id, status, trigger_type, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+cols,
		orgID, websiteID, StatusPending, triggerType, by,
	)
	return scanRow(row)
}

// CreateSystem records a backup with no user actor (scheduler-driven).
func (s *Store) CreateSystem(ctx context.Context, orgID, websiteID, serverID uuid.UUID, triggerType string) (*Backup, error) {
	return s.Create(ctx, orgID, websiteID, uuid.Nil, triggerType)
}

func (s *Store) MarkRunning(ctx context.Context, backupID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE backups SET status = 'running', started_at = now() WHERE id = $1`, backupID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkSuccessful(ctx context.Context, backupID uuid.UUID, size int64, databases []string) error {
	dbs, _ := json.Marshal(databases)
	tag, err := s.Pool.Exec(ctx, `
		UPDATE backups SET status = 'successful', size_bytes = $2, databases = $3, finished_at = now() WHERE id = $1
	`, backupID, size, dbs)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkFailed(ctx context.Context, backupID uuid.UUID, errMsg string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE backups SET status = 'failed', error = $2, finished_at = now() WHERE id = $1`, backupID, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetByID(ctx context.Context, orgID, backupID uuid.UUID) (*Backup, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM backups WHERE id = $1 AND organization_id = $2`, backupID, orgID)
	b, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

func (s *Store) GetAny(ctx context.Context, backupID uuid.UUID) (*Backup, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM backups WHERE id = $1`, backupID)
	b, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

func (s *Store) ListForWebsite(ctx context.Context, websiteID uuid.UUID, limit int) ([]Backup, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM backups WHERE website_id = $1 ORDER BY created_at DESC LIMIT $2`, websiteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		b, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// DueBackup is one website due for a scheduled backup.
type DueBackup struct {
	WebsiteID      uuid.UUID
	OrganizationID uuid.UUID
	ServerID       uuid.UUID
	Retention      int
}

// DueForSchedule returns ready websites needing a scheduled backup, with
// their retention setting, based on last_backup_at age.
func (s *Store) DueForSchedule(ctx context.Context, limit int) ([]DueBackup, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT w.id, w.organization_id, w.server_id, w.backup_retention
		FROM websites w
		WHERE w.status = 'ready' AND w.backup_schedule <> 'off'
		  AND (w.last_backup_at IS NULL
		       OR (w.backup_schedule = 'daily' AND w.last_backup_at < now() - interval '24 hours')
		       OR (w.backup_schedule = 'weekly' AND w.last_backup_at < now() - interval '7 days'))
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueBackup
	for rows.Next() {
		var d DueBackup
		if err := rows.Scan(&d.WebsiteID, &d.OrganizationID, &d.ServerID, &d.Retention); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PruneExcess deletes oldest backups of a website beyond its retention count.
// Returns backup IDs removed (the agent cleans archives on fanout... actually
// archives are cleaned by the agent via a dedicated payload; here we just
// remove rows and return the IDs).
func (s *Store) PruneExcess(ctx context.Context, websiteID uuid.UUID, retention int) ([]uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, `
		DELETE FROM backups
		WHERE website_id = $1 AND status = 'successful'
		  AND id NOT IN (
			SELECT id FROM backups WHERE website_id = $1 AND status = 'successful'
			ORDER BY created_at DESC LIMIT $2
		  )
		RETURNING id
	`, websiteID, retention)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

package deployments

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("deployment not found")

type Status string

const (
	StatusPending    Status = "pending"
	StatusRunning    Status = "running"
	StatusSuccessful Status = "successful"
	StatusFailed     Status = "failed"
)

type Deployment struct {
	ID           uuid.UUID  `json:"id"`
	Organization uuid.UUID  `json:"organization_id"`
	WebsiteID    uuid.UUID  `json:"website_id"`
	Branch       string     `json:"branch"`
	CommitSHA    string     `json:"commit_sha"`
	Status       Status     `json:"status"`
	TriggerType  string     `json:"trigger_type"`
	RollbackOf   *uuid.UUID `json:"rollback_of,omitempty"`
	ReleaseDir   string     `json:"release_dir,omitempty"`
	Log          string     `json:"log,omitempty"`
	Error        string     `json:"error,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, organization_id, website_id, branch, commit_sha, status, trigger_type, rollback_of, release_dir, log, error, started_at, finished_at, created_at`

func scanRow(row pgx.Row) (*Deployment, error) {
	var d Deployment
	err := row.Scan(&d.ID, &d.Organization, &d.WebsiteID, &d.Branch, &d.CommitSHA, &d.Status, &d.TriggerType,
		&d.RollbackOf, &d.ReleaseDir, &d.Log, &d.Error, &d.StartedAt, &d.FinishedAt, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Create inserts a pending deployment and returns it.
func (s *Store) Create(ctx context.Context, orgID, websiteID, createdBy uuid.UUID, branch string, triggerType string, rollbackOf *uuid.UUID) (*Deployment, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO deployments (organization_id, website_id, branch, status, trigger_type, rollback_of, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+cols,
		orgID, websiteID, branch, StatusPending, triggerType, rollbackOf, createdBy,
	)
	return scanRow(row)
}

// MarkRunning flips a deployment to running (called when its job is claimed).
func (s *Store) MarkRunning(ctx context.Context, deploymentID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE deployments SET status = 'running', started_at = now() WHERE id = $1`, deploymentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkSuccessful(ctx context.Context, deploymentID uuid.UUID, commitSHA, releaseDir, log string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE deployments SET status = 'successful', commit_sha = $2, release_dir = $3, log = $4, error = '', finished_at = now()
		WHERE id = $1
	`, deploymentID, commitSHA, releaseDir, log)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkFailed(ctx context.Context, deploymentID uuid.UUID, log, errMsg string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE deployments SET status = 'failed', log = $2, error = $3, finished_at = now() WHERE id = $1
	`, deploymentID, log, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetByID(ctx context.Context, orgID, deploymentID uuid.UUID) (*Deployment, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM deployments WHERE id = $1 AND organization_id = $2`, deploymentID, orgID)
	d, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// LastSuccessful returns the most recent successful deployment for a website.
func (s *Store) LastSuccessful(ctx context.Context, websiteID uuid.UUID) (*Deployment, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM deployments WHERE website_id = $1 AND status = 'successful' ORDER BY created_at DESC LIMIT 1`, websiteID)
	d, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// ListForWebsite returns deployment history (newest first).
func (s *Store) ListForWebsite(ctx context.Context, websiteID uuid.UUID, limit int) ([]Deployment, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM deployments WHERE website_id = $1 ORDER BY created_at DESC LIMIT $2`, websiteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		d, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// GetWebsiteDeployToken returns the encrypted deploy token for a website
// (nil when unset).
func (s *Store) GetWebsiteDeployToken(ctx context.Context, websiteID uuid.UUID) ([]byte, error) {
	var tok []byte
	err := s.Pool.QueryRow(ctx, `SELECT deploy_token_encrypted FROM websites WHERE id = $1`, websiteID).Scan(&tok)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return tok, nil
}

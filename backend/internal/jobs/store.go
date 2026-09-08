package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("job not found")

type Type string

const (
	TypeProvisionWebsite Type = "provision_website"
	TypeDeleteWebsite    Type = "delete_website"
	TypeInstallRuntime   Type = "install_runtime"
	TypeRemoveRuntime    Type = "remove_runtime"
	TypeCreateDatabase   Type = "create_database"
	TypeDeleteDatabase   Type = "delete_database"
	TypeIssueCertificate Type = "issue_certificate"
	TypeVerifyDomain     Type = "verify_domain"
	TypeDeployWebsite    Type = "deploy_website"
	TypeRollbackWebsite  Type = "rollback_website"
	TypeCloneStaging     Type = "clone_staging"
	TypePromoteStaging   Type = "promote_staging"
	TypeCreateBackup     Type = "create_backup"
	TypeRestoreBackup    Type = "restore_backup"
	TypeDBTools          Type = "install_database_tools"
	TypeInstallWP        Type = "install_wordpress"
	TypeSyncCrontab      Type = "sync_crontab"
	TypeBuildApp         Type = "build_app"
	TypeStartApp         Type = "start_app"
	TypeStopApp          Type = "stop_app"
	TypeRestartApp       Type = "restart_app"
	TypeAppStatus        Type = "app_status"
	TypeAppLogs          Type = "app_logs"
	TypeInstallExtension Type = "install_extension"
	TypeRemoveExtension  Type = "remove_extension"
	TypeDetectSoftware   Type = "detect_software"
	TypeSiteUsage        Type = "site_usage"
	TypeSyncFTPAccounts  Type = "sync_ftp_accounts"
	TypeSyncDNSZone      Type = "sync_dns_zone"
	TypeSuspendWebsite   Type = "suspend_website"
	TypeResumeWebsite    Type = "resume_website"
	TypeEnforceLimits    Type = "enforce_limits"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusSuccess Status = "success"
	StatusFailed  Status = "failed"
)

type Job struct {
	ID             uuid.UUID       `json:"id"`
	ServerID       uuid.UUID       `json:"server_id"`
	WebsiteID      *uuid.UUID      `json:"website_id"`
	Type           Type            `json:"type"`
	Status         Status          `json:"status"`
	Payload        json.RawMessage `json:"payload"`
	Result         json.RawMessage `json:"result"`
	Error          string          `json:"error,omitempty"`
	Progress       int             `json:"progress"`
	ProgressStep   string          `json:"progress_step,omitempty"`
	Attempts       int             `json:"attempts"`
	MaxAttempts    int             `json:"max_attempts"`
	IdempotencyKey *string         `json:"idempotency_key,omitempty"`
	ClaimedAt      *time.Time      `json:"claimed_at,omitempty"`
	LeaseExpiresAt *time.Time      `json:"lease_expires_at,omitempty"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

type Store struct {
	Pool *pgxpool.Pool
	// backoffUnit scales retry backoff. Zero value = production default of
	// 30s; tests set 1ns for immediate reclaims.
	backoffUnit time.Duration
}

// SetBackoffUnit overrides the retry-backoff unit (default 30s).
func (s *Store) SetBackoffUnit(d time.Duration) { s.backoffUnit = d }

func (s *Store) backoff() time.Duration {
	if s.backoffUnit == 0 {
		return 30 * time.Second
	}
	return s.backoffUnit
}

// leaseFor bounds how long an agent may hold a claimed job before the reaper
// considers the agent dead and requeues it. Long installs get more headroom.
func leaseFor(t Type) time.Duration {
	switch t {
	case TypeInstallRuntime, TypeRemoveRuntime, TypeInstallExtension, TypeRemoveExtension,
		TypeInstallWP, TypeDBTools, TypeCreateBackup, TypeRestoreBackup, TypeBuildApp,
		TypeCloneStaging, TypePromoteStaging:
		return 30 * time.Minute
	default:
		return 10 * time.Minute
	}
}

// backoffFor spaces out retries exponentially (30s, 1m, 2m, ... capped 15m)
// so a failing agent-side operation doesn't spin the queue.
func backoffFor(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempts && d < 15*time.Minute; i++ {
		d *= 2
	}
	if d > 15*time.Minute {
		d = 15 * time.Minute
	}
	return d
}

func (s *Store) Enqueue(ctx context.Context, serverID uuid.UUID, websiteID *uuid.UUID, jobType Type, payload any) (*Job, error) {
	return s.EnqueueIdempotent(ctx, serverID, websiteID, jobType, payload, "")
}

// EnqueueIdempotent enqueues a job; when key is non-empty and an identical
// pending/running job with the same key exists, that job is returned instead
// (provisioning retries never duplicate side effects).
func (s *Store) EnqueueIdempotent(ctx context.Context, serverID uuid.UUID, websiteID *uuid.UUID, jobType Type, payload any, key string) (*Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if key == "" {
		row := s.Pool.QueryRow(ctx, `
			INSERT INTO jobs (server_id, website_id, type, payload)
			VALUES ($1, $2, $3, $4)
			RETURNING `+jobCols,
			serverID, websiteID, jobType, payloadJSON,
		)
		return scanRow(row)
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO jobs (server_id, website_id, type, payload, idempotency_key)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL AND status IN ('pending', 'running')
		DO UPDATE SET updated_at = now()
		RETURNING `+jobCols,
		serverID, websiteID, jobType, payloadJSON, key,
	)
	return scanRow(row)
}

const jobCols = `id, server_id, website_id, type, status, payload, result, error, progress, progress_step, attempts, max_attempts, idempotency_key, claimed_at, lease_expires_at, finished_at, created_at`

// ClaimNext atomically claims the oldest pending, visible job for the given
// server. Uses FOR UPDATE SKIP LOCKED so multiple agents (or a retry storm)
// can never claim the same job twice, and stamps a lease the reaper checks.
func (s *Store) ClaimNext(ctx context.Context, serverID uuid.UUID) (*Job, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE jobs SET status = 'running', claimed_at = now(), attempts = attempts + 1, updated_at = now(),
			lease_expires_at = now() + (CASE
				WHEN type IN ('install_runtime','remove_runtime','install_extension','remove_extension',
				              'install_wordpress','install_database_tools','create_backup','restore_backup',
				              'build_app','clone_staging','promote_staging') THEN interval '30 minutes'
				ELSE interval '10 minutes' END)
		WHERE id = (
			SELECT j.id FROM jobs j
			JOIN servers sv ON sv.id = j.server_id
			WHERE j.server_id = $1 AND j.status = 'pending' AND sv.maintenance_mode = FALSE
			  AND j.visible_after <= now()
			ORDER BY j.created_at ASC
			FOR UPDATE OF j SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+jobCols,
		serverID)
	job, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return job, err
}

// ReportResult records the outcome of a claimed job. Failed jobs go back to
// pending with exponential backoff (visible_after) until max_attempts is
// reached, then fail terminally (dead-letter visible via ListFailed).
func (s *Store) ReportResult(ctx context.Context, jobID uuid.UUID, success bool, result any, errMsg string) (*Job, error) {
	resultJSON := []byte("{}")
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		resultJSON = b
	}

	var row pgx.Row
	if success {
		row = s.Pool.QueryRow(ctx, `
			UPDATE jobs SET status = 'success', result = $2, error = '', finished_at = now(), updated_at = now(),
				lease_expires_at = NULL
			WHERE id = $1 AND status = 'running'
			RETURNING `+jobCols,
			jobID, resultJSON)
	} else {
		row = s.Pool.QueryRow(ctx, `
			UPDATE jobs SET
				error = $2,
				status = (CASE WHEN attempts < max_attempts THEN 'pending'::job_status ELSE 'failed'::job_status END),
				finished_at = CASE WHEN attempts >= max_attempts THEN now() END,
				lease_expires_at = NULL,
				visible_after = CASE WHEN attempts < max_attempts
					THEN now() + (attempts * make_interval(secs => $3)) ELSE now() END,
				updated_at = now()
			WHERE id = $1 AND status = 'running'
			RETURNING `+jobCols,
			jobID, errMsg, s.backoff().Seconds())
	}
	job, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return job, err
}

// ReapExpiredLeases requeues jobs whose agent died mid-run (lease expired).
// Requeued jobs get the same backoff treatment as explicit failures; jobs
// past max_attempts fail terminally. Returns the affected jobs so callers can
// emit events.
func (s *Store) ReapExpiredLeases(ctx context.Context) ([]*Job, error) {
	rows, err := s.Pool.Query(ctx, `
		UPDATE jobs SET
			status = (CASE WHEN attempts < max_attempts THEN 'pending'::job_status ELSE 'failed'::job_status END),
			finished_at = CASE WHEN attempts >= max_attempts THEN now() END,
			error = 'lease expired (agent did not report in time)',
			lease_expires_at = NULL,
			visible_after = CASE WHEN attempts < max_attempts
				THEN now() + (attempts * make_interval(secs => $1)) ELSE now() END,
			updated_at = now()
		WHERE status = 'running' AND lease_expires_at IS NOT NULL AND lease_expires_at < now()
		RETURNING `+jobCols, s.backoff().Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) GetByID(ctx context.Context, jobID uuid.UUID) (*Job, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, server_id, website_id, type, status, payload, result, error, progress, progress_step, attempts, max_attempts, idempotency_key, claimed_at, lease_expires_at, finished_at, created_at
		FROM jobs WHERE id = $1
	`, jobID)
	job, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return job, err
}

func (s *Store) ListForWebsite(ctx context.Context, websiteID uuid.UUID, limit int) ([]Job, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, server_id, website_id, type, status, payload, result, error, progress, progress_step, attempts, max_attempts, idempotency_key, claimed_at, lease_expires_at, finished_at, created_at
		FROM jobs WHERE website_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, websiteID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		job, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *job)
	}
	return out, rows.Err()
}

// ScrubResult replaces a job's stored result (used to remove plaintext
// credentials after they have been encrypted into their owning record).
func (s *Store) ScrubResult(ctx context.Context, jobID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE jobs SET result = '{"scrubbed": true}'::jsonb WHERE id = $1`, jobID)
	return err
}

// EnqueueForWebsite resolves the website's server and enqueues a job linked
// to that website (used by domains/SSL and other website-scoped subsystems).
func (s *Store) EnqueueForWebsite(ctx context.Context, websiteID uuid.UUID, jobType Type, payload any) (*Job, error) {
	var serverID uuid.UUID
	err := s.Pool.QueryRow(ctx, `SELECT server_id FROM websites WHERE id = $1`, websiteID).Scan(&serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.Enqueue(ctx, serverID, &websiteID, jobType, payload)
}

func scanRow(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.ServerID, &j.WebsiteID, &j.Type, &j.Status, &j.Payload, &j.Result,
		&j.Error, &j.Progress, &j.ProgressStep, &j.Attempts, &j.MaxAttempts, &j.IdempotencyKey,
		&j.ClaimedAt, &j.LeaseExpiresAt, &j.FinishedAt, &j.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// ListByStatus returns recent jobs filtered by status ("" = all). Powers the
// dead-letter view for terminal failures (admin, Phase 2 hardening).
func (s *Store) ListByStatus(ctx context.Context, status string, limit int) ([]Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if status == "" {
		rows, err = s.Pool.Query(ctx, `
			SELECT `+jobCols+` FROM jobs ORDER BY created_at DESC LIMIT $1`, limit)
	} else {
		rows, err = s.Pool.Query(ctx, `
			SELECT `+jobCols+` FROM jobs WHERE status = $1::job_status ORDER BY created_at DESC LIMIT $2`, Status(status), limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// UpdateProgress records live progress (0-100) and a human-readable stage
// while a job is running; the UI polls it for real-time feedback.
func (s *Store) UpdateProgress(ctx context.Context, jobID uuid.UUID, progress int, step string) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE jobs SET progress = $2, progress_step = $3, updated_at = now() WHERE id = $1
	`, jobID, progress, step)
	return err
}

// ListRecentForServer returns the newest jobs for a server, optionally
// filtered by type (used by the setup wizard's live progress view).
func (s *Store) ListRecentForServer(ctx context.Context, serverID uuid.UUID, types []string, limit int) ([]Job, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	query := `
		SELECT id, server_id, website_id, type, status, payload, result, error, progress, progress_step, attempts, max_attempts, idempotency_key, claimed_at, lease_expires_at, finished_at, created_at
		FROM jobs WHERE server_id = $1`
	args := []any{serverID}
	if len(types) > 0 {
		query += ` AND type::text = ANY($2)`
		args = append(args, types)
	}
	query += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

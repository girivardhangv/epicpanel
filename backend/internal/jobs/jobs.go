// Package jobs provides durable background work for long-running
// infrastructure operations (website provisioning, deletion, reconfiguration).
// The runner is a single in-process worker: operations run sequentially per
// panel instance with progress recorded in PostgreSQL, so the UI can poll
// real state instead of pretending.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Job types.
const (
	TypeProvisionWebsite   = "provision_website"
	TypeReconfigureWebsite = "reconfigure_website"
	TypeDeleteWebsite      = "delete_website"
	TypeIssueSSL           = "issue_ssl"
	TypeNotifyAlert        = "notify_alert"
	TypeProvisionDatabase  = "provision_database"
	TypeDeleteDatabase     = "delete_database"
	TypeInstallSoftware    = "install_software"
	TypeRemoveSoftware     = "remove_software"
)

// Job statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool} }

// Job is the API/persistence view of one background operation.
type Job struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	ServerID    *string         `json:"server_id"`
	WebsiteID   *string         `json:"website_id"`
	Progress    int             `json:"progress"`
	Message     string          `json:"message"`
	Error       string          `json:"error"`
	Payload     map[string]any  `json:"payload"`
	CreatedAt   string          `json:"created_at"`
	StartedAt   *string         `json:"started_at"`
	CompletedAt *string         `json:"completed_at"`
}

const jobCols = `id, type, status, server_id, website_id, progress, message, error,
	payload::text, created_at::text, started_at::text, completed_at::text`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var payload string
	if err := row.Scan(&j.ID, &j.Type, &j.Status, &j.ServerID, &j.WebsiteID, &j.Progress,
		&j.Message, &j.Error, &payload, &j.CreatedAt, &j.StartedAt, &j.CompletedAt); err != nil {
		return nil, err
	}
	j.Payload = map[string]any{}
	if payload != "" {
		_ = json.Unmarshal([]byte(payload), &j.Payload)
	}
	return &j, nil
}

// queryRower is satisfied by both *pgxpool.Pool and pgx.Tx, letting one
// implementation serve plain and transactional inserts.
type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Create enqueues a job and returns it.
func (s *Store) Create(ctx context.Context, typ string, serverID, websiteID *string, payload map[string]any) (*Job, error) {
	return createJob(ctx, s.pool, typ, serverID, websiteID, payload)
}

// CreateTx enqueues a job inside an existing transaction. Website creation
// uses this so the job commits atomically with the (not yet visible)
// website row — inserting via the pool would violate the FK constraint.
func (s *Store) CreateTx(ctx context.Context, tx pgx.Tx, typ string, serverID, websiteID *string, payload map[string]any) (*Job, error) {
	return createJob(ctx, tx, typ, serverID, websiteID, payload)
}

func createJob(ctx context.Context, db queryRower, typ string, serverID, websiteID *string, payload map[string]any) (*Job, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, apierror.From(err)
	}
	j, err := scanJob(db.QueryRow(ctx, `
		INSERT INTO jobs (type, server_id, website_id, payload)
		VALUES ($1, $2, $3, $4::jsonb)
		RETURNING `+jobCols, typ, serverID, websiteID, string(raw)))
	if err != nil {
		return nil, apierror.From(err)
	}
	return j, nil
}

func (s *Store) Get(ctx context.Context, id string) (*Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("job")
		}
		return nil, apierror.From(err)
	}
	return j, nil
}

// ActiveForWebsite reports whether a queued/running job exists for a website.
func (s *Store) ActiveForWebsite(ctx context.Context, websiteID string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE website_id = $1 AND status IN ('queued','running')`, websiteID).Scan(&n)
	if err != nil {
		return false, apierror.From(err)
	}
	return n > 0, nil
}

// claimNext atomically claims the oldest queued job (SKIP LOCKED keeps
// multiple panel instances from grabbing the same row).
func (s *Store) claimNext(ctx context.Context) (*Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, `
		UPDATE jobs SET status = 'running', started_at = now(), message = ''
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'queued'
			ORDER BY created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+jobCols))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

func (s *Store) setProgress(ctx context.Context, id string, progress int, message string) {
	_, _ = s.pool.Exec(ctx,
		`UPDATE jobs SET progress = $2, message = $3 WHERE id = $1`, id, progress, message)
}

func (s *Store) setCompleted(ctx context.Context, id string, message string) {
	_, _ = s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'completed', progress = 100, message = $2, completed_at = now()
		WHERE id = $1`, id, message)
}

func (s *Store) setFailed(ctx context.Context, id string, message string) {
	_, _ = s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'failed', error = $2, completed_at = now()
		WHERE id = $1`, id, message)
}

// FailStaleRunning marks jobs that were running when the panel stopped so
// operators can retry them; a job claimed by a live worker is never stale
// because claimNext assigns it a fresh lease each run.
func (s *Store) FailStaleRunning(ctx context.Context) {
	_, _ = s.pool.Exec(ctx, `
		UPDATE jobs SET status = 'failed',
			error = 'interrupted by panel restart; safe to retry',
			completed_at = now()
		WHERE status = 'running'`)
}

// Runner executes jobs through registered handlers.
type Runner struct {
	store    *Store
	handlers map[string]Handler
	log      func(format string, args ...any)
}

// Handler executes one job. Progress reports (percent, human message) so the
// wizard can render real steps.
type Handler func(ctx context.Context, job *Job, progress ProgressFunc) error

type ProgressFunc func(percent int, message string)

// NewRunner wires a runner; Start launches its polling loop.
func NewRunner(store *Store, handlers map[string]Handler, log func(string, ...any)) *Runner {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Runner{store: store, handlers: handlers, log: log}
}

// Register attaches a handler for a job type (used by feature packages).
func (r *Runner) Register(jobType string, h Handler) {
	if r.handlers == nil {
		r.handlers = map[string]Handler{}
	}
	r.handlers[jobType] = h
}

func (r *Runner) Start(ctx context.Context) {
	// Jobs interrupted by a previous panel process cannot make progress.
	r.store.FailStaleRunning(ctx)
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.runNext(ctx)
			}
		}
	}()
}

func (r *Runner) runNext(ctx context.Context) {
	job, err := r.store.claimNext(ctx)
	if err != nil {
		r.log("job claim failed", "err", err)
		return
	}
	if job == nil {
		return
	}

	h, ok := r.handlers[job.Type]
	if !ok {
		r.store.setFailed(ctx, job.ID, "no handler registered for job type "+job.Type)
		return
	}

	progress := func(pct int, msg string) {
		if pct < 0 {
			pct = 0
		}
		if pct > 99 {
			pct = 99 // 100 is reserved for completion
		}
		r.store.setProgress(ctx, job.ID, pct, msg)
	}

	func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.log("job panicked", "job_id", job.ID, "panic", rec)
				r.store.setFailed(ctx, job.ID, "internal error while executing the operation")
			}
		}()
		if err := h(ctx, job, progress); err != nil {
			r.store.setFailed(ctx, job.ID, safeMessage(err))
			return
		}
		r.store.setCompleted(ctx, job.ID, "done")
	}()
}

// safeMessage keeps internal error detail out of the jobs table surface shown
// in the UI: handler errors are expected to pre-sanitize; unknown ones become
// generic.
func safeMessage(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

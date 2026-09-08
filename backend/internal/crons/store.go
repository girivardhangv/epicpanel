package crons

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("cron job not found")
	scheduleRe  = regexp.MustCompile(`^(\*|[0-9*/,-]+)(\s+(\*|[0-9*/,-]+)){4}$`)
)

// ValidSchedule checks standard 5-field cron syntax.
func ValidSchedule(s string) bool {
	if len(s) > 100 || !scheduleRe.MatchString(strings.TrimSpace(s)) {
		return false
	}
	// reject multiple spaces
	return len(strings.Fields(s)) == 5
}

// ValidateCommand rejects shell-injection vectors: no newlines, no backticks,
// no command chaining ( ; && || | ) — single simple command per cron entry.
func ValidCommand(c string) bool {
	if len(c) == 0 || len(c) > 500 {
		return false
	}
	if strings.ContainsAny(c, "\n\r`") {
		return false
	}
	for _, bad := range []string{";", "&&", "||", "|", "$((", "${", "$("} {
		if strings.Contains(c, bad) {
			return false
		}
	}
	// allow curl, php, wget, common binaries — but forbid obvious privesc
	lower := strings.ToLower(c)
	for _, dangerous := range []string{"sudo", "rm -rf /", "chmod 777 /", "passwd", "useradd", "userdel", "> /etc", ">> /etc"} {
		if strings.Contains(lower, dangerous) {
			return false
		}
	}
	return true
}

type CronJob struct {
	ID           uuid.UUID  `json:"id"`
	Organization uuid.UUID  `json:"organization_id"`
	WebsiteID    uuid.UUID  `json:"website_id"`
	Schedule     string     `json:"schedule"`
	Command      string     `json:"command"`
	Status       string     `json:"status"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, organization_id, website_id, schedule, command, status, last_run_at, created_at`

func scanRow(row pgx.Row) (*CronJob, error) {
	var c CronJob
	err := row.Scan(&c.ID, &c.Organization, &c.WebsiteID, &c.Schedule, &c.Command, &c.Status, &c.LastRunAt, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Store) Create(ctx context.Context, orgID, websiteID uuid.UUID, schedule, command string) (*CronJob, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO cron_jobs (organization_id, website_id, schedule, command)
		VALUES ($1, $2, $3, $4)
		RETURNING `+cols,
		orgID, websiteID, strings.TrimSpace(schedule), strings.TrimSpace(command),
	)
	return scanRow(row)
}

func (s *Store) GetByID(ctx context.Context, orgID, cronID uuid.UUID) (*CronJob, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM cron_jobs WHERE id = $1 AND organization_id = $2`, cronID, orgID)
	c, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *Store) ListForWebsite(ctx context.Context, websiteID uuid.UUID) ([]CronJob, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM cron_jobs WHERE website_id = $1 ORDER BY created_at ASC`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronJob
	for rows.Next() {
		c, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) SetStatus(ctx context.Context, orgID, cronID uuid.UUID, status string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE cron_jobs SET status = $3 WHERE id = $1 AND organization_id = $2`, cronID, orgID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, orgID, cronID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM cron_jobs WHERE id = $1 AND organization_id = $2`, cronID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SiteRow pairs a cron list with the website's server for sync jobs.
type SiteCron struct {
	ServerID  uuid.UUID
	WebsiteID uuid.UUID
}

// WebsitesWithCrons returns all sites that have at least one cron entry.
func (s *Store) WebsitesWithCrons(ctx context.Context) ([]SiteCron, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT w.server_id, w.id
		FROM cron_jobs c JOIN websites w ON w.id = c.website_id
		WHERE w.status IN ('pending','provisioning','ready','failed')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SiteCron
	for rows.Next() {
		var r SiteCron
		if err := rows.Scan(&r.ServerID, &r.WebsiteID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CronEntry mirrors the agent's wire shape for crontab rendering.
type CronEntry struct {
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Status   string `json:"status"`
}

// EntriesForWebsite returns the desired cron entries for a site.
func (s *Store) EntriesForWebsite(ctx context.Context, websiteID uuid.UUID) ([]CronEntry, error) {
	rows, err := s.Pool.Query(ctx, `SELECT schedule, command, status FROM cron_jobs WHERE website_id = $1`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CronEntry
	for rows.Next() {
		var e CronEntry
		if err := rows.Scan(&e.Schedule, &e.Command, &e.Status); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

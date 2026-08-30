// Package installer implements the first-run installation workflow.
//
// Installation state lives in the database (installations table), not in
// frontend routing, so once status flips to 'completed' every installer
// endpoint refuses to operate. The wizard UI merely mirrors this state.
package installer

import (
	"context"
	"errors"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/licensing"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	StatusPending   = "pending"
	StatusCompleted = "completed"

	SettingsKeySiteName      = "panel.site_name"
	SettingsKeyPanelTimeZone = "panel.timezone"
	SettingsKeySecurityDone  = "security.configured"
	SettingsKeyReqsSnapshot  = "installer.requirements"
)

type Service struct {
	pool       *pgxpool.Pool
	lic        licensing.Service
	auditSvc   *audit.Service
	settingSvc *settings.Service

	userStore    UserCreator
	dbVerifier   func(ctx context.Context) error // verifies configured DSN reachability
	dsnPersister func(dsn string) error          // persists an operator-provided DSN to config; triggers restart guidance
	version      string
}

// UserCreator abstracts first-administrator provisioning; the concrete
// implementation hashes passwords and assigns roles through RBAC.
type UserCreator interface {
	CreateAdministrator(ctx context.Context, username, email, displayName, password, confirm string) error
	CountUsers(ctx context.Context) (int64, error)
}

type Deps struct {
	Pool         *pgxpool.Pool
	Licensing    licensing.Service
	Audit        *audit.Service
	Settings     *settings.Service
	UserStore    UserCreator
	DBVerifier   func(ctx context.Context) error
	DSNPersister func(dsn string) error
	Version      string
}

func New(d Deps) *Service { return &Service{d.Pool, d.Licensing, d.Audit, d.Settings, d.UserStore, d.DBVerifier, d.DSNPersister, d.Version} }

var ErrInstallerLocked = errors.New("installer locked")

// LoadStatus reads canonical installation state straight from the database.
func (s *Service) LoadStatus(ctx context.Context) (*Row, error) {
	row := &Row{}
	var completedAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT id, status, instance_id::text, started_at, completed_at, COALESCE(panel_version,'')
		 FROM installations WHERE id = 1`).
		Scan(&row.ID, &row.Status, &row.InstanceID, &row.StartedAt, &completedAt, &row.PanelVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Migration guarantees the row; recreate defensively so the panel stays operable.
			if _, insErr := s.pool.Exec(ctx,
				`INSERT INTO installations (id, instance_id) VALUES (1, gen_random_uuid())
				 ON CONFLICT (id) DO NOTHING`); insErr != nil {
				return nil, insErr
			}
			err = s.pool.QueryRow(ctx,
				`SELECT id, status, instance_id::text, started_at, completed_at, COALESCE(panel_version,'')
				 FROM installations WHERE id = 1`).
				Scan(&row.ID, &row.Status, &row.InstanceID, &row.StartedAt, &completedAt, &row.PanelVersion)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	row.CompletedAt = completedAt
	return row, nil
}

type Row struct {
	ID           int
	Status       string
	InstanceID   string
	StartedAt    time.Time
	CompletedAt  *time.Time
	PanelVersion string
}

// RequireNotLocked fails any mutating installer call after completion.
func (s *Service) RequireNotLocked(ctx context.Context) (*Row, error) {
	row, err := s.LoadStatus(ctx)
	if err != nil {
		return nil, apierror.From(err)
	}
	if row.Status == StatusCompleted || row.CompletedAt != nil {
		return row, ErrInstallerLocked
	}
	return row, nil
}

// ---------------------------------------------------------------- steps -----

// StepVerification produces per-step completion booleans for the wizard UI.
func (s *Service) StepVerification(ctx context.Context) map[string]bool {
	steps := map[string]bool{}

	_, err := s.RequireNotLocked(ctx)
	steps["completed"] = errors.Is(err, ErrInstallerLocked)

	if _, rerr := s.CheckRequirements(ctx); rerr == nil {
		steps["requirements"] = true
	} else {
		steps["requirements"] = false
	}

	// Database: the API is talking to PostgreSQL right now, so success == reachable.
	steps["database"] = s.dbVerifier(ctx) == nil

	steps["license"] = s.lic.Usable(ctx)

	if name := s.settingSvc.String(ctx, SettingsKeySiteName, ""); name != "" {
		steps["configuration"] = true
	} else {
		steps["configuration"] = false
	}

	count, cerr := s.userStore.CountUsers(ctx)
	steps["administrator"] = cerr == nil && count > 0

	steps["security"] = s.settingSvc.String(ctx, SettingsKeySecurityDone, "") == "true"

	return steps
}

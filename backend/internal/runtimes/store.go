package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

var (
	ErrNotFound        = errors.New("runtime not found")
	ErrDuplicate       = errors.New("runtime version already installed or being installed")
	ErrInUse           = errors.New("runtime version is in use by websites")
	ErrTypeUnsupported = errors.New("unsupported runtime type")
)

var (
	versionRe      = regexp.MustCompile(`^[0-9]+\.[0-9]+$`) // php, python: "8.3", "3.12"
	versionMajorRe = regexp.MustCompile(`^[0-9]+$`)         // node, go: "22", "1"
)

// ValidVersionForType validates per runtime type: node takes a single major
// ("22"); go takes major.minor ("1.22" — the agent resolves the newest patch
// from go.dev); php/python take major.minor ("8.3", "3.12"); java takes a
// single major ("21", "17", "8" — the agent resolves the newest build). The
// old one-size regex rejected "22" for Node — field-reported bug.
func ValidVersionForType(t Type, v string) bool {
	switch t {
	case TypeNode, TypeJava:
		return versionMajorRe.MatchString(v)
	case TypePhpMyAdmin, TypeAdminer, TypeRedis:
		return v == "latest"
	default:
		return versionRe.MatchString(v)
	}
}

type Type string

const (
	TypePHP         Type = "php"
	TypeNode        Type = "node"
	TypePython      Type = "python"
	TypeGo          Type = "go"
	TypeApache      Type = "apache"
	TypeOpenLiteSpd Type = "openlitespeed"
	TypeJava        Type = "java"
	TypePhpMyAdmin  Type = "phpmyadmin"
	TypeAdminer     Type = "adminer"
	TypeRedis       Type = "redis"
)

type Status string

const (
	StatusInstalling Status = "installing"
	StatusAvailable  Status = "available"
	StatusFailed     Status = "failed"
	StatusRemoving   Status = "removing"
)

// Provider sets the driver used on the server for this runtime type.
// PHP: OS packages (distro repos, Ondřej PPA fallback on Debian/Ubuntu).
type Runtime struct {
	ID           uuid.UUID `json:"id"`
	ServerID     uuid.UUID `json:"server_id"`
	Type         Type      `json:"type"`
	Version      string    `json:"version"`
	Status       Status    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

// createdByUUID is the system actor for detect-software adoption inserts
// (runtimes.created_by is NOT NULL; a nil UUID denotes "system").
var createdByUUID = uuid.Nil

const cols = `id, server_id, type, version, status, error_message, created_at`

func scanRow(row pgx.Row) (*Runtime, error) {
	var r Runtime
	err := row.Scan(&r.ID, &r.ServerID, &r.Type, &r.Version, &r.Status, &r.ErrorMessage, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func ValidType(t string) bool {
	switch Type(t) {
	case TypePHP, TypeNode, TypePython, TypeGo, TypeApache, TypeOpenLiteSpd, TypeJava, TypePhpMyAdmin, TypeAdminer, TypeRedis:
		return true
	}
	return false
}

func ValidVersion(v string) bool { return versionRe.MatchString(v) }

// Create registers an installing runtime. If a previous attempt for the same
// (server, type, version) FAILED, the row is reset and returned so the caller
// can retry — failed installs never block re-installation. Rows in active
// states (installing/available/removing) return ErrDuplicate.
func (s *Store) Create(ctx context.Context, serverID, createdBy uuid.UUID, t Type, version string) (*Runtime, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO runtimes (server_id, type, version, status, created_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (server_id, type, version) DO UPDATE
			SET status = 'installing', error_message = '', updated_at = now()
			WHERE runtimes.status = 'failed'
		RETURNING `+cols,
		serverID, t, version, StatusInstalling, createdBy,
	)
	r, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDuplicate
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

// CreateAvailable registers an already-installed runtime directly as
// available (used by detect-software adoption; system user UUID as creator).
func (s *Store) CreateAvailable(ctx context.Context, serverID uuid.UUID, t string, version string) (*Runtime, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO runtimes (server_id, type, version, status, created_by)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (server_id, type, version) DO NOTHING
		RETURNING `+cols,
		serverID, Type(t), version, StatusAvailable, createdByUUID,
	)
	r, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDuplicate
	}
	return r, err
}

func (s *Store) GetByID(ctx context.Context, serverID, runtimeID uuid.UUID) (*Runtime, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM runtimes WHERE id = $1 AND server_id = $2`, runtimeID, serverID)
	r, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// GetByTypeVersion resolves a runtime row for a server by (type, version).
func (s *Store) GetByTypeVersion(ctx context.Context, serverID uuid.UUID, t Type, version string) (*Runtime, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM runtimes WHERE server_id = $1 AND type = $2 AND version = $3`, serverID, t, version)
	r, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

func (s *Store) ListForServer(ctx context.Context, serverID uuid.UUID) ([]Runtime, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM runtimes WHERE server_id = $1 ORDER BY type, version`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Runtime
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) SetStatus(ctx context.Context, runtimeID uuid.UUID, status Status, errMsg string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE runtimes SET status = $2, error_message = $3, updated_at = now() WHERE id = $1
	`, runtimeID, status, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CountWebsitesUsing returns how many websites reference (server, type, version).
func (s *Store) CountWebsitesUsing(ctx context.Context, serverID uuid.UUID, t Type, version string) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM websites
		WHERE server_id = $1 AND runtime = $2 AND runtime_version = $3 AND status != 'deleted'
	`, serverID, string(t), version).Scan(&n)
	return n, err
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

// jobPayloadID matches the runtime_id carried in install/remove job payloads.
type jobPayloadID struct {
	RuntimeID string `json:"runtime_id"`
}

// ApplyJobOutcome advances the runtime registry for finished install/remove
// jobs. Only terminal states mutate the registry: retryable failures
// (job back to pending) leave the current status untouched.
func (s *Store) ApplyJobOutcome(ctx context.Context, job *jobs.Job, errMsg string) {
	switch job.Type {
	case jobs.TypeInstallRuntime, jobs.TypeRemoveRuntime:
	default:
		return
	}
	if job.Status != jobs.StatusSuccess && job.Status != jobs.StatusFailed {
		return
	}

	var p jobPayloadID
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.RuntimeID == "" {
		slog.Error("runtime job payload missing runtime_id", "job", job.ID, "err", err)
		return
	}
	rtID, err := uuid.Parse(p.RuntimeID)
	if err != nil {
		slog.Error("runtime job payload has invalid runtime_id", "job", job.ID, "runtime_id", p.RuntimeID)
		return
	}

	if job.Type == jobs.TypeInstallRuntime {
		if job.Status == jobs.StatusSuccess {
			if err := s.SetStatus(ctx, rtID, StatusAvailable, ""); err != nil {
				slog.Error("runtime mark available failed", "runtime", rtID, "err", err)
			}
			return
		}
		if err := s.SetStatus(ctx, rtID, StatusFailed, errMsg); err != nil {
			slog.Error("runtime mark failed failed", "runtime", rtID, "err", err)
		}
		return
	}

	if job.Status == jobs.StatusSuccess {
		if err := s.DeleteByID(ctx, rtID); err != nil {
			slog.Error("runtime delete after removal failed", "runtime", rtID, "err", err)
		}
		return
	}
	if err := s.SetStatus(ctx, rtID, StatusFailed, errMsg); err != nil {
		slog.Error("runtime mark failed after removal failed", "runtime", rtID, "err", err)
	}
}

// DeleteByID removes a runtime registry row (after successful removal).
func (s *Store) DeleteByID(ctx context.Context, runtimeID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM runtimes WHERE id = $1`, runtimeID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

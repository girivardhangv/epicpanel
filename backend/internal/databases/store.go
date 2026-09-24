package databases

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

var (
	ErrNotFound  = errors.New("database not found")
	ErrDuplicate = errors.New("database name already in use on this server")
	ErrNoSecret  = secretbox.ErrNotConfigured
)

type Engine string

const (
	EngineMySQL      Engine = "mysql"
	EngineMariaDB    Engine = "mariadb"
	EnginePostgreSQL Engine = "postgresql"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusCreating Status = "creating"
	StatusReady    Status = "ready"
	StatusFailed   Status = "failed"
	StatusDeleting Status = "deleting"
)

var nameRe = regexp.MustCompile(`^ep_[a-z0-9_]{1,60}$`)

type Database struct {
	ID            uuid.UUID  `json:"id"`
	Organization  uuid.UUID  `json:"organization_id"`
	ServerID      uuid.UUID  `json:"server_id"`
	WebsiteID     *uuid.UUID `json:"website_id,omitempty"`
	Engine        Engine     `json:"engine"`
	Name          string     `json:"name"`
	DBUser        string     `json:"db_user"`
	Status        Status     `json:"status"`
	Purpose       string     `json:"purpose,omitempty"`
	ErrorMessage  string     `json:"error_message,omitempty"`
	ProvisionedAt *time.Time `json:"provisioned_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// DerivedName builds the panel-managed database/user name from the org and
// a caller-chosen label: ep_<org8>_<label>. Deterministic, collision-safe
// per server+engine (unique index), and always matches the SQL-safe pattern.
func DerivedName(orgID uuid.UUID, label string) (string, error) {
	if !labelRe.MatchString(label) {
		return "", errors.New("label must be 1-30 lowercase letters, digits or underscores")
	}
	org := orgID.String()
	if len(org) > 8 {
		org = org[:8]
	}
	return "ep_" + org + "_" + label, nil
}

var labelRe = regexp.MustCompile(`^[a-z0-9_]{1,30}$`)

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, organization_id, server_id, website_id, engine, name, db_user, purpose, status, error_message, provisioned_at, created_at`

func scanRow(row pgx.Row) (*Database, error) {
	var d Database
	err := row.Scan(&d.ID, &d.Organization, &d.ServerID, &d.WebsiteID, &d.Engine, &d.Name, &d.DBUser, &d.Purpose,
		&d.Status, &d.ErrorMessage, &d.ProvisionedAt, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func ValidEngine(e string) bool {
	switch Engine(e) {
	case EngineMySQL, EngineMariaDB, EnginePostgreSQL:
		return true
	}
	return false
}

// CreatePassword generates a 32-char URL-safe random password.
func CreatePassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create inserts a pending database row; password must be encrypted by the
// caller (fanout after agent success). Password is stored immediately so the
// job outcome handler can persist it on success. createdBy may be nil for
// system-initiated databases (e.g. the WordPress auto-provisioning flow).
func (s *Store) Create(ctx context.Context, orgID, serverID uuid.UUID, createdBy *uuid.UUID, websiteID *uuid.UUID, engine Engine, name, dbUser string) (*Database, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO databases (organization_id, server_id, website_id, engine, name, db_user, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+cols,
		orgID, serverID, websiteID, engine, name, dbUser, StatusPending, createdBy,
	)
	d, err := scanRow(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	return d, nil
}

func (s *Store) GetByID(ctx context.Context, orgID, dbID uuid.UUID) (*Database, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM databases WHERE id = $1 AND organization_id = $2`, dbID, orgID)
	d, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (s *Store) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]Database, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM databases WHERE organization_id = $1 ORDER BY created_at ASC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Database
	for rows.Next() {
		d, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// SetPurpose tags a database with a workflow purpose (e.g. "wordpress" for
// the one-click installer chaining).
func (s *Store) SetPurpose(ctx context.Context, dbID uuid.UUID, purpose string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE databases SET purpose = $2, updated_at = now() WHERE id = $1`, dbID, purpose)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetByIDAny resolves a database without org scoping (internal job fanout).
func (s *Store) GetByIDAny(ctx context.Context, dbID uuid.UUID) (*Database, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM databases WHERE id = $1`, dbID)
	d, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (s *Store) SetStatus(ctx context.Context, dbID uuid.UUID, status Status, errMsg string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE databases SET status = $2, error_message = $3, updated_at = now() WHERE id = $1`, dbID, status, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkReady stores the encrypted password and flips status to ready.
func (s *Store) MarkReady(ctx context.Context, dbID uuid.UUID, encryptedPassword []byte, fingerprint string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE databases SET status = 'ready', password_encrypted = $2, password_fingerprint = $3,
		       provisioned_at = now(), error_message = '', updated_at = now()
		WHERE id = $1
	`, dbID, encryptedPassword, fingerprint)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevealPassword decrypts the stored credential (caller must audit).
func (s *Store) RevealPassword(ctx context.Context, dbID uuid.UUID) (string, error) {
	var enc []byte
	err := s.Pool.QueryRow(ctx, `SELECT password_encrypted FROM databases WHERE id = $1`, dbID).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if enc == nil {
		return "", errors.New("credential not available yet")
	}
	return secretbox.Decrypt(enc)
}

func (s *Store) Delete(ctx context.Context, orgID, dbID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM databases WHERE id = $1 AND organization_id = $2`, dbID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteByServer removes the database row after a successful agent-side drop.
// Scoped by server so a compromised agent can only affect its own rows.
func (s *Store) DeleteByServer(ctx context.Context, serverID, dbID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM databases WHERE id = $1 AND server_id = $2`, dbID, serverID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

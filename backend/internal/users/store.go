package users

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrEmailTaken = errors.New("email already registered")

type User struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	IsAdmin      bool      `json:"is_platform_admin"`
	Status       string    `json:"status"`
	MFAEnabled   bool      `json:"mfa_enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	PasswordHash string    `json:"-"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, email, password_hash, name, is_platform_admin, status, mfa_enabled, created_at, updated_at`

func scanRow(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.IsAdmin, &u.Status, &u.MFAEnabled, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// Create inserts a user. The very first user in the system becomes a platform admin.
// Returns the created user and whether it was the first (bootstrap) user.
func (s *Store) Create(ctx context.Context, email, passwordHash, name string) (*User, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)

	// Serialize the bootstrap check so concurrent first registrations cannot
	// both observe count==0 and mint two platform admins (audit finding).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('epicpanel:first-user'))`); err != nil {
		return nil, false, err
	}

	var count int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return nil, false, err
	}
	isFirst := count == 0

	row := tx.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, name, is_platform_admin)
		VALUES ($1, $2, $3, $4)
		RETURNING `+cols,
		email, passwordHash, name, isFirst,
	)
	u, err := scanRow(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, false, ErrEmailTaken
		}
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return u, isFirst, nil
}

func (s *Store) GetByEmail(ctx context.Context, email string) (*User, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM users WHERE email = $1`, email)
	u, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*User, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM users WHERE id = $1`, id)
	u, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (s *Store) List(ctx context.Context, limit, offset int) ([]User, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+cols+` FROM users ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.IsAdmin, &u.Status, &u.MFAEnabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

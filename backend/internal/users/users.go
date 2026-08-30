// Package users provides user persistence used by authentication,
// administration and the installer.
package users

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrDuplicate = errors.New("users: username or email already exists")

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// ValidateEmail performs basic syntactic validation of an e-mail address.
func ValidateEmail(s string) bool {
	return s != "" && len(s) <= 254 && emailRe.MatchString(s)
}

// ValidateUsername enforces a strict, predictable username charset so values
// can safely appear in logs, URLs and future shell contexts.
func ValidateUsername(s string) error {
	if len(s) < 3 || len(s) > 64 {
		return errors.New("username must be between 3 and 64 characters")
	}
	for _, r := range s {
		ok := r == '.' || r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return errors.New("username may only contain letters, digits, '.', '-' and '_'")
		}
	}
	return nil
}

type Record struct {
	ID           string
	Username     string
	Email        *string
	DisplayName  string
	PasswordHash string
	IsActive     bool
}

func NormalizeIdentifier(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const selectCols = `id, username, email, display_name, password_hash, is_active`

func scanRecord(row interface {
	Scan(dest ...any) error
}) (*Record, error) {
	var u Record
	var email *string
	if err := row.Scan(&u.ID, &u.Username, &email, &u.DisplayName, &u.PasswordHash, &u.IsActive); err != nil {
		return nil, err
	}
	u.Email = email
	return &u, nil
}

func (s *Store) FindByIdentifier(ctx context.Context, ident string) (*Record, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+selectCols+` FROM users
		WHERE lower(username) = $1 OR (lower(email) = $1 AND $1 <> '')`,
		NormalizeIdentifier(ident))
	u, err := scanRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return u, nil
}

func (s *Store) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// UpdatePassword swaps the hash and clears lockout state.
func (s *Store) UpdatePassword(ctx context.Context, userID, newHash string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET password_hash = $2, failed_login_count = 0, locked_until = NULL, updated_at = now()
		WHERE id = $1`, userID, newHash)
	return err
}

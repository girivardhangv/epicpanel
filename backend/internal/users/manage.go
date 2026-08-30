// User administration operations (list/create/update/delete) backing the
// Management API. Safety rules live here, never in handlers:
//   - the last active super_admin cannot be deactivated, demoted or deleted
//   - usernames/e-mails are validated before hitting unique DB constraints
package users

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	auth "github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("users: account not found")

type View struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	Email       *string    `json:"email"`
	DisplayName string     `json:"display_name"`
	IsActive    bool       `json:"is_active"`
	Roles       []string   `json:"roles"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

type Manager struct {
	pool *pgxpool.Pool
}

func NewManager(pool *pgxpool.Pool) *Manager { return &Manager{pool} }

const viewCols = `
	u.id::text, u.username, u.email::text, COALESCE(u.display_name,''), u.is_active,
	u.last_login_at, u.created_at`

const rolesSubquery = `
	COALESCE((SELECT json_agg(r.name ORDER BY r.name)
	          FROM user_roles ur JOIN roles r ON r.id = ur.role_id
	          WHERE ur.user_id = u.id), '[]'::json)`

func scanViewRow(row pgxScanTarget) (*View, error) {
	var v View
	var email *string
	var rolesJSON []byte
	err := row.Scan(&v.ID, &v.Username, &email, &v.DisplayName, &v.IsActive,
		&v.LastLoginAt, &v.CreatedAt, &rolesJSON)
	if err != nil {
		return nil, err
	}
	v.Email = email
	v.Roles = []string{}
	if len(rolesJSON) > 0 {
		if err := json.Unmarshal(rolesJSON, &v.Roles); err != nil {
			return nil, fmt.Errorf("decode roles: %w", err)
		}
	}
	return &v, nil
}

type pgxScanTarget interface {
	Scan(dest ...any) error
}

func mapDuplicate(err error) error {
	if err != nil && isDuplicateError(err.Error()) {
		return apierror.Conflict("Username or email already exists")
	}
	return err
}

func isDuplicateError(msg string) bool {
	return strings.Contains(msg, "SQLSTATE 23505") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "users_username_lower_key") ||
		strings.Contains(msg, "users_email_lower_key")
}

// List returns every account together with aggregated role names.
func (m *Manager) List(ctx context.Context) ([]*View, error) {
	rows, err := m.pool.Query(ctx, `
		SELECT `+viewCols+`, `+rolesSubquery+`
		FROM users u ORDER BY u.created_at ASC`)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()

	out := []*View{}
	for rows.Next() {
		v, err := scanViewRow(rows)
		if err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (m *Manager) Get(ctx context.Context, id string) (*View, error) {
	row := m.pool.QueryRow(ctx, `
		SELECT `+viewCols+`, `+rolesSubquery+` FROM users u WHERE u.id = $1`, id)
	v, err := scanViewRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, apierror.From(err)
	}
	return v, nil
}

type CreateInput struct {
	Username    string
	Email       string
	DisplayName string
	Password    string
	RoleNames   []string
	MinLength   int
	Classes     int
}

// Create provisions an account end-to-end: server-side password policy,
// Argon2id hashing, optional role bindings. Returns the created view.
func (m *Manager) Create(ctx context.Context, in CreateInput) (*View, error) {
	in.Username = strings.TrimSpace(in.Username)
	in.Email = strings.TrimSpace(in.Email)

	if err := ValidateUsername(in.Username); err != nil {
		return nil, apierror.BadRequest(err.Error())
	}
	if problems := auth.ValidatePolicy(in.Password, in.MinLength, in.Classes); len(problems) > 0 {
		return nil, apierror.New(422, "WEAK_PASSWORD",
			"Password requirements not met: "+strings.Join(problems, "; "))
	}

	hash, err := auth.Hash(in.Password)
	if err != nil {
		return nil, apierror.From(err)
	}

	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, apierror.From(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID string
	emailArg := any(nil)
	if in.Email != "" {
		emailArg = strings.ToLower(in.Email)
	}
	insertErr := tx.QueryRow(ctx, `
		INSERT INTO users (username, email, display_name, password_hash)
		VALUES ($1, $2, NULLIF($3,''), $4) RETURNING id::text`,
		in.Username, emailArg, strings.TrimSpace(in.DisplayName), hash).Scan(&newID)
	if insertErr != nil {
		return nil, apierror.From(mapDuplicate(insertErr))
	}
	for _, role := range in.RoleNames {
		tag, err := tx.Exec(ctx, `
			INSERT INTO user_roles (user_id, role_id)
			SELECT $1::uuid, id FROM roles WHERE name = $2
			ON CONFLICT DO NOTHING`, newID, role)
		if err != nil {
			return nil, apierror.From(err)
		}
		if tag.RowsAffected() == 0 {
			return nil, apierror.BadRequest("Unknown role: "+role)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.From(err)
	}

	return m.Get(ctx, newID)
}

type UpdateInput struct {
	DisplayName *string
	Email       *string // empty string clears
	IsActive    *bool
	RoleNames   *[]string
}

// Update mutates an account applying last-super-admin protection.
func (m *Manager) Update(ctx context.Context, actorID, targetID string, in UpdateInput) (*View, error) {
	if _, err := uuid.Parse(targetID); err != nil {
		return nil, ErrNotFound
	}

	// Guard rules that could lock out administration entirely.
	deactivate := in.IsActive != nil && !*in.IsActive
	demote := in.RoleNames != nil && !containsString(*in.RoleNames, "super_admin")
	if deactivate || demote {
		protected, err := m.isProtectedSuperAdmin(ctx, targetID)
		if err != nil {
			return nil, apierror.From(err)
		}
		if protected {
			return nil, apierror.Conflict(
				"This account holds the only active super_admin role; promote another administrator first")
		}
	}

	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, apierror.From(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if in.DisplayName != nil {
		display := strings.TrimSpace(*in.DisplayName)
		if len(display) > 128 {
			return nil, apierror.BadRequest("Display name too long")
		}
		if _, err := tx.Exec(ctx,
			`UPDATE users SET display_name = NULLIF($2,''), updated_at = now() WHERE id = $1`,
			targetID, display); err != nil {
			return nil, apierror.From(err)
		}
	}
	if in.Email != nil {
		emailArg := any(nil)
		cleaned := strings.ToLower(strings.TrimSpace(*in.Email))
		if cleaned != "" && !ValidateEmail(cleaned) {
			return nil, apierror.BadRequest("Invalid email address")
		}
		if cleaned != "" {
			emailArg = cleaned
		}
		if _, err := tx.Exec(ctx,
			`UPDATE users SET email = $2, updated_at = now() WHERE id = $1`,
			targetID, emailArg); err != nil {
			return nil, apierror.From(mapDuplicate(err))
		}
	}
	if in.IsActive != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE users SET is_active = $2, failed_login_count = 0, locked_until = NULL,
			        updated_at = now() WHERE id = $1`,
			targetID, *in.IsActive); err != nil {
			return nil, apierror.From(err)
		}
	}
	if in.RoleNames != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, targetID); err != nil {
			return nil, apierror.From(err)
		}
		seen := map[string]bool{}
		for _, role := range *in.RoleNames {
			if seen[role] {
				continue
			}
			seen[role] = true
			tag, err := tx.Exec(ctx, `
				INSERT INTO user_roles (user_id, role_id, granted_by)
				SELECT $1::uuid, id, $3::uuid FROM roles WHERE name = $2
				ON CONFLICT DO NOTHING`, targetID, role, nullableUUID(actorID))
			if err != nil {
				return nil, apierror.From(err)
			}
			if tag.RowsAffected() == 0 {
				return nil, apierror.BadRequest("Unknown role: "+role)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.From(err)
	}
	return m.Get(ctx, targetID)
}

// Delete removes an account permanently. Actors may not delete themselves;
// deleting the only active super_admin is refused.
func (m *Manager) Delete(ctx context.Context, actorID, targetID string) error {
	if actorID != "" && actorID == targetID {
		return apierror.Conflict("You cannot delete your own account")
	}
	protected, err := m.isProtectedSuperAdmin(ctx, targetID)
	if err != nil {
		return apierror.From(err)
	}
	if protected {
		return apierror.Conflict(
			"This account holds the only active super_admin role; promote another administrator first")
	}
	tag, err := m.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, targetID)
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AdminSetPassword rotates an account password on behalf of an administrator
// without knowing the previous one, and revokes every session of the target.
func (m *Manager) AdminSetPassword(ctx context.Context, targetID, newPassword string, minLength, classes int) error {
	if problems := auth.ValidatePolicy(newPassword, minLength, classes); len(problems) > 0 {
		return apierror.New(422, "WEAK_PASSWORD",
			"Password requirements not met: "+strings.Join(problems, "; "))
	}
	hash, err := auth.Hash(newPassword)
	if err != nil {
		return apierror.From(err)
	}
	tag, err := m.pool.Exec(ctx, `
		UPDATE users SET password_hash = $2, failed_login_count = 0, locked_until = NULL,
		                 updated_at = now()
		WHERE id = $1`, targetID, hash)
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = m.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now(), revoked_reason = 'admin_password_reset'
		 WHERE user_id = $1 AND revoked_at IS NULL`, targetID)
	if err != nil {
		return apierror.From(err)
	}
	return nil
}

func (m *Manager) isProtectedSuperAdmin(ctx context.Context, targetID string) (bool, error) {
	var hasSuperAdmin, otherActiveSupers bool
	err := m.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
			WHERE ur.user_id = $1 AND r.name = 'super_admin'
		),
		EXISTS (
			SELECT 1 FROM users u2
			JOIN user_roles ur2 ON ur2.user_id = u2.id AND u2.is_active AND u2.id <> $1
			JOIN roles r2 ON r2.id = ur2.role_id
			WHERE r2.name = 'super_admin'
		)`, targetID).Scan(&hasSuperAdmin, &otherActiveSupers)
	if err != nil {
		return false, err
	}
	return hasSuperAdmin && !otherActiveSupers, nil
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	if _, err := uuid.Parse(s); err != nil {
		return nil
	}
	return s
}

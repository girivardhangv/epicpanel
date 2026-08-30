// Package rbac implements role-based access control lookups.
// Authorization decisions must always flow through this layer; hard-coding
// user identities or e-mail checks elsewhere is forbidden by the security
// charter.
package rbac

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// PermissionsForUser returns every distinct permission code granted to the
// user through any of their roles.
func (s *Service) PermissionsForUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT p.code
		FROM users u
		JOIN user_roles ur      ON ur.user_id = u.id AND u.is_active
		JOIN role_permissions rp ON rp.role_id = ur.role_id
		JOIN permissions p       ON p.id = rp.permission_id
		WHERE u.id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var perms []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		perms = append(perms, code)
	}
	if perms == nil {
		perms = []string{}
	}
	return perms, rows.Err()
}

type Role struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	IsSystem    bool    `json:"is_system"`
	PermissionCount int64 `json:"permission_count"`
}

func (s *Service) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.name, r.description, r.is_system,
		       (SELECT count(*) FROM role_permissions rp WHERE rp.role_id = r.id) AS perm_count
		FROM roles r ORDER BY r.is_system DESC, r.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		var ro Role
		if err := rows.Scan(&ro.ID, &ro.Name, &ro.Description, &ro.IsSystem, &ro.PermissionCount); err != nil {
			return nil, err
		}
		out = append(out, ro)
	}
	return out, rows.Err()
}

type Permission struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

func (s *Service) ListPermissions(ctx context.Context) ([]Permission, error) {
	rows, err := s.pool.Query(ctx, `SELECT code, description FROM permissions ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Permission{}
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.Code, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AssignRole attaches a role to a user. Returns false when either does not exist.
func (s *Service) AssignRole(ctx context.Context, userID, roleName, grantedBy string) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id, granted_by)
		SELECT $1, r.id, NULLIF($3, '')::uuid
		FROM roles r WHERE r.name = $2
		ON CONFLICT DO NOTHING`, userID, roleName, grantedBy)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnknownRoleOrUser
	}
	return nil
}

var ErrUnknownRoleOrUser = errors.New("unknown role or user")

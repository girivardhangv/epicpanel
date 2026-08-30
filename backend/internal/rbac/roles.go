// Role administration: create/update/delete roles and bind permissions.
// System-seeded roles are immutable except for user_bindings; this protects
// the permission baseline the product relies on.
package rbac

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/jackc/pgx/v5"
)

var ErrSystemRole = errors.New("rbac: system role cannot be modified or deleted")

type RoleView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	IsSystem    bool     `json:"is_system"`
	Permissions []string `json:"permissions"`
	UserCount   int64    `json:"user_count"`
}

const roleDetailCols = `
	r.id::text, r.name, r.description, r.is_system,
	COALESCE((SELECT json_agg(p.code ORDER BY p.code)
	          FROM role_permissions rp JOIN permissions p ON p.id = rp.permission_id
	          WHERE rp.role_id = r.id), '[]'::jsonb),
	(SELECT count(*) FROM user_roles ur WHERE ur.role_id = r.id)`

func scanRole(row interface{ Scan(...any) error }) (*RoleView, error) {
	var v RoleView
	var perms []byte
	err := row.Scan(&v.ID, &v.Name, &v.Description, &v.IsSystem, &perms, &v.UserCount)
	if err != nil {
		return nil, err
	}
	v.Permissions = []string{}
	if len(perms) > 0 {
		_ = decodePerms(perms, &v.Permissions)
	}
	return &v, nil
}

func decodePerms(raw []byte, dst *[]string) error {
	return json.Unmarshal(raw, dst)
}

func (s *Service) GetRole(ctx context.Context, id string) (*RoleView, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+roleDetailCols+` FROM roles r WHERE r.id = $1`, id)
	v, err := scanRole(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("role")
		}
		return nil, apierror.From(err)
	}
	return v, nil
}

func (s *Service) ListRolesDetail(ctx context.Context) ([]*RoleView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+roleDetailCols+` FROM roles r ORDER BY r.is_system DESC, r.name`)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []*RoleView{}
	for rows.Next() {
		v, err := scanRole(rows)
		if err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type UpsertRoleInput struct {
	Name        string
	Description string
	Permissions []string // full replacement list of permission codes
}

func validateRoleInput(in UpsertRoleInput) error {
	if len(in.Name) < 3 || len(in.Name) > 64 || !isPlainName(in.Name) {
		return apierror.BadRequest("Role name must be 3-64 characters using letters, digits, dash or underscore")
	}
	if len(in.Description) > 256 {
		return apierror.BadRequest("Description too long")
	}
	return nil
}

// CreateRole adds a custom role with its initial permission set.
func (s *Service) CreateRole(ctx context.Context, in UpsertRoleInput) (*RoleView, error) {
	if err := validateRoleInput(in); err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, apierror.From(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID string
	err = tx.QueryRow(ctx, `
		INSERT INTO roles (name, description) VALUES ($1, NULLIF($2,''))
		RETURNING id::text`, in.Name, in.Description).Scan(&newID)
	if err != nil {
		return nil, apierror.From(mapDuplicate(err))
	}
	if err := bindPermissions(ctx, tx, newID, in.Permissions); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.From(err)
	}
	return s.GetRole(ctx, newID)
}

// UpdateRole replaces name-independent metadata and permissions. System roles
// can never be edited — their behavior is part of the platform contract.
func (s *Service) UpdateRole(ctx context.Context, id string, in UpsertRoleInput) (*RoleView, error) {
	if err := validateRoleInput(in); err != nil {
		return nil, err
	}
	var isSystem bool
	if err := s.pool.QueryRow(ctx,
		`SELECT is_system FROM roles WHERE id = $1`, id).Scan(&isSystem); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("role")
		}
		return nil, apierror.From(err)
	}
	if isSystem {
		return nil, apierror.Conflict("System roles cannot be modified")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, apierror.From(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE roles SET description = NULLIF($2,'') WHERE id = $1`, id, in.Description)
	if err != nil {
		return nil, apierror.From(mapDuplicate(err))
	}
	_ = tag
	if _, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE role_id = $1`, id); err != nil {
		return nil, apierror.From(err)
	}
	if err := bindPermissions(ctx, tx, id, in.Permissions); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.From(err)
	}
	return s.GetRole(ctx, id)
}

// DeleteRole removes a non-system role; memberships cascade via FK.
func (s *Service) DeleteRole(ctx context.Context, id string) error {
	var isSystem bool
	err := s.pool.QueryRow(ctx, `SELECT is_system FROM roles WHERE id = $1`, id).Scan(&isSystem)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apierror.NotFound("role")
		}
		return apierror.From(err)
	}
	if isSystem {
		return apierror.Conflict("System roles cannot be deleted")
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM roles WHERE id = $1`, id)
	return apierror.From(err)
}

func bindPermissions(ctx context.Context, tx pgx.Tx, roleID string, codes []string) error {
	for _, code := range dedupe(codes) {
		tag, err := tx.Exec(ctx, `
			INSERT INTO role_permissions (role_id, permission_id)
			SELECT $1::uuid, p.id FROM permissions p WHERE p.code = $2
			ON CONFLICT DO NOTHING`, roleID, code)
		if err != nil {
			return apierror.From(err)
		}
		if tag.RowsAffected() == 0 {
			return apierror.BadRequest("Unknown permission code: "+code)
		}
	}
	return nil
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func isPlainName(s string) bool {
	for _, r := range s {
		ok := r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func mapDuplicate(err error) error {
	if err != nil && containsAny(err.Error(), []string{"duplicate key", "SQLSTATE 23505", "roles_name_key"}) {
		return apierror.Conflict("A role with that name already exists")
	}
	return err
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}



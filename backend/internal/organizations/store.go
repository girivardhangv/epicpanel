package organizations

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

var ErrSlugTaken = errors.New("slug already in use")

type Role string

const (
	RoleOwner     Role = "owner"
	RoleAdmin     Role = "admin"
	RoleReseller  Role = "reseller"
	RoleDeveloper Role = "developer"
	RoleBilling   Role = "billing"
	RoleSupport   Role = "support"
)

// RoleRank orders roles by privilege for permission checks.
// RBAC v2 mapping (permissions package): owner=Customer, admin=Admin,
// reseller=Reseller, support=Support; developer/billing are legacy staff
// roles preserved without dropping any existing grants.
var RoleRank = map[Role]int{
	RoleOwner:     5,
	RoleAdmin:     4,
	RoleReseller:  3,
	RoleDeveloper: 2,
	RoleBilling:   1,
	RoleSupport:   1,
}

type Organization struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedBy uuid.UUID `json:"created_by"`
}

type Member struct {
	UserID   uuid.UUID `json:"user_id"`
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Role     Role      `json:"role"`
	JoinedAt string    `json:"joined_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

var slugRe = regexp.MustCompile(`^([a-z0-9]{2}|[a-z0-9][a-z0-9-]{1,61}[a-z0-9])$`)

func NormalizeSlug(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Slugify converts a free-form name into a URL-safe slug.
func Slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_' || r == '.':
			b.WriteByte('-')
		}
	}
	slug := b.String()
	// Collapse consecutive separators, then trim leading/trailing dashes.
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	if len(slug) > 63 {
		slug = slug[:63]
	}
	return strings.Trim(slug, "-")
}

func ValidSlug(s string) bool { return slugRe.MatchString(s) }

func (s *Store) Create(ctx context.Context, name, slug string, creatorID uuid.UUID) (*Organization, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var org Organization
	err = tx.QueryRow(ctx, `
		INSERT INTO organizations (name, slug, created_by, package_id)
		VALUES ($1, $2, $3, (SELECT id FROM hosting_packages WHERE is_default = TRUE LIMIT 1))
		RETURNING id, name, slug, created_by
	`, name, slug, creatorID).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrSlugTaken
		}
		return nil, err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO organization_members (organization_id, user_id, role)
		VALUES ($1, $2, $3)
	`, org.ID, creatorID, RoleOwner)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &org, nil
}

func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*Organization, error) {
	var org Organization
	err := s.Pool.QueryRow(ctx, `SELECT id, name, slug, created_by FROM organizations WHERE id = $1`, id).
		Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &org, nil
}

func (s *Store) GetBySlug(ctx context.Context, slug string) (*Organization, error) {
	var org Organization
	err := s.Pool.QueryRow(ctx, `SELECT id, name, slug, created_by FROM organizations WHERE slug = $1`, slug).
		Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &org, nil
}

func (s *Store) ListForUser(ctx context.Context, userID uuid.UUID) ([]Organization, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT o.id, o.name, o.slug, o.created_by
		FROM organizations o
		JOIN organization_members m ON m.organization_id = o.id
		WHERE m.user_id = $1
		ORDER BY o.created_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Organization
	for rows.Next() {
		var org Organization
		if err := rows.Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, org)
	}
	return out, rows.Err()
}

// OrganizationsForUser returns just the org ids a user belongs to (WS event
// scoping).
func (s *Store) OrganizationsForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.Pool.Query(ctx, `SELECT organization_id FROM organization_members WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// NewStore builds a store on a pool (used by entrypoints wiring the bus).
func NewStore(pool *pgxpool.Pool) *Store { return &Store{Pool: pool} }

func (s *Store) List(ctx context.Context) ([]Organization, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, name, slug, created_by FROM organizations ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Organization
	for rows.Next() {
		var org Organization
		if err := rows.Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, org)
	}
	return out, rows.Err()
}

// RoleFor returns the member role of userID in orgID, or "" if not a member.
func (s *Store) RoleFor(ctx context.Context, orgID, userID uuid.UUID) (Role, error) {
	var role Role
	err := s.Pool.QueryRow(ctx, `
		SELECT role FROM organization_members WHERE organization_id = $1 AND user_id = $2
	`, orgID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

func (s *Store) Members(ctx context.Context, orgID uuid.UUID) ([]Member, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT m.user_id, u.email, u.name, m.role, m.created_at
		FROM organization_members m
		JOIN users u ON u.id = m.user_id
		WHERE m.organization_id = $1
		ORDER BY m.created_at ASC
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var joined time.Time
		if err := rows.Scan(&m.UserID, &m.Email, &m.Name, &m.Role, &joined); err != nil {
			return nil, err
		}
		m.JoinedAt = joined.UTC().Format(time.RFC3339)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMemberRole adds or updates a user's membership in the organization.
func (s *Store) SetMemberRole(ctx context.Context, orgID, userID uuid.UUID, role Role) error {
	tag, err := s.Pool.Exec(ctx, `
		INSERT INTO organization_members (organization_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id, user_id) DO UPDATE SET role = EXCLUDED.role
	`, orgID, userID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("member upsert affected no rows")
	}
	return nil
}

// RemoveMember removes a user from the organization.
func (s *Store) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `
		DELETE FROM organization_members WHERE organization_id = $1 AND user_id = $2
	`, orgID, userID)
	return err
}

// Update changes the organization's display name.
func (s *Store) Update(ctx context.Context, orgID uuid.UUID, name string) (*Organization, error) {
	var org Organization
	err := s.Pool.QueryRow(ctx, `
		UPDATE organizations SET name = $2, updated_at = now()
		WHERE id = $1
		RETURNING id, name, slug, created_by
	`, orgID, name).Scan(&org.ID, &org.Name, &org.Slug, &org.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &org, nil
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*UserRef, error) {
	var u UserRef
	err := s.Pool.QueryRow(ctx, `SELECT id, email FROM users WHERE email = $1`, email).
		Scan(&u.ID, &u.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) CountOwners(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM organization_members WHERE organization_id = $1 AND role = 'owner'
	`, orgID).Scan(&n)
	return n, err
}

func ValidRole(r Role) bool {
	_, ok := RoleRank[r]
	return ok
}

type UserRef struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

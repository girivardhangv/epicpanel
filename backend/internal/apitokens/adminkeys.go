package apitokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Platform admin API keys (epa_) are the machine counterpart of a
// platform-admin session: they operate across ALL organizations, subject to
// the same scope vocabulary (org scopes on /v1/organizations/... routes,
// admin:read/admin:write on the /v1/admin* surface). Unlike epk_ tokens they
// are not confined to an organization — that is the point — but they are
// still hashed at rest, expirable, revocable and last-used-tracked. Key
// creation/revocation is session-only so a leaked key cannot mint or revoke
// other keys.
//
// Security note (ADR-027 boundary preserved): org tokens never inherit
// platform-admin; this table is a separate principal type, not a flag on
// api_tokens.

const AdminKeyPrefix = "epa_"

var ErrAdminKeyNotFound = errors.New("admin api key not found")

// AdminKey is the stored (never-raw) representation of a platform key.
type AdminKey struct {
	ID         uuid.UUID  `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedBy  *uuid.UUID `json:"created_by,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// AdminValidScopes is the full scope set accepted at admin-key creation:
// every org scope plus the admin surface scopes.
var AdminValidScopes = append(append([]Scope{}, ValidScopes...), ScopeAdminRead, ScopeAdminWrite)

// ValidAdminScope reports whether s may be granted to a platform key.
func ValidAdminScope(s string) bool {
	for _, v := range AdminValidScopes {
		if string(v) == s {
			return true
		}
	}
	return false
}

const adminKeyCols = `id, name, scopes, created_by, last_used_at, expires_at, revoked_at, created_at`

func scanAdminKey(row pgx.Row) (*AdminKey, error) {
	var k AdminKey
	err := row.Scan(&k.ID, &k.Name, &k.Scopes, &k.CreatedBy, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt, &k.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// CreateAdminKey generates a raw epa_ key (returned once; only its hash is
// stored). The wildcard scope "*" expands to every valid scope, giving the
// convenient "full control" key without weakening resolution (the stored
// list stays explicit).
func (s *Store) CreateAdminKey(ctx context.Context, createdBy uuid.UUID, name string, scopes []string, expiresAt *time.Time) (*AdminKey, string, error) {
	if len(scopes) == 1 && scopes[0] == "*" {
		scopes = make([]string, 0, len(AdminValidScopes))
		for _, sc := range AdminValidScopes {
			scopes = append(scopes, string(sc))
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := AdminKeyPrefix + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])

	row := s.Pool.QueryRow(ctx, `
		INSERT INTO admin_api_keys (name, token_hash, created_by, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+adminKeyCols,
		name, hash, createdBy, scopes, expiresAt,
	)
	k, err := scanAdminKey(row)
	if err != nil {
		return nil, "", err
	}
	return k, token, nil
}

// resolveAdminKey authenticates a raw epa_ key and bumps last_used_at.
func (s *Store) resolveAdminKey(ctx context.Context, raw string) (*Resolved, error) {
	sum := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(sum[:])

	row := s.Pool.QueryRow(ctx, `
		UPDATE admin_api_keys SET last_used_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		RETURNING id, scopes, created_by
	`, hash)
	var id, createdBy uuid.UUID
	var scopes []string
	err := row.Scan(&id, &scopes, &createdBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAdminKeyNotFound
	}
	if err != nil {
		return nil, err
	}

	var email string
	err = s.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, createdBy).Scan(&email)
	if err != nil {
		return nil, err
	}

	set := make(map[string]bool, len(scopes))
	for _, sc := range scopes {
		set[sc] = true
	}
	return &Resolved{TokenID: id, UserID: createdBy, Email: email, IsAdmin: true, Platform: true, Scopes: set}, nil
}

// ListAdminKeys returns all platform keys (newest first), never raw.
func (s *Store) ListAdminKeys(ctx context.Context) ([]AdminKey, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+adminKeyCols+` FROM admin_api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminKey
	for rows.Next() {
		k, err := scanAdminKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// RevokeAdminKey revokes a platform key by id.
func (s *Store) RevokeAdminKey(ctx context.Context, keyID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE admin_api_keys SET revoked_at = now()
		WHERE id = $1 AND revoked_at IS NULL
	`, keyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrAdminKeyNotFound
	}
	return nil
}

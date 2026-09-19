package apitokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("api token not found")
)

// Scope is a permission unit for API tokens. Read scopes allow GET methods,
// write scopes allow mutating methods for their resource group.
type Scope string

const (
	ScopeOrgRead          Scope = "org:read"
	ScopeOrgWrite         Scope = "org:write"
	ScopeWebsitesRead     Scope = "websites:read"
	ScopeWebsitesWrite    Scope = "websites:write"
	ScopeServersRead      Scope = "servers:read"
	ScopeServersWrite     Scope = "servers:write"
	ScopeRuntimesRead     Scope = "runtimes:read"
	ScopeRuntimesWrite    Scope = "runtimes:write"
	ScopeDatabasesRead    Scope = "databases:read"
	ScopeDatabasesWrite   Scope = "databases:write"
	ScopeDomainsRead      Scope = "domains:read"
	ScopeDomainsWrite     Scope = "domains:write"
	ScopeDeploymentsRead  Scope = "deployments:read"
	ScopeDeploymentsWrite Scope = "deployments:write"
	ScopeBackupsRead      Scope = "backups:read"
	ScopeBackupsWrite     Scope = "backups:write"
	ScopeMonitoringRead   Scope = "monitoring:read"
	ScopeAlertsRead       Scope = "alerts:read"
	ScopeAlertsWrite      Scope = "alerts:write"
	ScopeAuditRead        Scope = "audit:read"
	ScopeBillingRead      Scope = "billing:read"
	ScopeBillingWrite     Scope = "billing:write"
)

// ValidScopes is the full set accepted at token creation.
var ValidScopes = []Scope{
	ScopeOrgRead, ScopeOrgWrite,
	ScopeWebsitesRead, ScopeWebsitesWrite,
	ScopeServersRead, ScopeServersWrite,
	ScopeRuntimesRead, ScopeRuntimesWrite,
	ScopeDatabasesRead, ScopeDatabasesWrite,
	ScopeDomainsRead, ScopeDomainsWrite,
	ScopeDeploymentsRead, ScopeDeploymentsWrite,
	ScopeBackupsRead, ScopeBackupsWrite,
	ScopeMonitoringRead,
	ScopeAlertsRead, ScopeAlertsWrite,
	ScopeAuditRead,
	ScopeBillingRead, ScopeBillingWrite,
}

// Admin-only scopes for platform admin keys (epa_). Deliberately NOT in
// ValidScopes: org tokens (epk_) can never hold them, which is what keeps
// the admin surface unreachable for org tokens by construction.
const (
	ScopeAdminRead  Scope = "admin:read"
	ScopeAdminWrite Scope = "admin:write"
)

func ValidScope(s string) bool {
	for _, v := range ValidScopes {
		if string(v) == s {
			return true
		}
	}
	return false
}

const TokenPrefix = "epk_"

// Kind distinguishes human-created tokens from service-account tokens.
type Kind string

const (
	KindUser    Kind = "user"
	KindService Kind = "service"
)

type Token struct {
	ID           uuid.UUID  `json:"id"`
	Organization uuid.UUID  `json:"organization_id"`
	Name         string     `json:"name"`
	Scopes       []string   `json:"scopes"`
	LastUsedAt   *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Resolved is the authenticated token identity used by middleware.
type Resolved struct {
	TokenID uuid.UUID
	OrgID   uuid.UUID
	UserID  uuid.UUID
	Email   string
	IsAdmin bool
	// Platform marks a platform admin key (epa_): acts as the platform
	// admin across all organizations. Org tokens (epk_) are always false.
	Platform bool
	Scopes   map[string]bool
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, organization_id, name, scopes, last_used_at, expires_at, revoked_at, created_at`

func scanRow(row pgx.Row) (*Token, error) {
	var t Token
	err := row.Scan(&t.ID, &t.Organization, &t.Name, &t.Scopes, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Create generates a raw token (returned once; only its hash is stored).
func (s *Store) Create(ctx context.Context, orgID, createdBy uuid.UUID, name string, scopes []string, expiresAt *time.Time) (*Token, string, error) {
	return s.CreateKind(ctx, orgID, createdBy, name, scopes, expiresAt, KindUser, uuid.Nil)
}

// CreateKind is the full form: kind=user tokens are created by humans,
// kind=service tokens back a service account (service_account_id bound).
func (s *Store) CreateKind(ctx context.Context, orgID, createdBy uuid.UUID, name string, scopes []string, expiresAt *time.Time, kind Kind, serviceAccountID uuid.UUID) (*Token, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	token := TokenPrefix + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])

	var saID *uuid.UUID
	if serviceAccountID != uuid.Nil {
		saID = &serviceAccountID
	}

	row := s.Pool.QueryRow(ctx, `
		INSERT INTO api_tokens (organization_id, created_by, name, token_hash, scopes, expires_at, kind, service_account_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+cols,
		orgID, createdBy, name, hash, scopes, expiresAt, kind, saID,
	)
	t, err := scanRow(row)
	if err != nil {
		return nil, "", err
	}
	return t, token, nil
}

// Resolve authenticates a raw token and updates last_used_at. The prefix
// selects the principal type: epa_ = platform admin key, anything else =
// org-confined epk_ token.
func (s *Store) Resolve(ctx context.Context, raw string) (*Resolved, error) {
	if strings.HasPrefix(raw, AdminKeyPrefix) {
		return s.resolveAdminKey(ctx, raw)
	}

	sum := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(sum[:])

	row := s.Pool.QueryRow(ctx, `
		UPDATE api_tokens SET last_used_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		RETURNING id, organization_id, scopes, created_by
	`, hash)
	var id, orgID, userID uuid.UUID
	var scopes []string
	err := row.Scan(&id, &orgID, &scopes, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var email string
	err = s.Pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email)
	if err != nil {
		return nil, err
	}

	set := make(map[string]bool, len(scopes))
	for _, sc := range scopes {
		set[sc] = true
	}
	// Tokens are org-confined automation identities: they never inherit the
	// creator's platform-admin (RBAC v2; closes the cross-org master-key hole).
	return &Resolved{TokenID: id, OrgID: orgID, UserID: userID, Email: email, IsAdmin: false, Scopes: set}, nil
}

func (s *Store) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]Token, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM api_tokens WHERE organization_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) Revoke(ctx context.Context, orgID, tokenID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE api_tokens SET revoked_at = now()
		WHERE id = $1 AND organization_id = $2 AND revoked_at IS NULL
	`, tokenID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

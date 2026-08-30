// Registration tokens replace the static shared key: they expire, are
// single-use, revocable, and their plaintext is shown exactly once at
// creation. Only SHA-256 digests are stored.
package servers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/jackc/pgx/v5"
)

const (
	// DefaultRegistrationTokenTTL balances convenience against exposure.
	DefaultRegistrationTokenTTL = 24 * time.Hour
	MaxRegistrationTokenTTL     = 7 * 24 * time.Hour
	// RegistrationTokenPrefix makes tokens identifiable in logs/shell history
	// without being valid on their own.
	RegistrationTokenPrefix = "epreg-"
)

type RegistrationToken struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	CreatedBy *string    `json:"created_by"`
	ExpiresAt string     `json:"expires_at"`
	UsedAt    *string    `json:"used_at"`
	UsedBy    *string    `json:"used_by_server"`
	RevokedAt *string    `json:"revoked_at"`
	CreatedAt string     `json:"created_at"`
}

// CreateRegistrationToken mints a token and returns the plaintext exactly once.
func (s *Service) CreateRegistrationToken(ctx context.Context, createdBy *string, label string, ttl time.Duration) (*RegistrationToken, string, error) {
	if ttl <= 0 {
		ttl = DefaultRegistrationTokenTTL
	}
	if ttl > MaxRegistrationTokenTTL {
		return nil, "", apierror.BadRequest("token lifetime may not exceed 7 days")
	}
	if len(label) > 128 {
		return nil, "", apierror.BadRequest("label too long")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", apierror.From(err)
	}
	plaintext := RegistrationTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(plaintext))

	var rt RegistrationToken
	err := s.pool.QueryRow(ctx, `
		INSERT INTO server_registration_tokens (token_hash, label, created_by, expires_at)
		VALUES ($1, NULLIF($2,''), $3, $4)
		RETURNING id, COALESCE(label,''), created_by, expires_at::text, used_at::text,
		          used_by_server::text, revoked_at::text, created_at::text`,
		hex.EncodeToString(sum[:]), label, createdBy, time.Now().Add(ttl)).
		Scan(&rt.ID, &rt.Label, &rt.CreatedBy, &rt.ExpiresAt, &rt.UsedAt,
			&rt.UsedBy, &rt.RevokedAt, &rt.CreatedAt)
	if err != nil {
		return nil, "", apierror.From(err)
	}
	return &rt, plaintext, nil
}

// ListRegistrationTokens returns tokens, newest first. Hashes never leave.
func (s *Service) ListRegistrationTokens(ctx context.Context) ([]RegistrationToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(label,''), created_by, expires_at::text, used_at::text,
		       used_by_server::text, revoked_at::text, created_at::text
		FROM server_registration_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []RegistrationToken{}
	for rows.Next() {
		var t RegistrationToken
		if err := rows.Scan(&t.ID, &t.Label, &t.CreatedBy, &t.ExpiresAt, &t.UsedAt,
			&t.UsedBy, &t.RevokedAt, &t.CreatedAt); err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeRegistrationToken prevents any future use of an unused token.
func (s *Service) RevokeRegistrationToken(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE server_registration_tokens SET revoked_at = now()
		WHERE id = $1 AND used_at IS NULL AND revoked_at IS NULL`, id)
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return apierror.Conflict("token not found, already used or already revoked")
	}
	return nil
}

// ConsumeRegistrationToken atomically marks a valid token as used and returns
// its row. Expired, revoked and used tokens are rejected with the same error
// so timing and messages leak nothing about which check failed.
func (s *Service) ConsumeRegistrationToken(ctx context.Context, plaintext string) error {
	sum := sha256.Sum256([]byte(plaintext))
	tag, err := s.pool.Exec(ctx, `
		UPDATE server_registration_tokens
		SET used_at = now()
		WHERE token_hash = $1
		  AND used_at IS NULL AND revoked_at IS NULL AND expires_at > now()`,
		hex.EncodeToString(sum[:]))
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return apierror.New(401, "AGENT_KEY_INVALID", "registration token invalid")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Ops channel (panel -> agent)
// ---------------------------------------------------------------------------

// OpsTarget carries everything the panel needs to reach and authenticate
// against an agent's management endpoint.
type OpsTarget struct {
	ID         string `json:"id"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Status     string `json:"status"`
	AgentURL   string `json:"agent_url"`
	OpsToken   string `json:"-"`
	Manageable bool   `json:"manageable"`
}

// OpsTarget resolves the agent management channel for a server.
func (s *Service) OpsTarget(ctx context.Context, id string) (*OpsTarget, error) {
	var t OpsTarget
	err := s.pool.QueryRow(ctx, `
		SELECT id, hostname, os, status, COALESCE(agent_url,''), COALESCE(ops_token,'')
		FROM servers WHERE id = $1`, id).
		Scan(&t.ID, &t.Hostname, &t.OS, &t.Status, &t.AgentURL, &t.OpsToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("server")
		}
		return nil, apierror.From(err)
	}
	t.Manageable = t.AgentURL != "" && t.OpsToken != "" && t.Status != "revoked"
	return &t, nil
}

// SaveCapabilities persists the last probed feature matrix.
func (s *Service) SaveCapabilities(ctx context.Context, id string, caps map[string]any) error {
	raw, err := jsonMarshal(caps)
	if err != nil {
		return apierror.From(err)
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE servers SET capabilities = $2::jsonb WHERE id = $1`, id, string(raw))
	if err != nil {
		return apierror.From(err)
	}
	return nil
}

// Capabilities returns the stored probe results (never fabricated: an empty
// map means the server has not been probed yet).
func (s *Service) Capabilities(ctx context.Context, id string) (map[string]any, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT capabilities FROM servers WHERE id = $1`, id).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("server")
		}
		return nil, apierror.From(err)
	}
	out := map[string]any{}
	if len(raw) > 0 {
		_ = jsonUnmarshal(raw, &out)
	}
	return out, nil
}

// SetAgentURL records the agent's advertised management endpoint.
func (s *Service) SetAgentURL(ctx context.Context, id, agentURL string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE servers SET agent_url = NULLIF($2,'') WHERE id = $1`, id, agentURL)
	if err != nil {
		return apierror.From(err)
	}
	return nil
}

// IDForAgentToken resolves the server id for a bearer agent token (used by
// telemetry ingestion; revocation is enforced by the status check).
func (s *Service) IDForAgentToken(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", apierror.Unauthorized
	}
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM servers
		WHERE agent_token_hash = $1 AND status <> 'revoked'`,
		hashAgentToken(token)).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apierror.Unauthorized
		}
		return "", apierror.From(err)
	}
	return id, nil
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

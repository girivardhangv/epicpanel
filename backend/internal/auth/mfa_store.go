package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrChallengeInvalid = errors.New("mfa challenge is invalid or expired")

// MFAStore backs the 2FA foundation: pending login challenges, encrypted TOTP
// secrets on users, and hashed single-use recovery codes.
type MFAStore struct {
	Pool *pgxpool.Pool
}

func newRandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// BeginChallenge issues a short-lived pending-login token for an MFA-enabled
// user (no session exists until the code verifies).
func (s *MFAStore) BeginChallenge(ctx context.Context, userID uuid.UUID, ttl time.Duration) (string, error) {
	raw, err := newRandomToken()
	if err != nil {
		return "", err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO mfa_challenges (token_hash, user_id, expires_at) VALUES ($1, $2, $3)
	`, hashToken(raw), userID, time.Now().Add(ttl))
	if err != nil {
		return "", err
	}
	return raw, nil
}

// ConsumeChallenge validates a pending-login token exactly once and returns
// its user.
func (s *MFAStore) ConsumeChallenge(ctx context.Context, raw string) (uuid.UUID, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE mfa_challenges SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id
	`, hashToken(raw))
	var id uuid.UUID
	err := row.Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrChallengeInvalid
	}
	return id, err
}

// SetSecret stores the (secretbox-encrypted) TOTP secret without enabling
// MFA. Ciphertext is base64-encoded for TEXT storage.
func (s *MFAStore) SetSecret(ctx context.Context, userID uuid.UUID, encrypted []byte) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET totp_secret_encrypted = $2, updated_at = now() WHERE id = $1`,
		userID, base64.StdEncoding.EncodeToString(encrypted))
	return err
}

// SecretEncrypted returns the stored ciphertext (base64 of the AES-GCM blob).
func (s *MFAStore) SecretEncrypted(ctx context.Context, userID uuid.UUID) ([]byte, error) {
	var encB64 string
	err := s.Pool.QueryRow(ctx, `SELECT totp_secret_encrypted FROM users WHERE id = $1`, userID).Scan(&encB64)
	if err != nil {
		return nil, err
	}
	if encB64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(encB64)
}

// Enable marks MFA active for the user.
func (s *MFAStore) Enable(ctx context.Context, userID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET mfa_enabled = TRUE, updated_at = now() WHERE id = $1`, userID)
	return err
}

// Disable clears MFA state entirely (secret, codes, flag).
func (s *MFAStore) Disable(ctx context.Context, userID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET mfa_enabled = FALSE, totp_secret_encrypted = '', updated_at = now() WHERE id = $1`, userID)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, userID)
	return err
}

// MFAEnabled reports whether the user has 2FA active.
func (s *MFAStore) MFAEnabled(ctx context.Context, userID uuid.UUID) (bool, error) {
	var on bool
	err := s.Pool.QueryRow(ctx, `SELECT mfa_enabled FROM users WHERE id = $1`, userID).Scan(&on)
	return on, err
}

// ReplaceRecoveryCodes swaps the user's recovery codes (called on enable).
func (s *MFAStore) ReplaceRecoveryCodes(ctx context.Context, userID uuid.UUID, hashes []string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_codes (user_id, code_hash) VALUES ($1, $2)`, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ConsumeRecoveryCode marks a matching unused code used; reports success.
func (s *MFAStore) ConsumeRecoveryCode(ctx context.Context, userID uuid.UUID, code string) (bool, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE recovery_codes SET used_at = now()
		WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL
	`, userID, hashToken(strings.ToUpper(strings.TrimSpace(code))))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// Password reset lifecycle.
//
// Request creates a single-use, short-lived token whose SHA-256 digest is the
// only thing persisted. Delivery channels (SMTP etc.) arrive in a later phase;
// in development mode the panel logs the token to its console so the flow can
// be exercised end-to-end without pretending e-mail works.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/jackc/pgx/v5"
)

const resetTokenTTL = "60 minutes"

func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// StartPasswordReset issues a reset token for identifier if it exists and the
// account is active. The HTTP response is identical regardless of existence
// (account-enumeration protection); the audit trail records the truth.
func (s *Service) StartPasswordReset(ctx context.Context, identifier, ip string) {
	rec := s.findUser(ctx, identifier)

	if rec == nil || !rec.isActive {
		s.auditStart(ctx, strings.TrimSpace(identifier), ip, "unknown_account")
		return
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return // silently fail: response must stay uniform
	}
	token := base64.RawURLEncoding.EncodeToString(b)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_hash, expires_at, requested_ip)
		VALUES ($1, $2, now() + $3::interval, $4)`,
		rec.id, hashResetToken(token), resetTokenTTL, ip)
	if err != nil {
		return
	}

	s.auditStart(ctx, rec.username, ip, "token_issued")

	if s.defaults.Development && s.debugSink != nil {
		s.debugSink("DEV password reset token for %q: %s", rec.username, token)
	}
}

func (s *Service) auditStart(ctx context.Context, label, ip, result string) {
	s.log.Log(ctx, audit.Entry{
		ActorType: "system", Label: label,
		Action:   audit.ActionPasswordResetRequested,
		Metadata: map[string]any{"result": result},
		IP:       ip,
	})
}

// CompletePasswordReset consumes a valid unused token, applies password policy,
// re-hashes and revokes every session belonging to that account.
func (s *Service) CompletePasswordReset(ctx context.Context, rawToken, newPassword, confirm, ip string) error {
	if confirm != newPassword {
		return apierror.BadRequest("Passwords do not match")
	}
	minLen := s.intSetting(settings.KeyPasswordMinLength, s.defaults.PasswordMinLen)
	classes := s.intSetting(settings.KeyPasswordRequireClasses, s.defaults.PasswordClasses)
	if problems := ValidatePolicy(newPassword, minLen, classes); len(problems) > 0 {
		return apierror.New(422, "WEAK_PASSWORD",
			"Password requirements not met: "+strings.Join(problems, "; "))
	}

	var userID, username string
	err := s.pool.QueryRow(ctx, `
		SELECT u.id::text, u.username
		FROM password_reset_tokens t JOIN users u ON u.id = t.user_id AND u.is_active
		WHERE t.token_hash = $1 AND t.used_at IS NULL AND t.expires_at > now()`,
		hashResetToken(rawToken)).Scan(&userID, &username)
	if err != nil {
		return apierror.New(400, "RESET_TOKEN_INVALID", "This reset link is invalid or has expired")
	}

	newHash, err := Hash(newPassword)
	if err != nil {
		return apierror.From(err)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return apierror.From(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE users SET password_hash = $2, failed_login_count = 0, locked_until = NULL,
		        updated_at = now() WHERE id = $1`, userID, newHash); err != nil {
		return apierror.From(err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET used_at = now()
		 WHERE token_hash = $1 AND used_at IS NULL`, hashResetToken(rawToken))
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return apierror.New(400, "RESET_TOKEN_INVALID", "This reset link is invalid or has expired")
	}
	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET revoked_at = now(), revoked_reason = 'password_reset'
		 WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return apierror.From(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return apierror.From(err)
	}

	s.log.Log(ctx, audit.Entry{
		ActorType: "user", Label: username,
		Action:   audit.ActionPasswordChanged,
		Metadata: map[string]any{"via": "reset_token"},
		IP:       ip,
	})
	return nil
}

var _ = time.Now // reserved for future rate-window bookkeeping

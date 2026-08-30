// Authentication service: login with brute-force protection, password
// changes, and session lifecycle. Business rules live here, not in handlers.
package auth

import (
	"context"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/rbac"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Deps struct {
	Pool     *pgxpool.Pool
	Sessions *Store
	Perms    *rbac.Service
	Audit    *audit.Service
	Settings *settings.Service

	MaxFailedLogins  int           // default account lockout threshold
	Lockout          time.Duration // default lockout duration
	SessionLifetime  time.Duration // default session lifetime
	PasswordMinLen   int           // installer may override via settings
	PasswordClasses  int
	Development      bool // enables dev conveniences (reset token console output)
}

type Service struct {
	pool        *pgxpool.Pool
	sessions    *Store
	perms       *rbac.Service
	log         Logger
	settingsSvc *settings.Service
	defaults    Deps

	// debugSink receives development-only diagnostics (e.g. reset tokens).
	debugSink func(format string, args ...any)

	// dummyDigest is an Argon2id hash of an unrelated constant secret. Verifying
	// against it when a username does not exist equalises CPU cost so response
	// timing cannot be used to enumerate accounts.
	dummyDigest string
}

// Logger avoids a hard dependency on concrete audit type in this layer.
type Logger interface {
	Log(ctx context.Context, e audit.Entry)
}

func NewService(d Deps, debugSink func(string, ...any)) *Service {
	s := &Service{
		pool:        d.Pool,
		sessions:    d.Sessions,
		perms:       d.Perms,
		log:         d.Audit,
		settingsSvc: d.Settings,
		defaults:    d,
		debugSink:   debugSink,
	}
	if h, err := Hash("epicpanel-timing-equaliser"); err == nil {
		s.dummyDigest = h
	}
	return s
}

// LoginResult carries everything the handler needs to finalize a login.
type LoginResult struct {
	Token       string
	Identity    *Identity
	LifetimeSec int
}

func (s *Service) Login(ctx context.Context, identifier, plainPassword, ip, userAgent string) (*LoginResult, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" || plainPassword == "" || len(identifier) > 254 || len(plainPassword) > 1024 {
		return nil, apierror.InvalidCredentials
	}
	now := time.Now().UTC()

	rec := s.findUser(ctx, identifier)

	if rec != nil && now.Before(rec.lockedUntil) {
		s.auditFailure(ctx, rec.id, rec.username, map[string]any{"reason": "account_locked"}, ip, userAgent)
		return nil, apierror.AccountLocked
	}

	if rec == nil {
		s.burnCPU(plainPassword)
		s.auditFailure(ctx, "", identifier, map[string]any{"reason": "no_such_user"}, ip, userAgent)
		return nil, apierror.InvalidCredentials
	}

	ok, needsUpgrade, err := Verify(rec.passwordHash, plainPassword)
	if err != nil {
		return nil, apierror.New(500, "INTERNAL_ERROR", "Password verification failed")
	}
	if !ok {
		fails := rec.failedCount + 1
		maxFails := s.intSetting(settings.KeyMaxFailedLogins, s.defaults.MaxFailedLogins)
		lockMin := s.intSetting(settings.KeyAccountLockoutMinutes, int(s.defaults.Lockout.Minutes()))
		if maxFails > 0 && fails >= maxFails && lockMin > 0 {
			until := now.Add(time.Duration(lockMin) * time.Minute)
			_, _ = s.pool.Exec(ctx,
				`UPDATE users SET failed_login_count = $2, locked_until = $3 WHERE id = $1`,
				rec.id, fails, until)
			s.auditFailure(ctx, rec.id, rec.username,
				map[string]any{"reason": "account_locked", "failed_count": fails}, ip, userAgent)
			return nil, apierror.AccountLocked
		}
		_, _ = s.pool.Exec(ctx, `UPDATE users SET failed_login_count = $2 WHERE id = $1`, rec.id, fails)
		s.auditFailure(ctx, rec.id, rec.username,
			map[string]any{"reason": "bad_password", "failed_count": fails}, ip, userAgent)
		return nil, apierror.InvalidCredentials
	}
	if !rec.isActive {
		s.auditFailure(ctx, rec.id, rec.username, map[string]any{"reason": "inactive_account"}, ip, userAgent)
		return nil, apierror.InvalidCredentials
	}
	if needsUpgrade {
		// Transparently re-hash with current Argon2id parameters.
		if h, herr := Hash(plainPassword); herr == nil {
			_, _ = s.pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, rec.id, h)
		}
	}

	_, _ = s.pool.Exec(ctx,
		`UPDATE users SET failed_login_count = 0, locked_until = NULL, last_login_at = now() WHERE id = $1`, rec.id)

	perms, err := s.perms.PermissionsForUser(ctx, rec.id)
	if err != nil {
		return nil, apierror.From(err)
	}

	lifetimeMin := s.intSetting(settings.KeySessionLifetimeMinutes, int(s.defaults.SessionLifetime.Minutes()))
	created, rawToken, err := s.sessions.CreateTTL(ctx, rec.id, ip, userAgent,
		time.Duration(lifetimeMin)*time.Minute)
	if err != nil {
		return nil, apierror.From(err)
	}

	id := &Identity{UserID: rec.id, Username: rec.username, DisplayName: rec.displayName,
		Email: emailPtr(rec.email), Permissions: perms}
	id.SessionID = created.SessionID
	id.CSRFToken = created.CSRFToken

	actorID := mustUUID(rec.id)
	s.log.Log(ctx, audit.Entry{
		ActorType: "user", ActorID: actorID, Label: rec.username,
		Action: audit.ActionLoginSuccess, IP: ip, UserAgent: userAgent,
		Metadata: map[string]any{"session_id": id.SessionID},
	})

	return &LoginResult{Token: rawToken, Identity: id, LifetimeSec: lifetimeMin * 60}, nil
}

// ChangePassword verifies the current password, enforces policy, re-hashes and
// revokes every other session belonging to the account.
func (s *Service) ChangePassword(ctx context.Context, sess *Identity, current, next, confirm string) error {
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, sess.UserID).Scan(&hash)
	if err != nil {
		return apierror.From(err)
	}
	ok, _, verr := Verify(hash, current)
	if verr != nil || !ok {
		actorID := mustUUID(sess.UserID)
		s.log.Log(ctx, audit.Entry{
			ActorType: "user", ActorID: actorID, Label: sess.Username,
			Action:   audit.ActionPasswordChanged,
			Metadata: map[string]any{"result": "current_password_rejected"},
		})
		return apierror.BadRequest("Current password is incorrect")
	}
	if confirm != next {
		return apierror.BadRequest("Passwords do not match")
	}

	minLen := s.intSetting(settings.KeyPasswordMinLength, s.defaults.PasswordMinLen)
	classes := s.intSetting(settings.KeyPasswordRequireClasses, s.defaults.PasswordClasses)
	if problems := ValidatePolicy(next, minLen, classes); len(problems) > 0 {
		return apierror.New(422, "WEAK_PASSWORD", "Password requirements not met: "+strings.Join(problems, "; "))
	}

	newHash, err := Hash(next)
	if err != nil {
		return apierror.From(err)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE users SET password_hash = $2, failed_login_count = 0, locked_until = NULL, updated_at = now()
		WHERE id = $1`, sess.UserID, newHash)
	if err != nil {
		return apierror.From(err)
	}

	uid := mustUUID(sess.UserID)
	if uid != nil && sess.SessionID != "" {
		_ = s.sessions.RevokeAllForUser(ctx, *uid, sess.SessionID, "password_changed")
	}
	s.log.Log(ctx, audit.Entry{
		ActorType: "user", ActorID: mustUUID(sess.UserID), Label: sess.Username,
		Action: audit.ActionPasswordChanged,
	})
	return nil
}

// Logout revokes the caller's active session.
func (s *Service) Logout(ctx context.Context, rawToken, ip, ua string, sess *Identity) {
	_ = s.sessions.Revoke(ctx, rawToken, "user_logout")
	var actorID *uuid.UUID
	label := "unknown"
	if sess != nil {
		actorID = mustUUID(sess.UserID)
		label = sess.Username
	}
	s.log.Log(ctx, audit.Entry{
		ActorType: "user", ActorID: actorID, Label: label,
		Action: audit.ActionLogout, IP: ip, UserAgent: ua,
	})
}

// --- internal ---------------------------------------------------------------

func (s *Service) burnCPU(password string) {
	if s.dummyDigest != "" {
		_, _, _ = Verify(s.dummyDigest, password)
	}
}

func (s *Service) auditFailure(ctx context.Context, actorIDStr, label string, meta map[string]any, ip, ua string) {
	s.log.Log(ctx, audit.Entry{
		ActorType: "user", ActorID: mustUUID(actorIDStr), Label: label,
		Action: audit.ActionLoginFailure, IP: ip, UserAgent: ua, Metadata: meta,
	})
}

type userRow struct {
	id           string
	username     string
	displayName  string
	email        *string
	passwordHash string
	isActive     bool
	failedCount  int
	lockedUntil  time.Time
}

func (s *Service) findUser(ctx context.Context, identifier string) *userRow {
	row := s.pool.QueryRow(ctx, `
		SELECT id, username, COALESCE(display_name,''), email::text, password_hash, is_active,
		       failed_login_count, COALESCE(locked_until, '1970-01-01'::timestamptz)
		FROM users
		WHERE lower(username) = lower($1) OR (email IS NOT NULL AND lower(email) = lower($1))`,
		strings.TrimSpace(identifier))
	u := &userRow{}
	err := row.Scan(&u.id, &u.username, &u.displayName, &u.email, &u.passwordHash,
		&u.isActive, &u.failedCount, &u.lockedUntil)
	if err != nil {
		return nil // uniform failure prevents enumeration
	}
	return u
}

func (s *Service) intSetting(key string, def int) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.settingsSvc.Int(ctx, key, def, 1, 1000000)
}

func mustUUID(id string) *uuid.UUID {
	if id == "" {
		return nil
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return nil
	}
	return &u
}

func emailPtr(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	c := *p
	return &c
}

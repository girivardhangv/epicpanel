// Support types bridging generic service interfaces to the concrete
// repositories in this binary. Kept next to main where dependencies meet.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/rbac"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func trimSpace(s string) string { return strings.TrimSpace(s) }

func displayNameOrPlaceholder(name string) string {
	if name == "" {
		return ""
	}
	return name
}

func validateUserInput(username, email string) error {
	if err := usersValidateUsername(username); err != nil {
		return err
	}
	if email != "" && !usersValidEmail(email) {
		return errors.New("invalid email address")
	}
	return nil
}

// local, dependency-light validation replicating users package rules
func usersValidateUsername(s string) error {
	if len(s) < 3 || len(s) > 64 {
		return errors.New("username must be between 3 and 64 characters")
	}
	for _, r := range s {
		ok := r == '.' || r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return errors.New("username may only contain letters, digits, '.', '-' and '_'")
		}
	}
	return nil
}

func usersValidEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	return !strings.ContainsAny(s, " \t\r\n")
}

func isDuplicate(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 23505") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "users_username_lower_key") ||
		strings.Contains(msg, "users_email_lower_key")
}

func parseUUID(id string) *uuid.UUID {
	u, err := uuid.Parse(id)
	if err != nil {
		return nil
	}
	return &u
}

func hashSum(data []byte) [32]byte { return sha256.Sum256(data) }

func pingDatabase(ctx context.Context, pool *pgxpool.Pool) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var one int
	if err := pool.QueryRow(cctx, `SELECT 1`).Scan(&one); err != nil {
		return fmt.Errorf("database unreachable")
	}
	return nil
}

// fingerprintProvider returns the installation fingerprint derived from the
// persistent instance id. Result is cached; it never changes.
type fingerprintProvider struct {
	pool    *pgxpool.Pool
	log     *slog.Logger
	mu      sync.Mutex
	value   string
}

func newFingerprintProvider(pool *pgxpool.Pool, log *slog.Logger) func() string {
	p := &fingerprintProvider{pool: pool, log: log}
	return p.get
}

func (p *fingerprintProvider) get() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.value != "" {
		return p.value
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var instanceID string
	if err := p.pool.QueryRow(ctx, `SELECT instance_id::text FROM installations WHERE id = 1`).Scan(&instanceID); err != nil {
		p.log.Error("cannot read installation row", "err", err.Error())
		return ""
	}
	// Cache the hashed form so every caller sees one stable fingerprint.
	p.value = "ep-" + sha256Short(instanceID)
	return p.value
}

func sha256Short(id string) string {
	sum := hashSum([]byte("epicpanel:" + id))
	return fmt.Sprintf("%x", sum[:12])
}

// adminProvisioner implements the installer.UserCreator contract with proper
// server-side policy enforcement and RBAC role assignment.
type adminProvisioner struct {
	pool     *pgxpool.Pool
	perms    *rbac.Service
	auditSvc *audit.Service
	settings *settings.Service
}

func (a *adminProvisioner) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := a.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (a *adminProvisioner) CreateAdministrator(ctx context.Context,
	username, email, displayName, password, confirm string) error {

	username = trimSpace(username)
	displayName = trimSpace(displayName)
	email = trimSpace(email)

	if err := validateUserInput(username, email); err != nil {
		return apierror.BadRequest(err.Error())
	}
	if displayName != "" && len(displayName) > 128 {
		return apierror.BadRequest("display name too long")
	}
	if password != confirm {
		return apierror.BadRequest("Passwords do not match")
	}

	minLen := a.settings.Int(ctx, settings.KeyPasswordMinLength, 12, 10, 128)
	classes := a.settings.Int(ctx, settings.KeyPasswordRequireClasses, 3, 3, 4)
	if problems := auth.ValidatePolicy(password, minLen, classes); len(problems) > 0 {
		msg := ""
		for i, pr := range problems {
			if i > 0 {
				msg += "; "
			}
			msg += pr
		}
		return apierror.New(422, "WEAK_PASSWORD", "Password requirements not met: "+msg)
	}

	hash, err := auth.Hash(password)
	if err != nil {
		return apierror.From(err)
	}

	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return apierror.From(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID string
	insertErr := tx.QueryRow(ctx, `
		INSERT INTO users (username, email, display_name, password_hash)
		VALUES ($1, NULLIF(lower($2),''), $3, $4)
		RETURNING id`,
		username, email, displayNameOrPlaceholder(displayName), hash).Scan(&userID)
	if insertErr != nil {
		if isDuplicate(insertErr) {
			return apierror.Conflict("Username or email already exists")
		}
		return apierror.From(insertErr)
	}

	if err := assignSuperAdmin(ctx, tx, userID); err != nil {
		return apierror.From(err)
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return apierror.From(commitErr)
	}

	uid := parseUUID(userID)
	a.auditSvc.Log(ctx, audit.Entry{
		ActorType: "installer", Action: audit.ActionRoleAssigned,
		Resource: "role", ResourceID: "super_admin",
		Metadata: map[string]any{"user_id": userID},
	})
	_ = uid
	return nil
}

func assignSuperAdmin(ctx context.Context, tx pgx.Tx, userID string) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1::uuid, r.id FROM roles r WHERE r.name = 'super_admin' AND r.is_system
		ON CONFLICT DO NOTHING`, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("super_admin system role missing from database")
	}
	return nil
}

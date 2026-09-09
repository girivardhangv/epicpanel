package main

// reset-password — operator password recovery for the panel.
//
//	epicpanel-api reset-password --email admin@example.com --password 'NewPass'
//
// Runs locally on the box (root), so it never touches the HTTP surface. Per
// the Phase 12 rules: password change ⇒ revoke all sessions; and because a
// lost password usually means a lost TOTP device too, MFA is disabled on the
// account (re-enrollable from the UI). The action is written to audit_logs.

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/config"
	"github.com/epicbyte/epicpanel/backend/internal/db"
	"github.com/epicbyte/epicpanel/backend/internal/users"
)

func resetPasswordCmd(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	email := fs.String("email", "", "account email (required)")
	password := fs.String("password", "", "new password (required)")
	mfa := fs.Bool("keep-mfa", false, "keep MFA enabled (default: disable it)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || *password == "" {
		return fmt.Errorf("both --email and --password are required")
	}
	if len(*password) < 10 {
		return fmt.Errorf("password must be at least 10 characters")
	}

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	us := &users.Store{Pool: pool}
	u, err := us.GetByEmail(ctx, *email)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("no user with email %q — if setup never completed, run: epicpanel-api setup-token", *email)
		}
		return err
	}
	uid := u.ID

	hash, err := auth.HashPassword(*password)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, uid, hash); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	if !*mfa {
		if _, err := pool.Exec(ctx,
			`UPDATE users SET mfa_enabled = FALSE, totp_secret_encrypted = '', updated_at = now() WHERE id = $1`, uid); err != nil {
			return fmt.Errorf("disable mfa: %w", err)
		}
	}

	// Password change ⇒ every session for this user dies (Phase 12).
	tag, err := pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, uid)
	if err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}

	// Audit trail (system actor).
	aid := audit.ActorSystem
	_ = aid
	(&audit.Store{Pool: pool}).RecordBestEffort(ctx, audit.Entry{
		ActorType:  audit.ActorSystem,
		Action:     "user.password_reset",
		Result:     "success",
		ResourceType: "user",
		ResourceID:   uid.String(),
	})

	fmt.Printf("password reset for %s\n", *email)
	if !*mfa {
		fmt.Println("MFA disabled (re-enable from Security page)")
	}
	fmt.Printf("sessions revoked: %d\n", tag.RowsAffected())
	fmt.Println("log in at your panel URL with the new password")
	return nil
}

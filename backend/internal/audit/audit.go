// Package audit records security-relevant events to durable storage.
// Credentials, tokens and other secrets must never be placed in Metadata.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Well-known action codes used across the panel.
const (
	ActionLoginSuccess      = "auth.login.success"
	ActionLoginFailure      = "auth.login.failure"
	ActionLogout            = "auth.logout"
	ActionPasswordChanged   = "auth.password.changed"
	ActionPasswordResetRequested = "auth.password.reset_requested"
	ActionSessionRevoked    = "auth.session.revoked"
	ActionUserCreated       = "users.created"
	ActionUserUpdated       = "users.updated"
	ActionUserDeleted       = "users.deleted"
	ActionRoleAssigned      = "rbac.role_assigned"
	ActionPermissionGranted = "rbac.permission_granted"
	ActionRoleCreated       = "rbac.role_created"
	ActionRoleUpdated       = "rbac.role_updated"
	ActionRoleDeleted       = "rbac.role_deleted"
	ActionLicenseActivated  = "license.activated"
	ActionLicenseRefreshed  = "license.refreshed"
	ActionLicenseDeactivated = "license.deactivated"
	ActionSettingsUpdated   = "settings.updated"
	ActionInstallerCompleted = "installer.completed"
	ActionServerRegistered  = "servers.registered"
	ActionServerRevoked     = "servers.revoked"
	ActionAgentHeartbeat    = "servers.heartbeat"

	// Phase 2 — hosting engine
	ActionServerTokenCreated    = "servers.token_created"
	ActionServerTokenRevoked    = "servers.token_revoked"
	ActionServerProbed          = "servers.probed"
	ActionDomainCreated         = "domains.created"
	ActionDomainDeleted         = "domains.deleted"
	ActionWebsiteCreated        = "websites.created"
	ActionWebsiteUpdated        = "websites.updated"
	ActionWebsiteEnabled        = "websites.enabled"
	ActionWebsiteDisabled       = "websites.disabled"
	ActionWebsiteReloaded       = "websites.reloaded"
	ActionWebsiteDeleteRequested = "websites.delete_requested"
	ActionWebsiteRetryRequested  = "websites.retry_requested"
	ActionWebsiteProvisioned    = "websites.provisioned"
	ActionWebsiteDeleted        = "websites.deleted"
	ActionWebsiteLimitsUpdated  = "websites.limits_updated"
	ActionJobFailed             = "jobs.failed"
)

type Entry struct {
	ActorType string // user | system | agent | installer
	ActorID   *uuid.UUID
	Label     string // human-readable actor description (e.g. username or hostname)
	Action    string
	Resource  string
	ResourceID string
	IP        string
	UserAgent string
	Metadata  map[string]any
}

type Service struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	return &Service{pool: pool, log: log}
}

// Log persists an entry. Failures are logged but never abort business logic:
// an un-writable audit trail should not make the panel unusable (though the
// operator will see the error in server logs).
func (s *Service) Log(ctx context.Context, e Entry) {
	if e.ActorType == "" {
		e.ActorType = "system"
	}
	meta := []byte("{}")
	if e.Metadata != nil {
		raw, err := json.Marshal(e.Metadata)
		if err != nil {
			s.log.Warn("audit metadata marshal failed", "action", e.Action, "err", err)
			raw, _ = json.Marshal(map[string]any{"marshal_error": err.Error()})
		}
		meta = raw
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO audit_logs (actor_type, actor_id, actor_label, action, resource, resource_id, ip_address, user_agent, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, e.ActorType, actorIDOrNull(e.ActorID), e.Label, e.Action, e.Resource, e.ResourceID, e.IP, e.UserAgent, meta); err != nil {
		s.log.Error("audit write failed", "action", e.Action, "err", err)
	}
}

type LogItem struct {
	ID          int64          `json:"id"`
	ActorType   string         `json:"actor_type"`
	ActorLabel  string         `json:"actor_label"`
	Action      string         `json:"action"`
	Resource    string         `json:"resource"`
	ResourceID  string         `json:"resource_id"`
	IPAddress   string         `json:"ip_address"`
	MetadataRaw map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"created_at"`
}

// Recent returns the newest entries, optionally filtered by action prefix.
func (s *Service) Recent(ctx context.Context, limit int, actions []string) ([]LogItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	args := []any{limit}
	q := `SELECT id, actor_type, actor_label, action, resource, resource_id, ip_address, metadata, created_at FROM audit_logs`
	if len(actions) > 0 {
		q += ` WHERE action = ANY($2)`
		args = append(args, actions)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $1`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LogItem
	for rows.Next() {
		var it LogItem
		var meta []byte
		if err := rows.Scan(&it.ID, &it.ActorType, &it.ActorLabel, &it.Action, &it.Resource, &it.ResourceID, &it.IPAddress, &meta, &it.CreatedAt); err != nil {
			return nil, err
		}
		it.MetadataRaw = map[string]any{}
		if len(meta) > 0 {
			_ = json.Unmarshal(meta, &it.MetadataRaw)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func actorIDOrNull(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return *id
}

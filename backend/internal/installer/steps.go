package installer

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
)

// Step handlers. Every mutating method re-verifies installer state server-side
// before doing anything; the UI's ordering is a convenience, never the guard.

const actionPrefix = "installer."

func (s *Service) audit(ctx context.Context, action string, meta map[string]any) {
	s.auditSvc.Log(ctx, audit.Entry{ActorType: "installer", Label: "system", Action: action, Metadata: meta})
}

func lockedErr(err error) error {
	if errors.Is(err, ErrInstallerLocked) {
		return apierror.InstallerLocked
	}
	return err
}

// CheckRequirements runs host checks and stores the latest snapshot.
func (s *Service) CheckRequirements(ctx context.Context) (*Report, error) {
	if _, err := s.RequireNotLocked(ctx); err != nil {
		return nil, lockedErr(err)
	}
	report := RunChecks(s.version)
	var hasError bool
	for _, c := range report.Checks {
		if c.Severity == SeverityError {
			hasError = true
		}
	}
	if !hasError {
		_ = s.settingSvc.Set(ctx, SettingsKeyReqsSnapshot, mustJSON(report))
	}
	return report, nil
}

func (s *Service) VerifyDatabase(ctx context.Context) (map[string]any, error) {
	if _, err := s.RequireNotLocked(ctx); err != nil {
		return nil, lockedErr(err)
	}
	verr := s.dbVerifier(ctx)
	out := map[string]any{"reachable": verr == nil}
	if verr != nil {
		out["message"] = "Panel cannot reach PostgreSQL using its configured connection. Fix EPICPANEL_DATABASE_DSN or use the override below."
	}
	return out, verr
}

// ReplaceDatabase persists an operator-provided DSN after verifying it, then
// asks for a panel restart to adopt it (never stores it in session/UI).
func (s *Service) ReplaceDatabase(ctx context.Context, dsn string) (map[string]any, error) {
	row, err := s.RequireNotLocked(ctx)
	if err != nil {
		return nil, lockedErr(err)
	}
	if row.Status == StatusCompleted {
		return nil, apierror.InstallerLocked
	}
	if dsn == "" {
		return nil, apierror.BadRequest("A PostgreSQL connection string is required")
	}
	if err := verifyDSN(ctx, dsn); err != nil {
		return nil, apierror.New(400, "DB_UNREACHABLE", "Cannot connect with the provided database URL: "+err.Error())
	}
	if err := s.dsnPersister(dsn); err != nil {
		return nil, apierror.From(err)
	}
	s.audit(ctx, actionPrefix+"database_replaced", map[string]any{"restart_required": true})
	return map[string]any{"restart_required": true}, nil
}

// ActivateLicense delegates to the licensing service and audits the outcome.
func (s *Service) ActivateLicense(ctx context.Context, key string) (any, error) {
	if _, err := s.RequireNotLocked(ctx); err != nil {
		return nil, lockedErr(err)
	}
	info, err := s.lic.Activate(ctx, key)
	if err != nil {
		s.audit(ctx, "license.activation_failed", map[string]any{})
		return nil, err
	}
	s.audit(ctx, audit.ActionLicenseActivated, map[string]any{
		"plan":   info.Plan,
		"key":    info.KeyHint,
		"status": info.Status,
	})
	return info, nil
}

// ConfigurePanel records basic product settings chosen during installation.
func (s *Service) ConfigurePanel(ctx context.Context, siteName, timezone, adminEmailDomain string) (map[string]any, error) {
	if _, err := s.RequireNotLocked(ctx); err != nil {
		return nil, lockedErr(err)
	}
	if siteName == "" || len(siteName) > 128 {
		return nil, apierror.BadRequest("Provide a panel name (max 128 characters)")
	}
	if timezone == "" || len(timezone) > 64 {
		timezone = "UTC"
	}
	if err := s.settingSvc.Set(ctx, SettingsKeySiteName, siteName); err != nil {
		return nil, apierror.From(err)
	}
	if err := s.settingSvc.Set(ctx, SettingsKeyPanelTimeZone, timezone); err != nil {
		return nil, apierror.From(err)
	}
	s.audit(ctx, audit.ActionSettingsUpdated, map[string]any{"keys": []string{SettingsKeySiteName, SettingsKeyPanelTimeZone}})
	return map[string]any{"site_name": siteName, "timezone": timezone}, nil
}

// CreateAdministrator provisions the first account via RBAC role assignment.
func (s *Service) CreateAdministrator(ctx context.Context, username, email, displayName, password, confirm string) (map[string]any, error) {
	if _, err := s.RequireNotLocked(ctx); err != nil {
		return nil, lockedErr(err)
	}
	count, err := s.userStore.CountUsers(ctx)
	if err != nil {
		return nil, apierror.From(err)
	}
	if count > 0 {
		return nil, apierror.Conflict("An administrator already exists")
	}
	err = s.userStore.CreateAdministrator(ctx, username, email, displayName, password, confirm)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, audit.ActionUserCreated, map[string]any{"username": username, "source": "installer"})
	return map[string]any{"created": true}, nil
}

// ApplySecurityConfigures stores security-related overrides.
func (s *Service) ApplySecurity(ctx context.Context, minPasswordLength, maxFailedLogins, lockoutMinutes, sessionLifetimeMinutes int) (map[string]any, error) {
	if _, err := s.RequireNotLocked(ctx); err != nil {
		return nil, lockedErr(err)
	}
	clamp := func(v int, lo, hi, def int) int {
		if v <= 0 {
			return def
		}
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	minLen := clamp(minPasswordLength, 10, 128, 12)
	maxFails := clamp(maxFailedLogins, 3, 1000, 10)
	lockout := clamp(lockoutMinutes, 1, 14400, 15)
	lifetime := clamp(sessionLifetimeMinutes, 30, 43200, 1440)

	if err := s.settingSvc.Set(ctx, "security.password_min_length", minLen); err != nil {
		return nil, apierror.From(err)
	}
	if err := s.settingSvc.Set(ctx, "security.max_failed_logins", maxFails); err != nil {
		return nil, apierror.From(err)
	}
	if err := s.settingSvc.Set(ctx, "security.account_lockout_minutes", lockout); err != nil {
		return nil, apierror.From(err)
	}
	if err := s.settingSvc.Set(ctx, "security.session_lifetime_minutes", lifetime); err != nil {
		return nil, apierror.From(err)
	}
	if err := s.settingSvc.Set(ctx, SettingsKeySecurityDone, "true"); err != nil {
		return nil, apierror.From(err)
	}
	s.audit(ctx, audit.ActionSettingsUpdated, map[string]any{
		"keys": []string{"password_min_length", "max_failed_logins", "account_lockout_minutes", "session_lifetime_minutes"},
	})
	return map[string]any{"configured": true}, nil
}

// Complete finalizes installation only when all prerequisites hold. Afterward
// every installer endpoint becomes permanently inaccessible.
func (s *Service) Complete(ctx context.Context) (map[string]any, error) {
	row, err := s.RequireNotLocked(ctx)
	if err != nil {
		return nil, lockedErr(err)
	}
	steps := s.StepVerification(ctx)

	type gate struct {
		ok      bool
		code    string
		message string
	}
	gates := []gate{
		{steps["license"], "INSTALL_LICENSE_MISSING", "Activate a valid license before finishing"},
		{steps["configuration"], "INSTALL_CONFIG_MISSING", "Panel configuration is incomplete"},
		{steps["administrator"], "INSTALL_ADMIN_MISSING", "Create the first administrator before finishing"},
		{steps["database"], "INSTALL_DB_FAILED", "Database connectivity check failed"},
	}
	for _, g := range gates {
		if !g.ok {
			return nil, apierror.New(409, g.code, g.message)
		}
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE installations SET status = 'completed', completed_at = now(), panel_version = $2 WHERE id = $1`,
		row.ID, s.version)
	if err != nil {
		return nil, apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, apierror.InstallerLocked
	}
	s.audit(ctx, audit.ActionInstallerCompleted, map[string]any{
		"instance_id": row.InstanceID,
		"version":     s.version,
	})
	return map[string]any{"completed": true, "instance_id": row.InstanceID}, nil
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

package websites

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("website not found")

type Status string

const (
	StatusPending      Status = "pending"
	StatusProvisioning Status = "provisioning"
	StatusReady        Status = "ready"
	StatusFailed       Status = "failed"
	StatusDeleting     Status = "deleting"
	StatusDeleted      Status = "deleted"
	StatusSuspended    Status = "suspended"
	StatusTerminated   Status = "terminated"
)

type Runtime string

const (
	RuntimeStatic Runtime = "static"
	RuntimePHP    Runtime = "php"
	RuntimeNode   Runtime = "node"
	RuntimePython Runtime = "python"
	RuntimeGo     Runtime = "go"
)

type Website struct {
	ID               uuid.UUID  `json:"id"`
	Organization     uuid.UUID  `json:"organization_id"`
	ServerID         uuid.UUID  `json:"server_id"`
	Name             string     `json:"name"`
	PrimaryDomain    string     `json:"primary_domain"`
	Runtime          Runtime    `json:"runtime"`
	RuntimeVersion   string     `json:"runtime_version"`
	WebServer        string     `json:"web_server"`
	BackendPort      int        `json:"backend_port"`
	DocrootSuffix    string     `json:"docroot_suffix"`
	DeployWebDir     string     `json:"deploy_web_dir"`
	// App-platform serving mode (node/python/go): nginx proxies to the app
	// process on AppPort; lifecycle is desired-state driven.
	AppStartupCommand string    `json:"app_startup_command"`
	AppBuildCommand   string    `json:"app_build_command"`
	AppPort           int       `json:"app_port"`
	AppDesiredState   string    `json:"app_desired_state"`
	UsageCPUPercent  float64    `json:"usage_cpu_percent"`
	UsageMemoryBytes int64      `json:"usage_memory_bytes"`
	UsageDiskMB      int64      `json:"usage_disk_mb"`
	UsageProcesses   int        `json:"usage_processes"`
	UsageSampledAt   *time.Time `json:"usage_sampled_at,omitempty"`
	Status           Status     `json:"status"`
	UnixUser         string     `json:"unix_user"`
	DocumentRoot     string     `json:"document_root"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	IsStaging        bool       `json:"is_staging"`
	// Lifecycle reasons (migration 0050): suspension carries WHY + when;
	// termination is its own status with a timestamp + free-form reason.
	SuspensionReason   *string        `json:"suspension_reason,omitempty"`
	SuspendedAt        *time.Time     `json:"suspended_at,omitempty"`
	SuspensionMetadata map[string]any `json:"suspension_metadata,omitempty"`
	TerminatedAt       *time.Time     `json:"terminated_at,omitempty"`
	TerminationReason  *string        `json:"termination_reason,omitempty"`
	// BandwidthLimitMB is the per-site monthly override: NULL = use the
	// hosting plan, 0 = unlimited, > 0 = override in MB.
	BandwidthLimitMB   *int64 `json:"bandwidth_limit_mb"`
	CreatedBy        uuid.UUID  `json:"created_by,omitempty"`
	BackupSchedule   string     `json:"backup_schedule"`
	BackupRetention  int        `json:"backup_retention"`
	StagingOf        *uuid.UUID `json:"staging_of,omitempty"`
	DeployRepoURL    string     `json:"deploy_repo_url,omitempty"`
	DeployBranch     string     `json:"deploy_branch,omitempty"`
	LastBackupAt     *time.Time `json:"last_backup_at,omitempty"`
	ProvisionedAt    *time.Time `json:"provisioned_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	// Dynamic resources (traffic-adaptive allocation + bot defense).
	DynamicEnabled bool   `json:"dynamic_enabled"`
	DynamicTier    int    `json:"dynamic_tier"`
	DynamicState   string `json:"dynamic_state"` // active | busy | suspended_attack
	FreePerk       bool   `json:"free_perk"`
}

// DesiredPayload is what the agent receives in a provision job.
type DesiredPayload struct {
	WebsiteID      uuid.UUID         `json:"website_id"`
	Organization   string            `json:"organization"`
	Name           string            `json:"name"`
	UnixUser       string            `json:"unix_user"`
	Runtime        string            `json:"runtime"`
	RuntimeVersion string            `json:"runtime_version,omitempty"`
	WebServer      string            `json:"web_server,omitempty"`
	BackendPort    int               `json:"backend_port,omitempty"`
	DocrootSuffix  string            `json:"docroot_suffix,omitempty"`
	// WebDir is the repo-relative running directory for git-deployed sites
	// (e.g. "public" for Laravel): the agent serves <site>/public/<web_dir>,
	// which follows the release symlink. Wins over DocrootSuffix when set.
	WebDir         string            `json:"web_dir,omitempty"`
	DocumentRoot   string            `json:"document_root"`
	PrimaryDomain  string            `json:"primary_domain"`
	RewriteRules   string            `json:"rewrite_rules,omitempty"`
	Domains        []DesiredDomain   `json:"domains,omitempty"`
	Redirects      []DesiredRedirect `json:"redirects,omitempty"`
	// FPM pool sizing derived from the org's hosting package via the
	// limits seam; zero = agent defaults.
	FpmMemoryLimitMB int `json:"fpm_memory_limit_mb,omitempty"`
	FpmMaxChildren   int `json:"fpm_max_children,omitempty"`
	// App-platform serving mode (node/python/go): nginx reverse-proxies to
	// the site's app process on AppPort. Empty commands mean agent defaults.
	AppStartupCommand string            `json:"app_startup_command,omitempty"`
	AppBuildCommand   string            `json:"app_build_command,omitempty"`
	AppPort           int               `json:"app_port,omitempty"`
	AppEnvEnc         string            `json:"app_env_enc,omitempty"` // base64 secretbox of the JSON env map
	AppEnv            map[string]string `json:"app_env,omitempty"`     // plaintext alternative (tests)
	// PHPSettings are validated per-site php.ini overrides rendered into the
	// FPM pool (MultiPHP INI Editor equivalent).
	PHPSettings map[string]string `json:"php_settings,omitempty"`
	// RequestTerminateTimeout caps a single PHP request (seconds; 0 = default).
	RequestTerminateTimeout int `json:"request_terminate_timeout,omitempty"`
}

// DesiredRedirect is one rewrite-style redirect (from -> to) served by the
// web server for the website.
type DesiredRedirect struct {
	Domain string `json:"from"`
	To     string `json:"to"`
	Status int    `json:"status"`
}

// DesiredDomain is one domain (primary or alias) with its SSL configuration.
type DesiredDomain struct {
	Domain        string `json:"domain"`
	SSLMode       string `json:"ssl_mode"`
	CertPath      string `json:"cert_path,omitempty"`
	KeyPath       string `json:"key_path,omitempty"`
	DocrootSuffix string `json:"docroot_suffix,omitempty"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, organization_id, server_id, name, primary_domain, runtime, runtime_version, web_server, backend_port, docroot_suffix, app_startup_command, app_build_command, app_port, app_desired_state, usage_cpu_percent, usage_memory_bytes, usage_disk_mb, usage_processes, usage_sampled_at, status, unix_user, document_root, error_message, is_staging, staging_of, backup_schedule, backup_retention, deploy_repo_url, deploy_branch, last_backup_at, provisioned_at, created_at, created_by, dynamic_enabled, dynamic_tier, dynamic_state, free_perk, suspension_reason, suspended_at, suspension_metadata, terminated_at, termination_reason, bandwidth_limit_mb, deploy_web_dir`

func scanRow(row pgx.Row) (*Website, error) {
	var w Website
	var suspensionMeta []byte
	err := row.Scan(&w.ID, &w.Organization, &w.ServerID, &w.Name, &w.PrimaryDomain, &w.Runtime, &w.RuntimeVersion, &w.WebServer, &w.BackendPort, &w.DocrootSuffix,
		&w.AppStartupCommand, &w.AppBuildCommand, &w.AppPort, &w.AppDesiredState,
		&w.UsageCPUPercent, &w.UsageMemoryBytes, &w.UsageDiskMB, &w.UsageProcesses, &w.UsageSampledAt,
		&w.Status, &w.UnixUser, &w.DocumentRoot, &w.ErrorMessage, &w.IsStaging, &w.StagingOf, &w.BackupSchedule, &w.BackupRetention,
		&w.DeployRepoURL, &w.DeployBranch, &w.LastBackupAt, &w.ProvisionedAt, &w.CreatedAt, &w.CreatedBy,
		&w.DynamicEnabled, &w.DynamicTier, &w.DynamicState, &w.FreePerk,
		&w.SuspensionReason, &w.SuspendedAt, &suspensionMeta, &w.TerminatedAt, &w.TerminationReason, &w.BandwidthLimitMB, &w.DeployWebDir)
	if err != nil {
		return nil, err
	}
	if len(suspensionMeta) > 0 {
		if js := json.Unmarshal(suspensionMeta, &w.SuspensionMetadata); js != nil {
			// A malformed metadata blob must never break site reads; it is
			// advisory page-template data, so it is dropped silently here
			// and the raw JSONB stays inspectable in the DB.
			w.SuspensionMetadata = nil
		}
	}
	return &w, nil
}

// unixUserFor derives the dedicated system user for a website: ep-<org8>-<name>
// truncated to 32 chars (Linux user name limit).
func unixUserFor(orgID uuid.UUID, name string) string {
	org := orgID.String()
	if len(org) > 8 {
		org = org[:8]
	}
	full := "ep-" + org + "-" + name
	if len(full) > 32 {
		full = full[:32]
	}
	return full
}

// DocumentRootFor is the canonical document root layout for a website.
func DocumentRootFor(websiteID uuid.UUID) string {
	return "/srv/epicpanel/websites/" + websiteID.String() + "/public"
}

func (s *Store) Create(ctx context.Context, orgID, serverID, createdBy uuid.UUID, name, primaryDomain string, runtime Runtime, runtimeVersion, webServer string) (*Website, string, error) {
	if webServer == "" {
		webServer = "nginx" // default serving path (mirrors the API handler)
	}
	unixUser := unixUserFor(orgID, name)
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO websites (organization_id, server_id, name, primary_domain, runtime, runtime_version, web_server, unix_user, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+cols,
		orgID, serverID, name, primaryDomain, runtime, runtimeVersion, webServer, unixUser, createdBy,
	)
	w, err := scanRow(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, "", ErrNameTaken
		}
		return nil, "", err
	}
	return w, unixUser, nil
}

func (s *Store) GetByID(ctx context.Context, orgID, websiteID uuid.UUID) (*Website, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM websites WHERE id = $1 AND organization_id = $2`, websiteID, orgID)
	w, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// GetByIDAny resolves a website without org scoping (internal fanout only).
func (s *Store) GetByIDAny(ctx context.Context, websiteID uuid.UUID) (*Website, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM websites WHERE id = $1`, websiteID)
	w, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

func (s *Store) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]Website, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM websites WHERE organization_id = $1 ORDER BY created_at ASC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Website
	for rows.Next() {
		w, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// SetBandwidthLimitMB writes the per-site monthly bandwidth override
// (migration 0050): NULL = follow the hosting plan, 0 = unlimited,
// > 0 = MB. Enforcement, the quota API, the resume guard and the
// suspension-page metadata all resolve through this single column.
func (s *Store) SetBandwidthLimitMB(ctx context.Context, websiteID uuid.UUID, limitMB *int64) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET bandwidth_limit_mb = $2, updated_at = now()
		WHERE id = $1
	`, websiteID, limitMB)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetStatus(ctx context.Context, websiteID uuid.UUID, status Status, errMsg string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET status = $2, error_message = $3, updated_at = now()
		WHERE id = $1
	`, websiteID, status, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkReady sets the website ready and records the document root reported by
// the agent (actual state wins over desired state). It never resurrects a
// site that is deleting/deleted/terminated (ErrMarkReadyBlocked) and leaves
// a suspended site suspended (no-op nil).
func (s *Store) MarkReady(ctx context.Context, websiteID uuid.UUID, unixUser, documentRoot string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET status = 'ready', unix_user = $2, document_root = $3,
		       provisioned_at = now(), error_message = '', updated_at = now()
		WHERE id = $1 AND status NOT IN ('deleting', 'deleted', 'suspended', 'terminated')
	`, websiteID, unixUser, documentRoot)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var status Status
		scanErr := s.Pool.QueryRow(ctx, `SELECT status FROM websites WHERE id = $1`, websiteID).Scan(&status)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if scanErr != nil {
			return scanErr
		}
		if status == StatusSuspended {
			return nil
		}
		return ErrMarkReadyBlocked
	}
	return nil
}

// SetRuntimeVersion updates the desired runtime version (reconciliation source).
func (s *Store) SetRuntimeVersion(ctx context.Context, websiteID uuid.UUID, version string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET runtime_version = $2, updated_at = now() WHERE id = $1
	`, websiteID, version)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRuntime changes the site's runtime type (static/php/node/python/go).
func (s *Store) SetRuntime(ctx context.Context, websiteID uuid.UUID, rt Runtime) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET runtime = $2, updated_at = now() WHERE id = $1
	`, websiteID, rt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWebServer updates the web server selection (comma list, e.g.
// "nginx,apache"). Converged by the agent on the next provision job.
func (s *Store) SetWebServer(ctx context.Context, websiteID uuid.UUID, ws string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET web_server = $2, updated_at = now() WHERE id = $1
	`, websiteID, ws)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetBackendPort persists the allocated private backend port (0 = none).
func (s *Store) SetBackendPort(ctx context.Context, websiteID uuid.UUID, port int) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET backend_port = $2, updated_at = now() WHERE id = $1
	`, websiteID, port)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAppConfig persists the app-platform desired state (node/python/go
// websites): startup/build commands and the desired process state.
func (s *Store) SetAppConfig(ctx context.Context, websiteID uuid.UUID, startup, build, desiredState string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET app_startup_command = $2, app_build_command = $3,
		       app_desired_state = $4, updated_at = now()
		WHERE id = $1
	`, websiteID, startup, build, desiredState)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAppPort persists the allocated private app port (0 = none).
func (s *Store) SetAppPort(ctx context.Context, websiteID uuid.UUID, port int) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET app_port = $2, updated_at = now() WHERE id = $1
	`, websiteID, port)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAppEnv stores the site's env vars encrypted at rest (nil leaves the
// existing blob untouched, mirroring SetDeployConfig).
func (s *Store) SetAppEnv(ctx context.Context, websiteID uuid.UUID, envEncrypted []byte) error {
	if envEncrypted == nil {
		return nil
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET app_env_encrypted = $2, updated_at = now() WHERE id = $1
	`, websiteID, envEncrypted)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetAppEnv returns the encrypted env blob for a website (nil = none).
func (s *Store) GetAppEnv(ctx context.Context, websiteID uuid.UUID) ([]byte, error) {
	var blob []byte
	err := s.Pool.QueryRow(ctx, `SELECT app_env_encrypted FROM websites WHERE id = $1`, websiteID).Scan(&blob)
	if err != nil {
		return nil, err
	}
	return blob, nil
}

// UsedAppPorts returns all app ports currently allocated on a server.
func (s *Store) UsedAppPorts(ctx context.Context, serverID uuid.UUID) (map[int]bool, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT app_port FROM websites
		WHERE server_id = $1 AND app_port > 0 AND status NOT IN ('deleted', 'deleting')
	`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		used[p] = true
	}
	return used, rows.Err()
}

// ListPendingForRuntime returns pending websites on a server waiting for the
// given runtime/version — created with install_if_missing; they provision
// once the runtime install lands (job chaining).
func (s *Store) ListPendingForRuntime(ctx context.Context, serverID uuid.UUID, rt Runtime, version string) ([]Website, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+cols+` FROM websites
		WHERE server_id = $1 AND runtime = $2 AND runtime_version = $3 AND status = 'pending'
		ORDER BY created_at ASC
	`, serverID, rt, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Website
	for rows.Next() {
		w, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// StoreUsage persists a resource-usage snapshot returned by the agent.
func (s *Store) StoreUsage(ctx context.Context, websiteID uuid.UUID, cpuPercent float64, memBytes, diskMB int64, procs int) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET usage_cpu_percent = $2, usage_memory_bytes = $3, usage_disk_mb = $4,
		       usage_processes = $5, usage_sampled_at = now(), updated_at = now()
		WHERE id = $1
	`, websiteID, cpuPercent, memBytes, diskMB, procs)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDocrootSuffix persists a relative docroot override (e.g. "public" for
// Laravel layouts). Validated by the caller; empty = standard public dir.
func (s *Store) SetDocrootSuffix(ctx context.Context, websiteID uuid.UUID, suffix string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET docroot_suffix = $2, updated_at = now() WHERE id = $1
	`, websiteID, suffix)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetServingDocroot persists a docroot override together with the resolved
// absolute path (actual state reported by the agent, e.g. after a one-click
// Laravel install moves serving to <site>/app/public).
func (s *Store) SetServingDocroot(ctx context.Context, websiteID uuid.UUID, suffix, documentRoot string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET docroot_suffix = $2, document_root = $3, updated_at = now() WHERE id = $1
	`, websiteID, suffix, documentRoot)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UsedBackendPorts returns all backend ports currently allocated on a server
// (any mode/port value > 0). The caller excludes its own website when
// re-allocating.
func (s *Store) UsedBackendPorts(ctx context.Context, serverID uuid.UUID) (map[int]bool, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT backend_port FROM websites
		WHERE server_id = $1 AND backend_port > 0 AND status NOT IN ('deleted', 'deleting')
	`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		used[p] = true
	}
	return used, rows.Err()
}

func (s *Store) Delete(ctx context.Context, orgID, websiteID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM websites WHERE id = $1 AND organization_id = $2`, websiteID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteByServer removes the website row after a successful agent-side delete.
// Scoped by server so a compromised agent can only affect its own rows.
func (s *Store) DeleteByServer(ctx context.Context, serverID, websiteID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM websites WHERE id = $1 AND server_id = $2`, websiteID, serverID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

var ErrNameTaken = errors.New("a website with that name already exists on this server")

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

// SetDeployConfig stores git deployment configuration; the token is stored
// encrypted (nil leaves the existing token untouched).
func (s *Store) SetDeployConfig(ctx context.Context, websiteID uuid.UUID, repoURL, branch string, tokenEncrypted []byte, webDir string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET deploy_repo_url = $2, deploy_branch = $3, deploy_web_dir = $4, updated_at = now() WHERE id = $1
	`, websiteID, repoURL, branch, webDir)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if tokenEncrypted != nil {
		_, err = s.Pool.Exec(ctx, `UPDATE websites SET deploy_token_encrypted = $2 WHERE id = $1`, websiteID, tokenEncrypted)
		if err != nil {
			return err
		}
	}
	return nil
}

// TouchLastBackup records a successful backup timestamp.
func (s *Store) TouchLastBackup(ctx context.Context, websiteID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE websites SET last_backup_at = now(), updated_at = now() WHERE id = $1`, websiteID)
	return err
}

// SetBackupSchedule configures scheduled backups.
func (s *Store) SetBackupSchedule(ctx context.Context, websiteID uuid.UUID, schedule string, retention int) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET backup_schedule = $2, backup_retention = $3, updated_at = now() WHERE id = $1
	`, websiteID, schedule, retention)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateStagingCopy creates a staging website mirroring the production site's
// runtime/web server on the same server, named <name>-staging.
func (s *Store) CreateStagingCopy(ctx context.Context, orgID uuid.UUID, prod *Website, createdBy uuid.UUID) (*Website, error) {
	name := prod.Name + "-staging"
	if len(name) > 63 {
		name = name[:63]
	}
	unixUser := unixUserFor(orgID, name)
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO websites (organization_id, server_id, name, primary_domain, runtime, runtime_version, web_server, unix_user, created_by, is_staging, staging_of)
		VALUES ($1, $2, $3, '', $4, $5, $6, $7, $8, TRUE, $9)
		RETURNING `+cols,
		orgID, prod.ServerID, name, prod.Runtime, prod.RuntimeVersion, prod.WebServer, unixUser, createdBy, prod.ID,
	)
	w, err := scanRow(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	return w, nil
}

// StaleUsage returns ready websites whose usage snapshot is older than the
// given age (or never sampled), for the usage scheduler.
func (s *Store) StaleUsage(ctx context.Context, olderThan time.Duration, limit int) ([]Website, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+cols+` FROM websites
		WHERE status = 'ready'
		  AND (usage_sampled_at IS NULL OR usage_sampled_at < now() - $1::interval)
		ORDER BY usage_sampled_at NULLS FIRST
		LIMIT $2
	`, olderThan, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Website
	for rows.Next() {
		w, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// ReadyForLimits returns ready websites (with their org + server) for the
// resource-limits convergence scheduler: the engine re-enforces the plan
// matrix on every site each pass (idempotent desired state).
func (s *Store) ReadyForLimits(ctx context.Context, limit int) ([]Website, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+cols+` FROM websites
		WHERE status = 'ready'
		ORDER BY usage_sampled_at NULLS FIRST
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Website
	for rows.Next() {
		w, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

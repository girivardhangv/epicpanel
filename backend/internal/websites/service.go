// Website lifecycle management. Status transitions:
//
//	provisioning -> active | error (job outcome)
//	active <-> disabled (direct agent ops)
//	active|disabled|error -> deleting (delete job) -> row removed
package websites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/domains"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Website statuses.
const (
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusDisabled     = "disabled"
	StatusError        = "error"
	StatusDeleting     = "deleting"
)

// Actor identifies who triggered an operation (for audit entries).
type Actor struct {
	ID    *string // user UUID
	Label string  // username
	IP    string
}

type Deps struct {
	Pool     *pgxpool.Pool
	Log      *slog.Logger
	Agent    *agentclient.Client
	Servers  *servers.Service
	Domains  *domains.Service
	Settings *settings.Service
	Jobs     *jobs.Store
	Audit    *audit.Service
}

type Service struct {
	deps Deps
}

func New(deps Deps) *Service { return &Service{deps} }

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type Website struct {
	ID           string   `json:"id"`
	ServerID     string   `json:"server_id"`
	ServerName   string   `json:"server_name"`
	ServerOS     string   `json:"server_os"`
	ServerOnline bool     `json:"server_online"`
	DomainID     string   `json:"domain_id"`
	Domain       string   `json:"domain"`
	Aliases      []string `json:"aliases"`
	Name         string   `json:"name"`
	DocumentRoot string   `json:"document_root"`
	Status       string   `json:"status"`
	PHPVersion   string   `json:"php_version"`
	WebServer    string   `json:"web_server"`
	OSUser       string   `json:"os_user"`     // per-site system user (Linux only)
	CPULimitPct  int      `json:"cpu_limit_pct"`   // 0 = unlimited; 1..100 = quota %
	MemoryLimitMB int     `json:"memory_limit_mb"` // 0 = unlimited; MB ceiling
	PHPSettings  map[string]string `json:"php_settings,omitempty"`
	ActiveJob    string   `json:"active_job_status,omitempty"` // queued|running|"" when idle
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

const websiteCols = `
	w.id, w.server_id, w.domain_id, COALESCE(w.name,''), w.document_root, w.status,
	COALESCE(w.php_version,''), COALESCE(w.web_server,'nginx'),
	COALESCE(w.os_user,''),
	COALESCE(rc.cpu_limit_pct,0), COALESCE(rc.memory_limit_mb,0),
	COALESCE(rc.php_settings,'{}'::jsonb)::text,
	w.created_at::text, w.updated_at::text`

func scanWebsite(rows pgx.Rows) (*Website, error) {
	var w Website
	var settingsRaw string
	var serverName, serverOS string
	var serverLastSeen *string
	var serverStatus string
	if err := rows.Scan(&w.ID, &w.ServerID, &w.DomainID, &w.Name, &w.DocumentRoot, &w.Status,
		&w.PHPVersion, &w.WebServer, &w.OSUser, &w.CPULimitPct, &w.MemoryLimitMB,
		&settingsRaw, &w.CreatedAt, &w.UpdatedAt,
		&w.Domain, &serverName, &serverOS, &serverStatus, &serverLastSeen); err != nil {
		return nil, err
	}
	w.PHPSettings = map[string]string{}
	if settingsRaw != "" {
		_ = json.Unmarshal([]byte(settingsRaw), &w.PHPSettings)
	}
	w.ServerName = serverName
	w.ServerOS = serverOS
	w.ServerOnline = serverStatus == "online" && serverLastSeen != nil
	return &w, nil
}

const websiteFrom = `
	FROM websites w
	JOIN domains d ON d.id = w.domain_id
	JOIN servers s ON s.id = w.server_id
	LEFT JOIN website_runtime_config rc ON rc.website_id = w.id`

func (s *Service) attachAliases(ctx context.Context, list []Website) ([]Website, error) {
	if len(list) == 0 {
		return list, nil
	}
	rows, err := s.deps.Pool.Query(ctx,
		`SELECT website_id, domain FROM domains WHERE website_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bySite := map[string][]string{}
	for rows.Next() {
		var siteID, dom string
		if err := rows.Scan(&siteID, &dom); err != nil {
			return nil, err
		}
		bySite[siteID] = append(bySite[siteID], dom)
	}
	for i := range list {
		list[i].Aliases = bySite[list[i].ID]
		if list[i].Aliases == nil {
			list[i].Aliases = []string{}
		}
	}
	return list, rows.Err()
}

func (s *Service) attachActiveJobs(ctx context.Context, list []Website) error {
	for i := range list {
		active, err := s.deps.Jobs.ActiveForWebsite(ctx, list[i].ID)
		if err != nil {
			return err
		}
		if active {
			list[i].ActiveJob = jobs.StatusRunning
		}
	}
	return nil
}

// List returns websites with search/status filters applied in SQL.
func (s *Service) List(ctx context.Context, search, status, serverID string) ([]Website, error) {
	q := `SELECT ` + websiteCols + `, d.domain, COALESCE(s.label,''), s.os, s.status, s.last_seen_at::text ` + websiteFrom
	var args []any
	conds := []string{}
	if search != "" {
		conds = append(conds, `(d.domain ILIKE $`+fmt.Sprint(len(args)+1)+` OR COALESCE(w.name,'') ILIKE $`+fmt.Sprint(len(args)+1)+`)`)
		args = append(args, "%"+search+"%")
	}
	if status != "" {
		conds = append(conds, `w.status = $`+fmt.Sprint(len(args)+1))
		args = append(args, status)
	}
	if serverID != "" {
		conds = append(conds, `w.server_id = $`+fmt.Sprint(len(args)+1))
		args = append(args, serverID)
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, " AND ")
	}
	q += ` ORDER BY w.created_at DESC`

	rows, err := s.deps.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []Website{}
	for rows.Next() {
		w, err := scanWebsite(rows)
		if err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, *w)
	}
	if err := rows.Err(); err != nil {
		return nil, apierror.From(err)
	}
	if _, err := s.attachAliases(ctx, out); err != nil {
		return nil, apierror.From(err)
	}
	if err := s.attachActiveJobs(ctx, out); err != nil {
		return nil, apierror.From(err)
	}
	return out, nil
}

// Get fetches one website with joined metadata.
func (s *Service) Get(ctx context.Context, id string) (*Website, error) {
	rows, err := s.deps.Pool.Query(ctx,
		`SELECT `+websiteCols+`, d.domain, COALESCE(s.label,''), s.os, s.status, s.last_seen_at::text `+
			websiteFrom+` WHERE w.id = $1`, id)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, apierror.From(err)
		}
		return nil, apierror.NotFound("website")
	}
	w, err := scanWebsite(rows)
	if err != nil {
		return nil, apierror.From(err)
	}
	list, err := s.attachAliases(ctx, []Website{*w})
	if err != nil {
		return nil, apierror.From(err)
	}
	if err := s.attachActiveJobs(ctx, list); err != nil {
		return nil, apierror.From(err)
	}
	return &list[0], nil
}

// ---------------------------------------------------------------------------
// Creation
// ---------------------------------------------------------------------------

type CreateInput struct {
	ServerID      string
	DomainID      string
	AliasIDs      []string
	PHPVersion    string
	DocumentRoot  string // optional override
}

type CreateResult struct {
	Website *Website
	Job     *jobs.Job
}

func (s *Service) Create(ctx context.Context, actor Actor, in CreateInput) (*CreateResult, error) {
	if in.ServerID == "" || in.DomainID == "" {
		return nil, apierror.BadRequest("server_id and domain_id are required")
	}

	srv, err := s.deps.Servers.OpsTarget(ctx, in.ServerID)
	if err != nil {
		return nil, err
	}
	if srv.Status == "revoked" {
		return nil, apierror.BadRequest("server is revoked")
	}
	if !srv.Manageable {
		return nil, apierror.New(409, "SERVER_NOT_MANAGEABLE",
			"server has no management channel; re-enroll the agent with ops support")
	}

	dom, err := s.deps.Domains.Get(ctx, in.DomainID)
	if err != nil {
		return nil, err
	}
	if dom.ServerID != in.ServerID {
		return nil, apierror.BadRequest("domain belongs to a different server")
	}
	if dom.WebsiteID != nil {
		return nil, apierror.Conflict("domain is already used by another website")
	}
	if dom.Type == domains.TypeAlias {
		return nil, apierror.BadRequest("the primary website domain cannot be of type alias")
	}

	// Wildcard domains cannot be website primaries in this phase.
	if err := domains.Validate(dom.Domain, false); err != nil {
		return nil, apierror.BadRequest("domain cannot host a website: " + err.Error())
	}

	var aliasNames []string
	for _, aliasID := range in.AliasIDs {
		ad, err := s.deps.Domains.Get(ctx, aliasID)
		if err != nil {
			return nil, err
		}
		if ad.ServerID != in.ServerID {
			return nil, apierror.BadRequest("alias " + ad.Domain + " belongs to a different server")
		}
		if ad.WebsiteID != nil {
			return nil, apierror.Conflict("alias " + ad.Domain + " is already attached to a website")
		}
		if err := domains.Validate(ad.Domain, true); err != nil {
			return nil, apierror.BadRequest("alias " + ad.Domain + " is not usable: " + err.Error())
		}
		aliasNames = append(aliasNames, ad.Domain)
	}

	if in.PHPVersion != "" {
		ok, err := s.phpVersionAvailable(ctx, in.ServerID, in.PHPVersion)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apierror.BadRequest("PHP " + in.PHPVersion + " is not installed on this server")
		}
	}

	sitesRoot := s.sitesRoot(ctx, srv.OS)
	plan, err := ResolveSitePaths(sitesRoot, dom.Domain, srv.OS)
	if err != nil {
		return nil, apierror.BadRequest(err.Error())
	}
	docRoot := plan.PublicDir
	if override, err := ValidateDocumentRootOverride(sitesRoot, dom.Domain, in.DocumentRoot, srv.OS); err != nil {
		return nil, apierror.BadRequest(err.Error())
	} else if override != "" {
		docRoot = override
	}

	tx, err := s.deps.Pool.Begin(ctx)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer tx.Rollback(ctx)

	var websiteID string
	osUser := SiteUserName(dom.Domain)
	err = tx.QueryRow(ctx, `
		INSERT INTO websites (server_id, domain_id, name, document_root, status, php_version, web_server, os_user)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		in.ServerID, in.DomainID, dom.Domain, docRoot, StatusProvisioning, in.PHPVersion, WebServerNginx, osUser).
		Scan(&websiteID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apierror.Conflict("a website already exists for this domain")
		}
		return nil, apierror.From(err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO website_runtime_config (website_id) VALUES ($1)`, websiteID); err != nil {
		return nil, apierror.From(err)
	}
	for _, aliasID := range in.AliasIDs {
		if _, err := tx.Exec(ctx,
			`UPDATE domains SET website_id = $2, updated_at = now() WHERE id = $1`, aliasID, websiteID); err != nil {
			return nil, apierror.From(err)
		}
	}

	payload := map[string]any{
		"document_root": docRoot,
		"php_version":   in.PHPVersion,
		"aliases":       aliasNames,
		"slug":          Slug(dom.Domain),
		"os_user":       osUser,
		"cpu_limit_pct": 0,
		"memory_limit_mb": 0,
	}
	// Same transaction as the website row: the job's FK needs the committed
	// (well, in-flight) website — pool-level inserts cannot see it.
	job, err := s.deps.Jobs.CreateTx(ctx, tx, jobs.TypeProvisionWebsite, &in.ServerID, &websiteID, payload)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.From(err)
	}

	s.audit(ctx, actor, audit.ActionWebsiteCreated, websiteID, dom.Domain, map[string]any{
		"server_id": in.ServerID, "php_version": in.PHPVersion, "job_id": job.ID,
	})

	w, err := s.Get(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	return &CreateResult{Website: w, Job: job}, nil
}

// ---------------------------------------------------------------------------
// Lifecycle operations (direct agent ops)
// ---------------------------------------------------------------------------

func (s *Service) SetEnabled(ctx context.Context, actor Actor, id string, enable bool) error {
	w, target, plan, cfg, err := s.loadForOps(ctx, id)
	if err != nil {
		return err
	}
	if w.Status != StatusActive && w.Status != StatusDisabled {
		return apierror.Conflict("website must be active or disabled to change its state")
	}
	if enable {
		// Re-deploy the stored configuration to be safe, then enable.
		content := s.renderConfig(w, plan, cfg)
		if _, err := s.deps.Agent.NginxDeploySite(ctx, target.AgentURL, target.OpsToken,
			agentclient.DeploySiteRequest{Name: Slug(w.Domain), Content: content, Enable: true}); err != nil {
			return err
		}
	} else {
		if err := s.deps.Agent.NginxSetEnabled(ctx, target.AgentURL, target.OpsToken, Slug(w.Domain), false); err != nil {
			return err
		}
	}
	if err := s.deps.Agent.NginxReload(ctx, target.AgentURL, target.OpsToken); err != nil {
		return err
	}
	next := StatusActive
	if !enable {
		next = StatusDisabled
	}
	if _, err := s.deps.Pool.Exec(ctx,
		`UPDATE websites SET status = $2, updated_at = now() WHERE id = $1`, id, next); err != nil {
		return apierror.From(err)
	}
	action := audit.ActionWebsiteEnabled
	if !enable {
		action = audit.ActionWebsiteDisabled
	}
	s.audit(ctx, actor, action, id, w.Domain, nil)
	return nil
}

func (s *Service) Reload(ctx context.Context, actor Actor, id string) error {
	w, target, _, _, err := s.loadForOps(ctx, id)
	if err != nil {
		return err
	}
	if w.Status != StatusActive {
		return apierror.Conflict("only active websites can be reloaded")
	}
	if err := s.deps.Agent.NginxReload(ctx, target.AgentURL, target.OpsToken); err != nil {
		return err
	}
	s.audit(ctx, actor, audit.ActionWebsiteReloaded, id, w.Domain, nil)
	return nil
}

// Retry re-queues provisioning for a website stuck in provisioning/error.
func (s *Service) Retry(ctx context.Context, actor Actor, id string) (*jobs.Job, error) {
	w, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if w.Status != StatusProvisioning && w.Status != StatusError {
		return nil, apierror.Conflict("only provisioning or failed websites can be retried")
	}
	if active, _ := s.deps.Jobs.ActiveForWebsite(ctx, id); active {
		return nil, apierror.Conflict("a job is already running for this website")
	}
	payload := map[string]any{
		"document_root": w.DocumentRoot,
		"php_version":   w.PHPVersion,
		"aliases":       w.Aliases,
		"slug":          Slug(w.Domain),
	}
	job, err := s.deps.Jobs.Create(ctx, jobs.TypeProvisionWebsite, &w.ServerID, &id, payload)
	if err != nil {
		return nil, err
	}
	if _, err := s.deps.Pool.Exec(ctx,
		`UPDATE websites SET status = $2, updated_at = now() WHERE id = $1`, id, StatusProvisioning); err != nil {
		return nil, apierror.From(err)
	}
	s.audit(ctx, actor, audit.ActionWebsiteRetryRequested, id, w.Domain, map[string]any{"job_id": job.ID})
	return job, nil
}

// RequestDelete queues the delete job; destructive options are explicit.
// A website whose previous delete job failed (stuck in deleting) can be
// re-queued as long as no delete job is currently running.
func (s *Service) RequestDelete(ctx context.Context, actor Actor, id string, deleteFiles bool) (*jobs.Job, error) {
	w, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if active, _ := s.deps.Jobs.ActiveForWebsite(ctx, id); active {
		return nil, apierror.Conflict("a job is already running for this website")
	}
	payload := map[string]any{
		"slug":         Slug(w.Domain),
		"delete_files": deleteFiles,
		"php_version":  w.PHPVersion,
	}
	job, err := s.deps.Jobs.Create(ctx, jobs.TypeDeleteWebsite, &w.ServerID, &id, payload)
	if err != nil {
		return nil, err
	}
	if _, err := s.deps.Pool.Exec(ctx,
		`UPDATE websites SET status = $2, updated_at = now() WHERE id = $1`, id, StatusDeleting); err != nil {
		return nil, apierror.From(err)
	}
	s.audit(ctx, actor, audit.ActionWebsiteDeleteRequested, id, w.Domain, map[string]any{
		"delete_files": deleteFiles, "job_id": job.ID,
	})
	return job, nil
}

// ---------------------------------------------------------------------------
// Update (PHP runtime / PHP settings / aliases) via reconfigure job
// ---------------------------------------------------------------------------

var phpSizeRe = regexp.MustCompile(`^\d+[KMG]?$`)
var phpSecondsRe = regexp.MustCompile(`^\d{1,6}$`)

// allowedPHPSettings is the validated subset exposed in this phase.
var allowedPHPSettings = map[string]*regexp.Regexp{
	"memory_limit":        phpSizeRe,
	"upload_max_filesize": phpSizeRe,
	"post_max_size":       phpSizeRe,
	"max_execution_time":  phpSecondsRe,
	"max_input_time":      phpSecondsRe,
}

type UpdateInput struct {
	PHPVersion   *string
	PHPSettings  map[string]string
	AliasIDs     *[]string
}

// Update validates the patch and enqueues a reconfigure job.
func (s *Service) Update(ctx context.Context, actor Actor, id string, in UpdateInput) (*jobs.Job, error) {
	w, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if w.Status == StatusProvisioning || w.Status == StatusDeleting {
		return nil, apierror.Conflict("website is busy; wait for the current operation to finish")
	}

	phpVersion := w.PHPVersion
	if in.PHPVersion != nil {
		phpVersion = *in.PHPVersion
		if phpVersion != "" {
			ok, err := s.phpVersionAvailable(ctx, w.ServerID, phpVersion)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, apierror.BadRequest("PHP " + phpVersion + " is not installed on this server")
			}
		}
	}

	cfg, err := s.loadConfig(ctx, id)
	if err != nil {
		return nil, err
	}
	phpSettings := cfg.PHPSettings
	if in.PHPSettings != nil {
		phpSettings = map[string]string{}
		for k, v := range cfg.PHPSettings {
			phpSettings[k] = v
		}
		for k, v := range in.PHPSettings {
			re, ok := allowedPHPSettings[k]
			if !ok {
				return nil, apierror.BadRequest("unsupported PHP setting: " + k)
			}
			if !re.MatchString(v) {
				return nil, apierror.BadRequest("invalid value for " + k)
			}
			if v == "0" || v == "0K" || v == "0M" || v == "0G" {
				return nil, apierror.BadRequest(k + " cannot be zero")
			}
			phpSettings[k] = v
		}
	}

	aliasIDs := []string{}
	if in.AliasIDs != nil {
		for _, aliasID := range *in.AliasIDs {
			ad, err := s.deps.Domains.Get(ctx, aliasID)
			if err != nil {
				return nil, err
			}
			if ad.ServerID != w.ServerID {
				return nil, apierror.BadRequest("alias " + ad.Domain + " belongs to a different server")
			}
			if ad.WebsiteID != nil && *ad.WebsiteID != id {
				return nil, apierror.Conflict("alias " + ad.Domain + " is attached to another website")
			}
			aliasIDs = append(aliasIDs, aliasID)
		}
	}

	payload := map[string]any{
		"php_version":   phpVersion,
		"php_settings":  phpSettings,
		"alias_ids":     aliasIDs,
		"document_root": w.DocumentRoot,
		"slug":          Slug(w.Domain),
		"was_active":    w.Status == StatusActive,
	}
	job, err := s.deps.Jobs.Create(ctx, jobs.TypeReconfigureWebsite, &w.ServerID, &id, payload)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, actor, audit.ActionWebsiteUpdated, id, w.Domain, map[string]any{"job_id": job.ID})
	return job, nil
}

// ---------------------------------------------------------------------------
// Resource limits (Phase 9)
// ---------------------------------------------------------------------------

// UpdateLimits persists per-site CPU/memory ceilings and applies them on the
// agent (cgroup v2 on Linux, Job Object on Windows). Zero = unlimited.
func (s *Service) UpdateLimits(ctx context.Context, actor Actor, id string, cpuPct, memMB int) error {
	w, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if cpuPct < 0 || cpuPct > 100 {
		return apierror.BadRequest("cpu_limit_pct must be 0 (unlimited) or 1..100")
	}
	if memMB < 0 {
		return apierror.BadRequest("memory_limit_mb cannot be negative")
	}

	if _, err := s.deps.Pool.Exec(ctx, `
		UPDATE website_runtime_config
		SET cpu_limit_pct = $2, memory_limit_mb = $3, updated_at = now()
		WHERE website_id = $1`, id, cpuPct, memMB); err != nil {
		return apierror.From(err)
	}

	// Apply on the agent if the site is managed and the server is reachable.
	if w.ServerOS != "windows" {
		if target, terr := s.deps.Servers.OpsTarget(ctx, w.ServerID); terr == nil && target.Manageable {
			if aerr := s.deps.Agent.SetLimits(ctx, target.AgentURL, target.OpsToken,
				Slug(w.Domain), cpuPct, memMB); aerr != nil {
				// Persist anyway; surface the agent failure to the operator.
				s.deps.Log.Warn("limits persisted but not applied on agent", "site", w.Domain, "err", aerr)
				return apierror.New(502, "LIMITS_AGENT_FAILED",
					"limits saved but the agent could not apply them: "+aerr.Error())
			}
		}
	}
	s.audit(ctx, actor, audit.ActionWebsiteLimitsUpdated, id, w.Domain,
		map[string]any{"cpu_limit_pct": cpuPct, "memory_limit_mb": memMB})
	return nil
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

func (s *Service) Logs(ctx context.Context, id, logType string, maxBytes int64) (*agentclient.LogsResult, error) {
	w, target, _, _, err := s.loadForOps(ctx, id)
	if err != nil {
		return nil, err
	}
	access, errLog, ok := LogsForSite(w.DocumentRoot)
	if !ok {
		return nil, apierror.BadRequest("log locations are not available for this website")
	}
	path := access
	if logType == "error" {
		path = errLog
	} else if logType != "access" {
		return nil, apierror.BadRequest("log type must be access or error")
	}
	return s.deps.Agent.LogsRead(ctx, target.AgentURL, target.OpsToken, path, maxBytes)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// opsTargetFor resolves the management channel for a server row.
func (s *Service) loadForOps(ctx context.Context, websiteID string) (*Website, *servers.OpsTarget, PathPlan, *runtimeConfig, error) {
	w, err := s.Get(ctx, websiteID)
	if err != nil {
		return nil, nil, PathPlan{}, nil, err
	}
	target, err := s.deps.Servers.OpsTarget(ctx, w.ServerID)
	if err != nil {
		return nil, nil, PathPlan{}, nil, err
	}
	if !target.Manageable {
		return nil, nil, PathPlan{}, nil, apierror.New(409, "SERVER_NOT_MANAGEABLE",
			"server has no management channel; re-enroll the agent with ops support")
	}
	sitesRoot := s.sitesRoot(ctx, target.OS)
	plan, err := ResolveSitePaths(sitesRoot, w.Domain, target.OS)
	if err != nil {
		return nil, nil, PathPlan{}, nil, apierror.BadRequest(err.Error())
	}
	cfg, err := s.loadConfig(ctx, websiteID)
	if err != nil {
		return nil, nil, PathPlan{}, nil, err
	}
	return w, target, plan, cfg, nil
}

type runtimeConfig struct {
	PHPSettings  map[string]string
	PHPAddress   string
	NginxName    string
}

func (s *Service) loadConfig(ctx context.Context, websiteID string) (*runtimeConfig, error) {
	var raw string
	var rc runtimeConfig
	err := s.deps.Pool.QueryRow(ctx, `
		SELECT COALESCE(php_settings,'{}'::jsonb)::text, COALESCE(php_address,''), COALESCE(nginx_config_name,'')
		FROM website_runtime_config WHERE website_id = $1`, websiteID).
		Scan(&raw, &rc.PHPAddress, &rc.NginxName)
	if errors.Is(err, pgx.ErrNoRows) {
		return &runtimeConfig{PHPSettings: map[string]string{}}, nil
	}
	if err != nil {
		return nil, apierror.From(err)
	}
	rc.PHPSettings = map[string]string{}
	_ = json.Unmarshal([]byte(raw), &rc.PHPSettings)
	return &rc, nil
}

func (s *Service) sitesRoot(ctx context.Context, agentOS string) string {
	linux := s.deps.Settings.String(ctx, KeySitesRootLinux, DefaultSitesRootLinux)
	windows := s.deps.Settings.String(ctx, KeySitesRootWindows, DefaultSitesRootWindows)
	return SitesRoot(agentOS, linux, windows)
}

// phpVersionAvailable consults the last probed capabilities.
func (s *Service) phpVersionAvailable(ctx context.Context, serverID, version string) (bool, error) {
	caps, err := s.deps.Servers.Capabilities(ctx, serverID)
	if err != nil {
		return false, err
	}
	raw, _ := json.Marshal(caps)
	var c Capabilities
	_ = json.Unmarshal(raw, &c)
	if len(c.PHP) == 0 {
		return false, nil
	}
	for _, p := range c.PHP {
		if p.Version == version && p.Status != "unavailable" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) renderConfig(w *Website, plan PathPlan, cfg *runtimeConfig) string {
	return Generate(SiteConfig{
		Domain:       w.Domain,
		Aliases:      w.Aliases,
		IncludeWWW:   true,
		DocumentRoot: w.DocumentRoot,
		PHPVersion:   w.PHPVersion,
		FastCGIAddr:  cfg.PHPAddress,
		AccessLog:    plan.AccessLog,
		ErrorLog:     plan.ErrorLog,
	})
}

func (s *Service) audit(ctx context.Context, actor Actor, action, resourceID, resourceLabel string, meta map[string]any) {
	entry := audit.Entry{
		ActorType: "user", Label: actor.Label,
		Action: action, Resource: "website", ResourceID: resourceID,
		Metadata: meta,
	}
	if actor.Label == "" {
		entry.ActorType = "system"
		entry.Label = "provisioning engine"
	}
	s.deps.Audit.Log(ctx, entry)
}

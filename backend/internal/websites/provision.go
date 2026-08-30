package websites

import (
	"context"
	"fmt"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// Capabilities is the probed feature matrix stored per server.
type Capabilities struct {
	ProbedAt  string       `json:"probed_at,omitempty"`
	Reachable bool         `json:"reachable"`
	Error     string       `json:"error,omitempty"`
	Nginx     *agentclient.NginxStatus `json:"nginx,omitempty"`
	PHP       []agentclient.PHPVersion `json:"php,omitempty"`
	Provisioning bool      `json:"provisioning"` // nginx + agent channel healthy
	LogAccess    bool      `json:"log_access"`
}

// RegisterHandlers wires the three job types to the runner. Handlers assume
// the payload shape produced by Create/Update/RequestDelete and are written
// to be safe on retry: every step is idempotent or leaves prior state intact.
func (s *Service) RegisterHandlers(runner *jobs.Runner) {
	runner.Register(jobs.TypeProvisionWebsite, s.handleProvision)
	runner.Register(jobs.TypeReconfigureWebsite, s.handleReconfigure)
	runner.Register(jobs.TypeDeleteWebsite, s.handleDelete)
	runner.Register(jobs.TypeIssueSSL, s.handleIssueSSL)
}

// handleProvision: validate → directories → site user → PHP → nginx → reload → default page.
func (s *Service) handleProvision(ctx context.Context, job *jobs.Job, progress jobs.ProgressFunc) error {
	w, target, plan, cfg, err := s.loadForOps(ctx, deref(job.WebsiteID))
	if err != nil {
		return err
	}
	slug := payloadString(job.Payload, "slug", Slug(w.Domain))
	docRoot := payloadString(job.Payload, "document_root", w.DocumentRoot)
	phpVersion := payloadString(job.Payload, "php_version", w.PHPVersion)
	aliases := payloadStrings(job.Payload, "aliases")
	osUser := payloadString(job.Payload, "os_user", w.OSUser)
	cpuPct := payloadInt(job.Payload, "cpu_limit_pct", 0)
	memMB := payloadInt(job.Payload, "memory_limit_mb", 0)

	progress(5, "Validating website configuration")
	if w.Status == StatusDeleting {
		return fmt.Errorf("website is scheduled for deletion")
	}

	progress(15, "Creating website directories")
	if err := s.deps.Agent.FSMkdir(ctx, target.AgentURL, target.OpsToken,
		[]string{plan.PublicDir, plan.LogsDir, plan.TmpDir, plan.PrivateDir}); err != nil {
		return err
	}

	// Per-site OS user (cPanel-style isolation). The account owns the site
	// files and runs the PHP-FPM pool; other sites' accounts cannot read it.
	// On Windows the account concept does not apply — ACLs are inherited.
	if target.OS != "windows" && osUser != "" && osUser != SiteUser {
		progress(30, "Creating isolated account "+osUser)
		if err := s.deps.Agent.FSUser(ctx, target.AgentURL, target.OpsToken, osUser); err != nil {
			// A missing site user must not block provisioning; nginx and
			// PHP still run, isolation is weaker (reported honestly).
			s.deps.Log.Warn("per-site account setup failed", "account", osUser, "err", err)
		} else {
			_ = s.deps.Agent.FSChown(ctx, target.AgentURL, target.OpsToken, plan.SiteRoot, osUser)
		}
	} else if target.OS != "windows" {
		if err := s.deps.Agent.FSUser(ctx, target.AgentURL, target.OpsToken, SiteUser); err != nil {
			s.deps.Log.Warn("site user setup failed; continuing without it", "err", err)
		}
	}

	fastcgi := ""
	if phpVersion != "" {
		progress(45, "Configuring PHP "+phpVersion)
		pool, perr := s.deps.Agent.PHPPool(ctx, target.AgentURL, target.OpsToken, agentclient.PHPPoolRequest{
			SiteSlug: slug,
			Version:  phpVersion,
			User:     osUser, // pool runs as the isolated account
			Settings: mergedSettings(cfg.PHPSettings, job.Payload["php_settings"]),
		})
		if perr != nil {
			return perr
		}
		fastcgi = pool.Address
		if err := s.savePHPAddress(ctx, w.ID, fastcgi); err != nil {
			return err
		}
	}

	progress(55, "Applying resource limits")
	if cpuPct > 0 || memMB > 0 {
		if err := s.deps.Agent.SetLimits(ctx, target.AgentURL, target.OpsToken, slug, cpuPct, memMB); err != nil {
			s.deps.Log.Warn("resource limits could not be applied", "site", slug, "err", err)
		}
	}

	progress(60, "Creating Nginx configuration")
	content := Generate(SiteConfig{
		Domain:       w.Domain,
		Aliases:      aliases,
		IncludeWWW:   true,
		DocumentRoot: docRoot,
		PHPVersion:   phpVersion,
		FastCGIAddr:  fastcgi,
		AccessLog:    plan.AccessLog,
		ErrorLog:     plan.ErrorLog,
	})
	res, err := s.deps.Agent.NginxDeploySite(ctx, target.AgentURL, target.OpsToken,
		agentclient.DeploySiteRequest{Name: slug, Content: content, Enable: true})
	if err != nil {
		return err
	}
	if !res.Deployed {
		msg := "nginx rejected the configuration"
		if res.ValidationOut != "" {
			msg += ": " + res.ValidationOut
		}
		return fmt.Errorf("%s", msg)
	}
	if err := s.saveNginxName(ctx, w.ID, slug); err != nil {
		return err
	}

	progress(75, "Reloading Nginx")
	if err := s.deps.Agent.NginxReload(ctx, target.AgentURL, target.OpsToken); err != nil {
		return err
	}

	progress(88, "Creating default website page")
	page := DefaultIndexPage(w.Domain, phpVersion)
	if err := s.deps.Agent.FSWrite(ctx, target.AgentURL, target.OpsToken,
		joinPath(target.OS, docRoot, "index.php"), []byte(page)); err != nil {
		return err
	}

	progress(96, "Activating website")
	if _, err := s.deps.Pool.Exec(ctx, `
		UPDATE websites SET status = $2, php_version = $3, document_root = $4, updated_at = now()
		WHERE id = $1`, w.ID, StatusActive, phpVersion, docRoot); err != nil {
		return err
	}
	progress(100, "Website activated")
	return nil
}

// handleReconfigure: PHP pool → nginx redeploy → reload; keeps enabled state.
func (s *Service) handleReconfigure(ctx context.Context, job *jobs.Job, progress jobs.ProgressFunc) error {
	w, target, plan, cfg, err := s.loadForOps(ctx, deref(job.WebsiteID))
	if err != nil {
		return err
	}
	phpVersion := payloadString(job.Payload, "php_version", w.PHPVersion)
	wasActive, _ := job.Payload["was_active"].(bool)
	slug := payloadString(job.Payload, "slug", Slug(w.Domain))

	progress(10, "Configuring PHP "+orDefault(phpVersion, "(static)"))
	fastcgi := ""
	if phpVersion != "" {
		pool, perr := s.deps.Agent.PHPPool(ctx, target.AgentURL, target.OpsToken, agentclient.PHPPoolRequest{
			SiteSlug: slug,
			Version:  phpVersion,
			Settings: mergedSettings(cfg.PHPSettings, job.Payload["php_settings"]),
		})
		if perr != nil {
			return perr
		}
		fastcgi = pool.Address
		if err := s.savePHPAddress(ctx, w.ID, fastcgi); err != nil {
			return err
		}
	} else if cfg.PHPAddress != "" {
		// PHP was removed from the site: tear the old pool down.
		if _, terr := s.deps.Agent.PHPPool(ctx, target.AgentURL, target.OpsToken, agentclient.PHPPoolRequest{
			SiteSlug: slug, Version: payloadString(job.Payload, "previous_php", ""), Remove: true,
		}); terr != nil {
			s.deps.Log.Warn("old PHP pool teardown failed", "err", terr)
		}
		if err := s.savePHPAddress(ctx, w.ID, ""); err != nil {
			return err
		}
	}

	progress(45, "Updating Nginx configuration")
	aliases := []string{}
	if ids, ok := job.Payload["alias_ids"].([]any); ok {
		for _, idv := range ids {
			if id, ok := idv.(string); ok {
				if d, err := s.deps.Domains.Get(ctx, id); err == nil {
					aliases = append(aliases, d.Domain)
				}
			}
		}
	}
	// Persist the alias set before rendering so future deploys match.
	if err := s.replaceAliases(ctx, w.ID, idsOf(job.Payload["alias_ids"])); err != nil {
		return err
	}

	content := Generate(SiteConfig{
		Domain:       w.Domain,
		Aliases:      aliases,
		IncludeWWW:   true,
		DocumentRoot: payloadString(job.Payload, "document_root", w.DocumentRoot),
		PHPVersion:   phpVersion,
		FastCGIAddr:  fastcgi,
		AccessLog:    plan.AccessLog,
		ErrorLog:     plan.ErrorLog,
	})
	res, err := s.deps.Agent.NginxDeploySite(ctx, target.AgentURL, target.OpsToken,
		agentclient.DeploySiteRequest{Name: slug, Content: content, Enable: wasActive})
	if err != nil {
		return err
	}
	if !res.Deployed {
		msg := "nginx rejected the configuration"
		if res.ValidationOut != "" {
			msg += ": " + res.ValidationOut
		}
		return fmt.Errorf("%s", msg)
	}

	progress(75, "Reloading Nginx")
	if err := s.deps.Agent.NginxReload(ctx, target.AgentURL, target.OpsToken); err != nil {
		return err
	}

	progress(95, "Applying changes")
	next := StatusDisabled
	if wasActive {
		next = StatusActive
	}
	if _, err := s.deps.Pool.Exec(ctx, `
		UPDATE websites SET status = $2, php_version = $3, updated_at = now()
		WHERE id = $1`, w.ID, next, phpVersion); err != nil {
		return err
	}
	return nil
}

// handleDelete: nginx removal → reload → PHP teardown → optional files → DB cleanup.
func (s *Service) handleDelete(ctx context.Context, job *jobs.Job, progress jobs.ProgressFunc) error {
	websiteID := deref(job.WebsiteID)
	w, target, plan, cfg, err := s.loadForOps(ctx, websiteID)
	if err != nil {
		// The row may already be gone (retry after partial completion).
		if _, gerr := s.Get(ctx, websiteID); gerr != nil {
			return nil
		}
		return err
	}
	slug := payloadString(job.Payload, "slug", Slug(w.Domain))

	progress(10, "Removing Nginx configuration")
	if err := s.deps.Agent.NginxRemoveSite(ctx, target.AgentURL, target.OpsToken, slug); err != nil {
		s.deps.Log.Warn("nginx site removal failed; continuing cleanup", "err", err)
	}

	progress(30, "Reloading Nginx")
	if err := s.deps.Agent.NginxReload(ctx, target.AgentURL, target.OpsToken); err != nil {
		s.deps.Log.Warn("nginx reload after delete failed", "err", err)
	}

	progress(50, "Removing PHP pool")
	if w.PHPVersion != "" || cfg.PHPAddress != "" {
		if _, terr := s.deps.Agent.PHPPool(ctx, target.AgentURL, target.OpsToken, agentclient.PHPPoolRequest{
			SiteSlug: slug, Version: w.PHPVersion, Remove: true,
		}); terr != nil {
			s.deps.Log.Warn("php pool teardown failed", "err", terr)
		}
	}

	if deleteFiles, _ := job.Payload["delete_files"].(bool); deleteFiles {
		progress(70, "Deleting website files")
		if err := s.deps.Agent.FSRemove(ctx, target.AgentURL, target.OpsToken, plan.SiteRoot); err != nil {
			return fmt.Errorf("website files could not be removed: %w", err)
		}
	} else {
		progress(70, "Preserving website files")
	}

	progress(90, "Finalizing")
	if err := s.deps.Domains.DetachAllFromWebsite(ctx, websiteID); err != nil {
		return err
	}
	if _, err := s.deps.Pool.Exec(ctx, `DELETE FROM websites WHERE id = $1`, websiteID); err != nil {
		return err
	}
	progress(100, "Website deleted")
	return nil
}

// ---------------------------------------------------------------------------
// Default page
// ---------------------------------------------------------------------------

// DefaultIndexPage renders the welcome page. It deliberately exposes only the
// domain, server family and PHP version — no IPs, paths, credentials or
// panel internals.
func DefaultIndexPage(domain, phpVersion string) string {
	runtime := "Static HTML"
	if phpVersion != "" {
		runtime = "PHP " + phpVersion
	}
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Welcome · ` + domain + `</title>
<style>
  body { font-family: system-ui, -apple-system, "Segoe UI", sans-serif; background: #f8fafc;
         color: #0f172a; display: grid; place-items: center; min-height: 100vh; margin: 0; }
  .card { background: #fff; border: 1px solid #e2e8f0; border-radius: 16px; padding: 48px;
          max-width: 480px; text-align: center; box-shadow: 0 1px 3px rgba(15,23,42,.08); }
  h1 { font-size: 1.5rem; margin: 0 0 8px; }
  p { color: #475569; font-size: .95rem; line-height: 1.6; margin: 4px 0; }
  .badge { display: inline-block; margin-top: 16px; padding: 4px 12px; border-radius: 999px;
           background: #eef2ff; color: #4338ca; font-size: .8rem; font-weight: 600; }
</style>
</head>
<body>
  <main class="card">
    <h1>Welcome to your new website</h1>
    <p>This page was placed here by EpicPanel when the site was provisioned.
       Replace it by uploading your own application into the web root.</p>
    <p><strong>` + domain + `</strong></p>
    <span class="badge">Runtime: ` + runtime + `</span>
  </main>
</body>
</html>
`
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func mergedSettings(stored map[string]string, payload any) map[string]string {
	out := map[string]string{}
	for k, v := range stored {
		out[k] = v
	}
	if m, ok := payload.(map[string]any); ok {
		for k, v := range m {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

func payloadString(payload map[string]any, key, def string) string {
	if v, ok := payload[key].(string); ok && v != "" {
		return v
	}
	return def
}

func payloadStrings(payload map[string]any, key string) []string {
	raw, ok := payload[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func payloadInt(payload map[string]any, key string, def int) int {
	if v, ok := payload[key].(float64); ok {
		return int(v)
	}
	return def
}

func idsOf(payload any) []string {
	raw, ok := payload.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// joinPath joins with the agent OS separator.
func joinPath(agentOS string, elems ...string) string {
	sep := "/"
	if agentOS == "windows" {
		sep = "\\"
	}
	out := ""
	for i, e := range elems {
		if i > 0 {
			out += sep
		}
		out += e
	}
	return out
}

func (s *Service) savePHPAddress(ctx context.Context, websiteID, addr string) error {
	_, err := s.deps.Pool.Exec(ctx,
		`UPDATE website_runtime_config SET php_address = $2, updated_at = now() WHERE website_id = $1`,
		websiteID, addr)
	return err
}

func (s *Service) saveNginxName(ctx context.Context, websiteID, name string) error {
	_, err := s.deps.Pool.Exec(ctx,
		`UPDATE website_runtime_config SET nginx_config_name = $2, updated_at = now() WHERE website_id = $1`,
		websiteID, name)
	return err
}

// replaceAliases rewrites the alias attachment set for a website.
func (s *Service) replaceAliases(ctx context.Context, websiteID string, aliasIDs []string) error {
	if _, err := s.deps.Pool.Exec(ctx,
		`UPDATE domains SET website_id = NULL, updated_at = now() WHERE website_id = $1`, websiteID); err != nil {
		return err
	}
	for _, id := range aliasIDs {
		if _, err := s.deps.Pool.Exec(ctx,
			`UPDATE domains SET website_id = $2, updated_at = now() WHERE id = $1 AND website_id IS NULL`,
			id, websiteID); err != nil {
			return err
		}
	}
	return nil
}

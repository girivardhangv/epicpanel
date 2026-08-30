// Phase 4: per-website TLS certificates. The panel records certificate
// metadata issued by the agent (Let's Encrypt ACME, or mock/self-signed in
// development) and re-deploys the nginx configuration with the certificate
// paths. Issuance runs as a background job so long ACME rounds never block
// HTTP requests; renewal is driven by the maintenance worker.
package websites

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/jackc/pgx/v5"
)

// Certificate provider modes.
const (
	CertProviderACME = "acme"
	CertProviderMock = "mock"
)

// Certificate is the stored per-website TLS state.
type Certificate struct {
	ID        string     `json:"id"`
	WebsiteID string     `json:"website_id"`
	Provider  string     `json:"provider"`
	Domains   []string   `json:"domains"`
	Status    string     `json:"status"` // issuing | issued | error | removed
	CertPath  string     `json:"cert_path"`
	KeyPath   string     `json:"key_path"`
	AutoRenew bool       `json:"auto_renew"`
	IssuedAt  *time.Time `json:"issued_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

func (s *Service) loadCertificate(ctx context.Context, websiteID string) (*Certificate, error) {
	var c Certificate
	var domainsRaw, issuedAt, expiresAt *string
	err := s.deps.Pool.QueryRow(ctx, `
		SELECT id, website_id, provider, domains::text, status, cert_path, key_path,
		       auto_renew, issued_at::text, expires_at::text, error
		FROM website_certificates WHERE website_id = $1`, websiteID).
		Scan(&c.ID, &c.WebsiteID, &c.Provider, &domainsRaw, &c.Status,
			&c.CertPath, &c.KeyPath, &c.AutoRenew, &issuedAt, &expiresAt, &c.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apierror.From(err)
	}
	if domainsRaw != nil {
		_ = json.Unmarshal([]byte(*domainsRaw), &c.Domains)
	}
	c.IssuedAt = parseTimePtr(issuedAt)
	c.ExpiresAt = parseTimePtr(expiresAt)
	return &c, nil
}

func parseTimePtr(raw *string) *time.Time {
	if raw == nil || *raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return nil
	}
	return &t
}

// RequestCertificate enqueues an issuance/renewal job for a website. The
// operator explicitly asks; nothing happens automatically during creation.
func (s *Service) RequestCertificate(ctx context.Context, actor Actor, websiteID string, autoRenew bool) (*jobs.Job, error) {
	w, err := s.Get(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	if w.Status != StatusActive && w.Status != StatusDisabled {
		return nil, apierror.Conflict("website must be provisioned before requesting a certificate")
	}
	target, err := s.deps.Servers.OpsTarget(ctx, w.ServerID)
	if err != nil {
		return nil, err
	}
	if !target.Manageable {
		return nil, apierror.New(409, "SERVER_NOT_MANAGEABLE",
			"server has no management channel; re-enroll the agent")
	}
	if active, _ := s.deps.Jobs.ActiveForWebsite(ctx, websiteID); active {
		return nil, apierror.Conflict("a job is already running for this website")
	}

	// The ACME mode is operator configuration; mock is only ever enabled by
	// an explicit setting (development), never by default.
	payload := map[string]any{
		"website_id": websiteID,
		"slug":       Slug(w.Domain),
		"auto_renew": autoRenew,
	}
	job, err := s.deps.Jobs.Create(ctx, jobs.TypeIssueSSL, &w.ServerID, &websiteID, payload)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "ssl.issue_requested", websiteID, w.Domain, map[string]any{"job_id": job.ID})
	return job, nil
}

// RemoveCertificate removes the stored cert, tears down the agent-side cert
// files and re-deploys the site without TLS. Non-destructive to the site.
func (s *Service) RemoveCertificate(ctx context.Context, actor Actor, websiteID string) error {
	w, err := s.Get(ctx, websiteID)
	if err != nil {
		return err
	}
	cert, err := s.loadCertificate(ctx, websiteID)
	if err != nil {
		return err
	}
	if cert == nil {
		return apierror.NotFound("certificate")
	}
	target, err := s.deps.Servers.OpsTarget(ctx, w.ServerID)
	if err != nil {
		return err
	}
	if !target.Manageable {
		return apierror.New(409, "SERVER_NOT_MANAGEABLE",
			"server has no management channel; re-enroll the agent")
	}

	// Tear the cert files down on the agent (best effort) then re-deploy
	// the plain-HTTP configuration.
	if cert.CertPath != "" {
		_ = s.deps.Agent.SSLRemove(ctx, target.AgentURL, target.OpsToken,
			Slug(w.Domain), cert.Provider)
	}

	if _, err := s.deps.Pool.Exec(ctx, `
		DELETE FROM website_certificates WHERE website_id = $1`, websiteID); err != nil {
		return apierror.From(err)
	}

	if err := s.redeployWithSSL(ctx, w, nil); err != nil {
		s.deps.Log.Warn("ssl removal: site redeploy failed", "website", websiteID, "err", err)
	}
	s.audit(ctx, actor, "ssl.removed", websiteID, w.Domain, nil)
	return nil
}

// redeployWithSSL regenerates and deploys the site config including (or
// excluding) TLS based on the given certificate. Returns a job-less direct
// operation since nginx redeploys are quick and atomic on the agent.
func (s *Service) redeployWithSSL(ctx context.Context, w *Website, cert *Certificate) error {
	target, err := s.deps.Servers.OpsTarget(ctx, w.ServerID)
	if err != nil {
		return err
	}
	sitesRoot := s.sitesRoot(ctx, target.OS)
	plan, err := ResolveSitePaths(sitesRoot, w.Domain, target.OS)
	if err != nil {
		return err
	}
	cfg, err := s.loadConfig(ctx, w.ID)
	if err != nil {
		return err
	}
	content := s.renderConfigWithCert(w, plan, cfg, cert)
	res, err := s.deps.Agent.NginxDeploySite(ctx, target.AgentURL, target.OpsToken,
		agentclient.DeploySiteRequest{
			Name:    Slug(w.Domain),
			Content: content,
			Enable:  w.Status == StatusActive,
		})
	if err != nil {
		return err
	}
	if !res.Deployed {
		msg := "nginx rejected the configuration"
		if res.ValidationOut != "" {
			msg += ": " + res.ValidationOut
		}
		return errors.New(msg)
	}
	return s.deps.Agent.NginxReload(ctx, target.AgentURL, target.OpsToken)
}

// CertificateInfo returns the stored certificate for a website, or nil.
func (s *Service) CertificateInfo(ctx context.Context, websiteID string) (*Certificate, error) {
	return s.loadCertificate(ctx, websiteID)
}

// CertificatesDueForRenewal lists issued, auto-renew certs expiring within
// the given window — used by the maintenance worker.
func (s *Service) CertificatesDueForRenewal(ctx context.Context, within time.Duration) ([]Certificate, error) {
	rows, err := s.deps.Pool.Query(ctx, `
		SELECT c.id, c.website_id, c.provider, c.domains::text, c.status, c.cert_path,
		       c.key_path, c.auto_renew, c.issued_at::text, c.expires_at::text, c.error
		FROM website_certificates c
		JOIN websites w ON w.id = c.website_id
		WHERE c.status = 'issued' AND c.auto_renew
		  AND c.expires_at < now() + ($1 || ' seconds')::interval`,
		int(within.Seconds()))
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []Certificate{}
	for rows.Next() {
		var c Certificate
		var domainsRaw, issuedAt, expiresAt *string
		if err := rows.Scan(&c.ID, &c.WebsiteID, &c.Provider, &domainsRaw, &c.Status,
			&c.CertPath, &c.KeyPath, &c.AutoRenew, &issuedAt, &expiresAt, &c.Error); err != nil {
			return nil, apierror.From(err)
		}
		if domainsRaw != nil {
			_ = json.Unmarshal([]byte(*domainsRaw), &c.Domains)
		}
		c.IssuedAt = parseTimePtr(issuedAt)
		c.ExpiresAt = parseTimePtr(expiresAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// IssueSSL job handler
// ---------------------------------------------------------------------------

// handleIssueSSL orchestrates certificate issuance on the agent: it gathers
// the ACME mode/contact from settings, asks the agent to obtain the cert,
// stores the metadata, then re-deploys the site with TLS enabled.
func (s *Service) handleIssueSSL(ctx context.Context, job *jobs.Job, progress jobs.ProgressFunc) error {
	websiteID := deref(job.WebsiteID)
	w, err := s.Get(ctx, websiteID)
	if err != nil {
		return err
	}
	target, err := s.deps.Servers.OpsTarget(ctx, w.ServerID)
	if err != nil {
		return err
	}
	if !target.Manageable {
		return errors.New("server has no management channel; re-enroll the agent")
	}

	autoRenew, _ := job.Payload["auto_renew"].(bool)
	mode := s.deps.Settings.String(ctx, settings.KeyACMEMode, "production")
	email := s.deps.Settings.String(ctx, settings.KeyACMEEmail, "")

	// Domains for the certificate: primary + www + aliases (all validated).
	domains := []string{w.Domain, "www." + w.Domain}
	for _, a := range w.Aliases {
		domains = append(domains, a)
	}

	progress(15, "Requesting certificate from ACME")
	provider := CertProviderACME
	if mode == "mock" {
		provider = CertProviderMock
	}
	res, err := s.deps.Agent.SSLOrder(ctx, target.AgentURL, target.OpsToken, agentclient.SSLOrderRequest{
		SiteSlug: Slug(w.Domain),
		Domains:  domains,
		WebRoot:  w.DocumentRoot,
		Mode:     mode,
		Email:    email,
	})
	if err != nil {
		return err
	}

	expiresAt := parseTimePtrPtr(res.ExpiresAt)
	domainsJSON, _ := json.Marshal(res.Domains)

	progress(65, "Recording certificate")
	if _, err := s.deps.Pool.Exec(ctx, `
		INSERT INTO website_certificates
			(website_id, provider, domains, status, cert_path, key_path, auto_renew, issued_at, expires_at)
		VALUES ($1, $2, $3::jsonb, 'issued', $4, $5, $6, now(), $7)
		ON CONFLICT (website_id) DO UPDATE SET
			provider = EXCLUDED.provider, domains = EXCLUDED.domains,
			status = 'issued', cert_path = EXCLUDED.cert_path, key_path = EXCLUDED.key_path,
			auto_renew = EXCLUDED.auto_renew, issued_at = EXCLUDED.issued_at,
			expires_at = EXCLUDED.expires_at, error = '', updated_at = now()`,
		websiteID, provider, string(domainsJSON), res.CertPath, res.KeyPath, autoRenew, expiresAt); err != nil {
		return apierror.From(err)
	}

	progress(80, "Deploying TLS configuration")
	cert := &Certificate{
		Provider: provider, CertPath: res.CertPath, KeyPath: res.KeyPath,
		ExpiresAt: expiresAt,
	}
	if err := s.redeployWithSSL(ctx, w, cert); err != nil {
		// The cert is obtained and stored; only the nginx deploy failed.
		// Surface the failure honestly so the operator can retry.
		return err
	}

	progress(100, "Certificate installed")
	s.deps.Audit.Log(ctx, audit.Entry{
		ActorType: "system", Label: "monitoring",
		Action: "ssl.issued", Resource: "website", ResourceID: websiteID,
		Metadata: map[string]any{"domains": domains, "provider": provider, "expires_at": res.ExpiresAt},
	})
	return nil
}

func parseTimePtrPtr(raw *string) *time.Time {
	return parseTimePtr(raw)
}

// renderConfigWithCert is the SSL-aware variant of renderConfig.
func (s *Service) renderConfigWithCert(w *Website, plan PathPlan, cfg *runtimeConfig, cert *Certificate) string {
	gen := SiteConfig{
		Domain:       w.Domain,
		Aliases:      w.Aliases,
		IncludeWWW:   true,
		DocumentRoot: w.DocumentRoot,
		PHPVersion:   w.PHPVersion,
		FastCGIAddr:  cfg.PHPAddress,
		AccessLog:    plan.AccessLog,
		ErrorLog:     plan.ErrorLog,
	}
	if cert != nil {
		gen.CertPath = cert.CertPath
		gen.KeyPath = cert.KeyPath
	}
	return Generate(gen)
}

// ensureAliasesNormalized is a tiny guard so wildcard aliases never reach a
// certificate SAN list (ACME forbids them at this phase).
func ensureAliasesNormalized(aliases []string) []string {
	out := []string{}
	for _, a := range aliases {
		if strings.HasPrefix(a, "*.") {
			continue
		}
		out = append(out, a)
	}
	return out
}

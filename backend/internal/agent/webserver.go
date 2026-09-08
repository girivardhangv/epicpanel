package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// ============================================================================
// WEB SERVER ABSTRACTION — one provider interface across the three serving
// modes (nginx / nginx+apache / nginx+openlitespeed), a shared rewrite-rule
// core, and the redirect + suspension rendering contract.
// ============================================================================

// RedirectRule is one domain-level redirect (From -> To). Status must be one
// of 301/302/307/308; zero defaults to 301.
type RedirectRule struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Status int    `json:"status"`
}

var validRedirectStatuses = map[int]bool{301: true, 302: true, 307: true, 308: true}

// normalizeRedirects validates a redirect list, defaults status to 301 and
// drops invalid entries loudly. Every provider renderer calls this first so
// the guarantee holds regardless of the target web server.
func normalizeRedirects(websiteID string, rules []RedirectRule) []RedirectRule {
	var out []RedirectRule
	for _, r := range rules {
		if r.Status == 0 {
			r.Status = 301
		}
		if !validRedirectStatuses[r.Status] {
			slog.Warn("dropping redirect with unsupported status", "website", websiteID, "status", r.Status)
			continue
		}
		if !validDomainName(r.From) {
			slog.Warn("dropping redirect with invalid source domain", "website", websiteID, "from", r.From)
			continue
		}
		if !validRedirectTarget(r.To) {
			slog.Warn("dropping redirect with invalid target", "website", websiteID, "to", r.To)
			continue
		}
		out = append(out, r)
	}
	return out
}

// validRedirectTarget accepts absolute http(s) URLs or root-absolute paths and
// rejects anything that could break out of a directive (structure characters,
// quotes, comments, backrefs).
func validRedirectTarget(s string) bool {
	if s == "" || len(s) > 2000 {
		return false
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "/") {
		return false
	}
	return !strings.ContainsAny(s, " \t\r\n\"'`;{}\\$#")
}

// WebServerProvider is the common contract every web server backend
// implements: idempotent ensure of the serving config for one website, and
// idempotent removal by website id.
type WebServerProvider interface {
	Name() string
	Ensure(ctx context.Context, spec VhostSpec) error
	Remove(ctx context.Context, websiteID string) error
}

var (
	_ WebServerProvider = (*NginxProvider)(nil)
	_ WebServerProvider = (*ApacheProvider)(nil)
	_ WebServerProvider = (*OLSProvider)(nil)
)

// ============================================================================
// SHARED REWRITE-RULE CORE — one allowlist + smuggling filter for all three
// providers, each of which renders its own syntax from the validated lines.
// ============================================================================

var (
	// nginxRewriteTokens are the user-allowed nginx directives inside a
	// server block.
	nginxRewriteTokens = map[string]bool{
		"rewrite": true, "if": true, "return": true, "set": true, "break": true,
		"expires": true, "add_header": true, "try_files": true, "autoindex": true,
		"deny": true, "allow": true, "error_page": true, "client_max_body_size": true,
		"client_body_buffer_size": true, "index": true, "satisfy": true,
		"auth_basic": true, "auth_basic_user_file": true,
	}
	// apacheRewriteTokens are the user-allowed Apache directives inside the
	// managed <IfModule mod_rewrite.c> block.
	apacheRewriteTokens = map[string]bool{
		"rewrite": true, "rewritecond": true, "rewriterule": true, "redirect": true,
		"redirectmatch": true, "header": true, "setenv": true, "directoryindex": true,
		"errordocument": true, "options": true, "addtype": true,
	}
	// olsRewriteTokens: OpenLiteSpeed only maps nginx-style rewrite lines.
	olsRewriteTokens = map[string]bool{"rewrite": true}
)

// varRefRe mirrors the control-plane validator: only well-known nginx
// variable references survive rendering.
var varRefRe = regexp.MustCompile(`^(\d|uri|args|query_string|request_uri|host|scheme|request_method|remote_addr|https|server_port|http_[a-z0-9_]+)`)

// sanitizeRewriteLine validates ONE user-supplied rewrite line against the
// provider token set and the shared smuggling checks; returns the normalized
// line, or "" when the line must be dropped. Defense in depth: the control
// plane validates too, but the agent never trusts stored content.
func sanitizeRewriteLine(websiteID, raw string, tokens map[string]bool) string {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "}") {
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	if !tokens[strings.ToLower(fields[0])] || lineHasSmuggledDirective(line) {
		// Never render an unapproved line — drop it loudly.
		slog.Warn("dropping non-allowlisted line from rewrite rules", "website", websiteID, "directive", fields[0])
		return ""
	}
	return line
}

// lineHasSmuggledDirective rejects structure characters and directive-shaped
// tokens anywhere in the line — mirrors the control-plane validator (audit
// S6: the remainder of an allowlisted line previously rendered verbatim).
func lineHasSmuggledDirective(line string) bool {
	for _, tok := range []string{";", "{", "}", "`", "proxy_pass", "fastcgi_pass", "include", "root ", "alias ", "daemon", "error_log", "access_log", "listen ", "server_name", "location "} {
		if strings.Contains(line, tok) {
			return true
		}
	}
	for _, seg := range strings.Split(line, "$")[1:] {
		if seg == "" {
			return true
		}
		if !varRefRe.MatchString(seg) {
			return true
		}
	}
	return false
}

// ============================================================================
// NGINX PROVIDER
// ============================================================================

// enableSiteFile points the enabled symlink at the available file
// (idempotent; replaces stale or dangling links).
func enableSiteFile(enabled, target string) error {
	if link, err := os.Readlink(enabled); err == nil && link == target {
		return nil
	}
	_ = os.Remove(enabled)
	return os.Symlink(target, enabled)
}

// ============================================================================
// APACHE + OPENLITESPEED PROVIDERS — thin adapters over the site engines in
// webserver_ops.go. All filesystem paths are injectable for tests.
// ============================================================================

// ApacheProvider adapts Apache to the WebServerProvider contract. The site
// always runs as a PRIVATE backend on 127.0.0.1:<BackendPort>.
type ApacheProvider struct {
	Ex             *Executor
	SitesAvailable string // default /etc/apache2/sites-available
	SitesEnabled   string // default /etc/apache2/sites-enabled
	PortsConf      string // default /etc/apache2/ports.conf
	ServerNameConf string // default /etc/apache2/conf-available/epicpanel-servername.conf
	LogsBase       string // default /srv/epicpanel/websites
}

func (a *ApacheProvider) Name() string { return "apache" }

func (a *ApacheProvider) sitesAvailableDir() string {
	if a.SitesAvailable != "" {
		return a.SitesAvailable
	}
	return apacheSitesAvail
}

func (a *ApacheProvider) sitesEnabledDir() string {
	if a.SitesEnabled != "" {
		return a.SitesEnabled
	}
	return apacheSitesEnabled
}

func (a *ApacheProvider) portsConfPath() string {
	if a.PortsConf != "" {
		return a.PortsConf
	}
	return "/etc/apache2/ports.conf"
}

func (a *ApacheProvider) serverNameConfPath() string {
	if a.ServerNameConf != "" {
		return a.ServerNameConf
	}
	return "/etc/apache2/conf-available/epicpanel-servername.conf"
}

func (a *ApacheProvider) logsBaseDir() string {
	if a.LogsBase != "" {
		return a.LogsBase
	}
	return "/srv/epicpanel/websites"
}

// Ensure provisions the Apache vhost on the spec's backend port.
func (a *ApacheProvider) Ensure(ctx context.Context, spec VhostSpec) error {
	if spec.BackendPort <= 0 {
		return fmt.Errorf("apache backend port not allocated")
	}
	return a.ensureSite(ctx, spec, spec.BackendPort)
}

// Remove disables and deletes the site's Apache vhost (idempotent).
func (a *ApacheProvider) Remove(ctx context.Context, websiteID string) error {
	a.removeSite(ctx, websiteID)
	return nil
}

// OLSProvider adapts OpenLiteSpeed to the WebServerProvider contract.
type OLSProvider struct {
	Ex          *Executor
	VhostDir    string // default /usr/local/lsws/conf/vhosts
	HTTPDConfig string // default /usr/local/lsws/conf/httpd_config.conf
	LogsBase    string // default /srv/epicpanel/websites
	LSWSCtrl    string // default /usr/local/lsws/bin/lswsctrl
}

func (o *OLSProvider) Name() string { return "openlitespeed" }

func (o *OLSProvider) vhostDirPath() string {
	if o.VhostDir != "" {
		return o.VhostDir
	}
	return olsVhostDir
}

func (o *OLSProvider) httpdConfigPath() string {
	if o.HTTPDConfig != "" {
		return o.HTTPDConfig
	}
	return "/usr/local/lsws/conf/httpd_config.conf"
}

func (o *OLSProvider) logsBaseDir() string {
	if o.LogsBase != "" {
		return o.LogsBase
	}
	return "/srv/epicpanel/websites"
}

func (o *OLSProvider) lswsctrlPath() string {
	if o.LSWSCtrl != "" {
		return o.LSWSCtrl
	}
	return "/usr/local/lsws/bin/lswsctrl"
}

// Ensure provisions the OLS vhost + listener on the spec's backend port.
func (o *OLSProvider) Ensure(ctx context.Context, spec VhostSpec) error {
	if spec.BackendPort <= 0 {
		return fmt.Errorf("openlitespeed backend port not allocated")
	}
	return o.ensureSite(ctx, spec, spec.BackendPort)
}

// Remove unregisters the vhost + listener (idempotent).
func (o *OLSProvider) Remove(ctx context.Context, websiteID string) error {
	o.removeSite(ctx, websiteID)
	return nil
}

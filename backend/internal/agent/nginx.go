package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/agent/pages"
)

// VhostSpec describes the serving configuration for one website across all
// its domains (primary + aliases), with per-domain SSL.
type VhostSpec struct {
	WebsiteID    string
	UnixUser     string
	DocumentRoot string
	FpmSocket    string // empty = static only
	// RewriteRules is an optional user-provided nginx directive snippet
	// (rewrite rules, extra headers…). Allowlist-validated before rendering.
	RewriteRules string
	// ProxyPass, when set (e.g. "http://127.0.0.1:6123"), makes nginx the
	// edge: dynamic traffic reverse-proxies to the backend web server
	// (Apache/OpenLiteSpeed) instead of serving files directly.
	ProxyPass string
	// BackendPort is the private loopback port of the Apache/OLS backend
	// when this spec is rendered for a backend provider (0 for pure nginx).
	BackendPort int
	// Redirects are domain-level redirects rendered per provider.
	Redirects []RedirectRule
	// Suspended serves a plain 503 stub for every domain (no PHP, no proxy).
	Suspended bool
	// QuotaExceeded serves a 509 stub when bandwidth is exceeded.
	QuotaExceeded bool
	// Terminated serves a 410 stub (terminate lifecycle; distinct from
	// suspension — the site does not come back without an explicit purge).
	Terminated bool
	// StubPage overrides the stub file for reason-aware suspension pages
	// ("" = suspended.html). "bandwidth_exhausted.html" is served from the
	// PER-SITE pages dir (templated with StubVars); everything else comes
	// from the shared default_pages dir.
	StubPage string
	// StubVars fills the templated stub page (bandwidth used/limit/resets).
	StubVars map[string]string
	// Path overrides for tests (empty = production locations).
	AcmeWebroot string
	LogsBase    string
	// Domains is primary-first; per-domain SSL modes apply.
	Domains []DomainSpec
}

type DomainSpec struct {
	Domain        string
	SSLMode       string // none | selfsigned | letsencrypt
	CertPath      string
	KeyPath       string
	DocrootSuffix string // per-domain running directory (relative)
}

// SecuredDomains returns domains with an active certificate available on disk.
func (v VhostSpec) SecuredDomains() []DomainSpec {
	var out []DomainSpec
	for _, d := range v.Domains {
		if d.SSLMode == "none" {
			continue
		}
		if d.CertPath == "" || d.KeyPath == "" {
			continue
		}
		if _, err := os.Stat(d.CertPath); err != nil {
			continue
		}
		out = append(out, d)
	}
	return out
}

// wsMapVar is the per-site nginx variable carrying the Connection header
// value for websocket upgrades. The map lives in the vhost file's top level
// (http context); the per-site name keeps coexisting vhosts collision-free.
func wsMapVar(websiteID string) string {
	return "ep_ws_" + strings.ReplaceAll(websiteID, "-", "_")
}

// renderWSMap emits the http-context map used for websocket-aware proxying.
// Without it, websocket upgrades die at the edge (Connection header is not
// forwarded by default).
func renderWSMap(websiteID string) string {
	return fmt.Sprintf("map $http_upgrade $%s {\n\tdefault upgrade;\n\t''      close;\n}\n\n", wsMapVar(websiteID))
}

// RenderVhost produces the nginx configuration: one 80-block for unsecured
// domains, a redirect block for secured domains, and a 443 block serving
// them. Includes the ACME webroot location for HTTP-01 challenges.
func RenderVhost(v VhostSpec) string {
	if v.Terminated {
		return renderVhostStub(v, 410)
	}
	if v.Suspended {
		return renderVhostSuspended(v)
	}
	if v.QuotaExceeded {
		return renderVhostQuotaExceeded(v)
	}
	var b strings.Builder
	b.WriteString("# managed by EpicPanel — website " + v.WebsiteID + " — DO NOT EDIT\n")
	if v.ProxyPass != "" {
		b.WriteString(renderWSMap(v.WebsiteID))
	}
	redirects := normalizeRedirects(v.WebsiteID, v.Redirects)
	redirectSet := map[string]bool{}
	for _, r := range redirects {
		redirectSet[strings.ToLower(r.From)] = true
	}

	secured := v.SecuredDomains()
	securedSet := map[string]bool{}
	for _, d := range secured {
		securedSet[strings.ToLower(d.Domain)] = true
	}

	// Group ALL domains by effective docroot: the site default or a
	// per-domain override. Each group becomes its own server blocks, so an
	// addon domain can run any subdirectory of the site tree.
	type group struct {
		names   []string
		secured []DomainSpec
		docroot string
	}
	groups := map[string]*group{}
	var order []string
	docrootFor := func(suffix string) string {
		if suffix == "" {
			return v.DocumentRoot
		}
		rel := strings.TrimPrefix(filepath.Clean("/"+suffix), "/")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
			return v.DocumentRoot
		}
		return filepath.Join(strings.TrimSuffix(v.DocumentRoot, "/public"), rel)
	}
	for _, d := range v.Domains {
		// A domain with a dedicated redirect is excluded from the plain :80
		// server_names — it gets its own redirect-only server block below.
		if redirectSet[strings.ToLower(d.Domain)] {
			continue
		}
		root := docrootFor(d.DocrootSuffix)
		if groups[root] == nil {
			groups[root] = &group{docroot: root}
			order = append(order, root)
		}
		groups[root].names = append(groups[root].names, d.Domain)
		if securedSet[strings.ToLower(d.Domain)] {
			for _, s := range secured {
				if strings.EqualFold(s.Domain, d.Domain) {
					groups[root].secured = append(groups[root].secured, s)
				}
			}
		}
	}

	// Dedicated redirect-only :80 server blocks. A redirected domain is
	// excluded from every other plain :80 server_name, so the redirect block
	// is the authoritative match.
	for _, r := range redirects {
		fmt.Fprintf(&b, "server {\n\tlisten 80;\n\tserver_name %s;\n\n\treturn %d %s;\n}\n\n", r.From, r.Status, r.To)
	}

	for _, root := range order {
		g := groups[root]
		gv := VhostSpec{
			WebsiteID:    v.WebsiteID,
			UnixUser:     v.UnixUser,
			DocumentRoot: g.docroot,
			FpmSocket:    v.FpmSocket,
			RewriteRules: v.RewriteRules,
			ProxyPass:    v.ProxyPass,
			Domains:      g.secured,
		}
		common := gv.commonLocations() + gv.renderRewriteRules()

		var plainNames, securedNames []string
		for _, name := range g.names {
			if securedSet[strings.ToLower(name)] {
				securedNames = append(securedNames, name)
			} else {
				plainNames = append(plainNames, name)
			}
		}

		if len(plainNames) > 0 {
			fmt.Fprintf(&b, "server {\n\tlisten 80;\n\tserver_name %s;\n\n%s}\n\n", strings.Join(plainNames, " "), common)
		}
		if len(g.secured) > 0 {
			fmt.Fprintf(&b, "server {\n\tlisten 80;\n\tserver_name %s;\n\n\tlocation /.well-known/acme-challenge/ {\n\t\troot %s;\n\t}\n\n\tlocation / {\n\t\treturn 301 https://$host$request_uri;\n\t}\n}\n\n", strings.Join(securedNames, " "), acmeWebroot)
			first := g.secured[0]
			fmt.Fprintf(&b, "server {\n\tlisten 443 ssl;\n\tserver_name %s;\n\n\tssl_certificate %s;\n\tssl_certificate_key %s;\n\tssl_protocols TLSv1.2 TLSv1.3;\n\n%s}\n\n", strings.Join(securedNames, " "), first.CertPath, first.KeyPath, common)
		}
	}
	return b.String()
}

// renderRewriteRules returns the sanitized user snippet indented for a server
// block, or "" when none configured. Validated by the shared rewrite core
// (see sanitizeRewriteLine) before rendering.
func (v VhostSpec) renderRewriteRules() string {
	rules := strings.TrimSpace(v.RewriteRules)
	if rules == "" {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n\t# user rewrite rules / custom directives (EpicPanel managed)\n")
	for _, raw := range strings.Split(rules, "\n") {
		if line := sanitizeRewriteLine(v.WebsiteID, raw, nginxRewriteTokens); line != "" {
			out.WriteString("\t" + line + "\n")
		}
	}
	return out.String()
}

func (v VhostSpec) commonLocations() string {
	accessLog := fmt.Sprintf("/srv/epicpanel/websites/%s/logs/nginx-access.log", v.WebsiteID)
	errorLog := fmt.Sprintf("/srv/epicpanel/websites/%s/logs/nginx-error.log", v.WebsiteID)

	phpSection := ""
	if v.FpmSocket != "" {
		phpSection = fmt.Sprintf(`
	location ~ \.php$ {
		include snippets/fastcgi-php.conf;
		fastcgi_pass unix:%s;
	}
`, v.FpmSocket)
	}

	// nginx-edge mode: everything (except ACME challenges) goes to the
	// backend web server (Apache/OpenLiteSpeed) on its internal port — or to
	// the site's application process (node/python/go systemd unit) on its
	// loopback port. Upgrade/Connection headers keep WebSockets working
	// ($epicpanel_connection_upgrade is the http-level map written by
	// InstallNginx; the namespaced variable never collides with user maps);
	// generous timeouts for long-poll/SSE workloads and a request-body cap
	// are part of the proxy contract.
	rootSection := fmt.Sprintf("	root %s;\n	index index.php index.html index.htm;\n\n	location / {\n		try_files $uri $uri/ =404;\n	}\n\n", v.DocumentRoot)
	phpHeader := ""
	if v.ProxyPass != "" {
		rootSection = ""
		phpSection = ""
		phpHeader = fmt.Sprintf(`
	location / {
		proxy_pass %s;
		proxy_http_version 1.1;
		proxy_set_header Upgrade $http_upgrade;
		proxy_set_header Connection $%s;
		proxy_set_header Host $host;
		proxy_set_header X-Real-IP $remote_addr;
		proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
		proxy_set_header X-Forwarded-Proto $scheme;
		proxy_set_header X-Forwarded-Host $host;
		proxy_connect_timeout 15s;
		proxy_send_timeout 300s;
		proxy_read_timeout 300s;
		proxy_buffering off;
		client_max_body_size 100m;
	}
`, v.ProxyPass, wsMapVar(v.WebsiteID))
	}

	return fmt.Sprintf(`	%s
	access_log %s;
	error_log %s;

	location /.well-known/acme-challenge/ {
		root %s;
	}
%s
	location ~ /\. {
		deny all;
	}
%s%s
`, rootSection, accessLog, errorLog, v.acmeWebrootPath(), errorPagesSection(), phpHeader, phpSection)
}

// epicpanelDefaultPagesDir is the panel-owned directory holding the global
// default status pages (written by InstallNginx).
const epicpanelDefaultPagesDir = "/srv/epicpanel/default_pages"

// errorPagesSection wires the panel's default status pages into a live
// vhost: nginx-generated 404s render "Sorry, Wrong Page"; origin
// unreachable/overloaded (nginx-generated 502/504) renders "Server Busy".
// Application-generated error responses pass through untouched
// (fastcgi_intercept_errors / proxy_intercept_errors default off) — the
// panel never masks what a site's own code answers.
func errorPagesSection() string {
	return fmt.Sprintf(`
	location = /epicpanel-busy.html {
		root %s;
		internal;
	}

	location = /epicpanel-notfound.html {
		root %s;
		internal;
	}

	error_page 404 /epicpanel-notfound.html;
	error_page 502 504 /epicpanel-busy.html;
`, epicpanelDefaultPagesDir, epicpanelDefaultPagesDir)
}

// acmeWebrootPath resolves the ACME webroot (injectable for tests).
func (v VhostSpec) acmeWebrootPath() string {
	if v.AcmeWebroot != "" {
		return v.AcmeWebroot
	}
	return acmeWebroot
}

func (v VhostSpec) logsBasePath() string {
	if v.LogsBase != "" {
		return v.LogsBase
	}
	return "/srv/epicpanel/websites"
}

// renderVhostSuspended renders the suspension stub: every domain answers 503
// from a static page. No PHP, no proxy, no docroot — but the log locations
// are preserved so traffic stays observable.
func renderVhostSuspended(v VhostSpec) string {
	names := make([]string, 0, len(v.Domains))
	for _, d := range v.Domains {
		names = append(names, d.Domain)
	}
	if len(names) == 0 {
		return ""
	}
	return renderVhostStub(v, 503)
}

// renderVhostStub renders the lifecycle stub for every domain: the site
// answers `status` from a static page (suspended 503 / terminated 410 /
// bandwidth-exhausted 503 with per-site templated values). No PHP, no
// proxy, no docroot — log locations are preserved so traffic stays
// observable. The stub response is marked no-store so CDN/browser caches
// cannot keep serving the site after a lifecycle change.
func renderVhostStub(v VhostSpec, status int) string {
	names := make([]string, 0, len(v.Domains))
	for _, d := range v.Domains {
		names = append(names, d.Domain)
	}
	if len(names) == 0 {
		return ""
	}
	page := "suspended.html"
	root := "/srv/epicpanel/default_pages"
	if v.Terminated {
		page, status = "terminated.html", 410
	} else if v.StubPage != "" {
		page = v.StubPage
		if page == bandwidthStubPage {
			root = filepath.Join("/srv/epicpanel/websites", v.WebsiteID, "pages")
		}
	}
	logsDir := fmt.Sprintf("%s/%s/logs", v.logsBasePath(), v.WebsiteID)
	return fmt.Sprintf(`# managed by EpicPanel — website %s (stub %d) — DO NOT EDIT
server {
	listen 80;
	server_name %s;

	access_log %s/nginx-access.log;
	error_log %s/nginx-error.log;

	return %d;
	error_page %d /%s;
	location = /%s {
		root %s;
		internal;
		add_header Cache-Control "no-store" always;
	}
}
`, v.WebsiteID, status, strings.Join(names, " "), logsDir, logsDir, status, status, page, page, root)
}

// bandwidthStubPage is the reason-aware suspension page for
// bandwidth_exhausted, rendered per site from the suspend metadata.
const bandwidthStubPage = "bandwidth_exhausted.html"

// renderVhostQuotaExceeded renders the quota exceeded stub: every domain answers 509
// (Bandwidth Limit Exceeded) immediately.
func renderVhostQuotaExceeded(v VhostSpec) string {
	var names []string
	for _, d := range v.Domains {
		names = append(names, d.Domain)
	}
	if len(names) == 0 {
		return ""
	}
	logsDir := filepath.Join("/srv/epicpanel/websites", v.WebsiteID, "logs")
	return fmt.Sprintf(`	# managed by EpicPanel — website %s (quota exceeded) — DO NOT EDIT
	server {
		listen 80;
		server_name %s;

		access_log %s/nginx-access.log;
		error_log %s/nginx-error.log;

		return 509;
		error_page 509 /quota_exceeded.html;
		location = /quota_exceeded.html {
			root /srv/epicpanel/default_pages;
			internal;
		}
	}
`, v.WebsiteID, strings.Join(names, " "), logsDir, logsDir)
}

const nginxAvailable = "/etc/nginx/sites-available"
const nginxEnabled = "/etc/nginx/sites-enabled"

// NginxProvider implements WebServerProvider for nginx. Use NginxProvider{}
// for the production paths, or set the dir fields for tests.
type NginxProvider struct {
	AvailableDir string
	EnabledDir   string
	ACMEWebroot  string
	LogsBase     string
}

func (n *NginxProvider) Name() string { return "nginx" }

func (n *NginxProvider) availableDir() string {
	if n.AvailableDir != "" {
		return n.AvailableDir
	}
	return nginxAvailable
}

func (n *NginxProvider) enabledDir() string {
	if n.EnabledDir != "" {
		return n.EnabledDir
	}
	return nginxEnabled
}

func (n *NginxProvider) acmeWebrootPath() string {
	if n.ACMEWebroot != "" {
		return n.ACMEWebroot
	}
	return acmeWebroot
}

// Ensure writes the vhost atomically, validates with nginx -t (120s cap via
// ValidateCmd), enables and reloads. On validation failure the previous
// config is restored (SwapValidated) and the error returned.
func (n *NginxProvider) Ensure(ctx context.Context, v VhostSpec) error {
	if err := validateVhostSpec(v); err != nil {
		return err
	}
	availDir, enabledDir := n.availableDir(), n.enabledDir()
	if err := os.MkdirAll(availDir, 0o755); err != nil {
		return fmt.Errorf("create sites-available: %w", err)
	}
	if err := os.MkdirAll(enabledDir, 0o755); err != nil {
		return fmt.Errorf("create sites-enabled: %w", err)
	}
	if err := os.MkdirAll(n.acmeWebrootPath(), 0o755); err != nil {
		return fmt.Errorf("create acme webroot: %w", err)
	}
	logsBase := v.logsBasePath()
	if n.LogsBase != "" {
		logsBase = n.LogsBase
	}
	if err := os.MkdirAll(filepath.Join(logsBase, v.WebsiteID, "logs"), 0o755); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}

	avail := filepath.Join(availDir, "epicpanel-"+v.WebsiteID+".conf")
	enabled := filepath.Join(enabledDir, "epicpanel-"+v.WebsiteID+".conf")
	content := RenderVhost(v)

	if existing, err := os.ReadFile(avail); err != nil || string(existing) != content {
		hadExisting := err == nil
		if err := SwapValidated(avail, []byte(content), 0o644, func() error {
			return ValidateCmd(ctx, 120*time.Second, "nginx", "-t")
		}); err != nil {
			if !hadExisting {
				// Fresh site: nothing to restore, so also make sure no
				// dangling enabled link is left behind.
				_ = os.Remove(enabled)
			}
			return fmt.Errorf("nginx config validation failed: %w", err)
		}
	}

	if err := enableSiteFile(enabled, avail); err != nil {
		return fmt.Errorf("enable vhost: %w", err)
	}

	return reloadNginx(ctx)
}

// Remove deletes both files (idempotent) and reloads.
func (n *NginxProvider) Remove(ctx context.Context, websiteID string) error {
	avail := filepath.Join(n.availableDir(), "epicpanel-"+websiteID+".conf")
	enabled := filepath.Join(n.enabledDir(), "epicpanel-"+websiteID+".conf")
	changed := false
	for _, f := range []string{enabled, avail} {
		if _, err := os.Lstat(f); err == nil {
			if err := os.Remove(f); err != nil {
				return fmt.Errorf("remove %s: %w", f, err)
			}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if _, err := exec.LookPath("nginx"); err == nil {
		return reloadNginx(ctx)
	}
	return nil
}

// InstallNginx ensures nginx itself is present (idempotent), and provisions
// the global default/suspended pages.
func (e *Executor) InstallNginx(ctx context.Context) error {
	if _, err := exec.LookPath("nginx"); err != nil {
		if err := e.aptInstall(ctx, []string{"nginx"}, "", "nginx"); err != nil {
			return err
		}
	}

	pagesDir := "/srv/epicpanel/default_pages"
	if err := os.MkdirAll(pagesDir, 0o755); err != nil {
		return fmt.Errorf("mkdir pages: %w", err)
	}
	_ = os.WriteFile(filepath.Join(pagesDir, "default.html"), []byte(pages.DefaultHTML), 0o644)
	_ = os.WriteFile(filepath.Join(pagesDir, "suspended.html"), []byte(pages.SuspendedHTML), 0o644)
	_ = os.WriteFile(filepath.Join(pagesDir, "quota_exceeded.html"), []byte(pages.QuotaExceededHTML), 0o644)
	_ = os.WriteFile(filepath.Join(pagesDir, "busy.html"), []byte(pages.BusyHTML), 0o644)
	_ = os.WriteFile(filepath.Join(pagesDir, "notfound.html"), []byte(pages.NotFoundHTML), 0o644)

	// WebSocket upgrade map for proxied vhosts (app sites + Apache/OLS
	// edge mode). conf.d/*.conf is included by nginx.conf's http block on
	// every supported distro. Idempotent: identical content is never
	// rewritten (nginx -t validates on every vhost converge anyway).
	mapFile := "/etc/nginx/conf.d/epicpanel-websocket-map.conf"
	mapContent := `# managed by EpicPanel — do not edit
map $http_upgrade $epicpanel_connection_upgrade {
	default upgrade;
	''      close;
}
`
	if existing, err := os.ReadFile(mapFile); err != nil || string(existing) != mapContent {
		_ = os.MkdirAll(filepath.Dir(mapFile), 0o755)
		if err := AtomicWriteFile(mapFile, []byte(mapContent), 0o644); err != nil {
			return fmt.Errorf("write websocket map: %w", err)
		}
	}

	defaultVhost := fmt.Sprintf(`server {
	listen 80 default_server;
	server_name _;
	root %s;
	index default.html;
	location / {
		try_files $uri /default.html;
	}
	error_page 503 /suspended.html;
	error_page 509 /quota_exceeded.html;
}
`, pagesDir)
	
	if err := os.WriteFile("/etc/nginx/sites-available/default", []byte(defaultVhost), 0o644); err != nil {
		return fmt.Errorf("write default vhost: %w", err)
	}
	_ = runCmd(ctx, "ln", "-sf", "/etc/nginx/sites-available/default", "/etc/nginx/sites-enabled/default")

	return nil
}

func reloadNginx(ctx context.Context) error {
	if _, err := exec.LookPath("nginx"); err != nil {
		return fmt.Errorf("nginx binary not found")
	}
	if err := runCmd(ctx, "systemctl", "enable", "nginx"); err != nil {
		return fmt.Errorf("enable nginx: %w", err)
	}
	_ = runCmd(ctx, "systemctl", "start", "nginx")
	if err := runCmd(ctx, "systemctl", "reload", "nginx"); err != nil {
		return fmt.Errorf("reload nginx: %w", err)
	}
	slog.Info("nginx reloaded")
	return nil
}

func runCmd(ctx context.Context, name string, args ...string) error {
	c, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(c, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s (%w)", name, strings.Join(args, " "), tail(out, 500), err)
	}
	return nil
}

func validateVhostSpec(v VhostSpec) error {
	if _, err := uuid.Parse(v.WebsiteID); err != nil {
		return fmt.Errorf("invalid website id: %w", err)
	}
	if v.DocumentRoot == "" || !strings.HasPrefix(v.DocumentRoot, "/srv/epicpanel/websites/") {
		return fmt.Errorf("document root %q outside epicpanel tree", v.DocumentRoot)
	}
	for _, d := range v.Domains {
		if !validDomainName(d.Domain) {
			return fmt.Errorf("invalid domain %q", d.Domain)
		}
	}
	return nil
}

func validDomainName(s string) bool {
	if len(s) < 3 || len(s) > 253 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.':
		default:
			return false
		}
	}
	return true
}

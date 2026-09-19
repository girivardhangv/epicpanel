package agent

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/ports"
)

// ============================================================================
// MULTI WEB SERVER SUPPORT — Apache + OpenLiteSpeed site vhosts.
//
// Serving models (websites.web_servers):
//   nginx            — nginx serves directly (existing RenderVhost path)
//   apache           — Apache binds the domain on :80, nginx vhost removed
//   openlitespeed    — OLS binds the domain on :80, nginx vhost removed
//   nginx,apache     — nginx is the edge (80/443, TLS, ACME) and proxies to
//                      Apache on a per-site internal port
//   nginx,openlitespeed — nginx edge proxies to OLS internal port
// ============================================================================

const apacheSitesAvail = "/etc/apache2/sites-available"
const apacheSitesEnabled = "/etc/apache2/sites-enabled"
const olsVhostDir = "/usr/local/lsws/conf/vhosts"

// Backend port resolution: the control plane persists a per-website port in
// the desired payload (source of truth). The hash-derived fallback only
// applies to payloads from an older control plane, and stays inside the same
// configurable ranges so it can never collide with public ports.
func apachePort(p ProvisionPayload) int {
	if p.BackendPort > 0 {
		return p.BackendPort
	}
	return fallbackPort(ports.Apache(), p.WebsiteID)
}

func olsPort(p ProvisionPayload) int {
	if p.BackendPort > 0 {
		return p.BackendPort
	}
	return fallbackPort(ports.OLS(), p.WebsiteID)
}

func apacheInternalPort(websiteID string) int { return fallbackPort(ports.Apache(), websiteID) }
func olsInternalPort(websiteID string) int    { return fallbackPort(ports.OLS(), websiteID) }

func fallbackPort(r ports.Range, websiteID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(websiteID))
	return r.Min + int(h.Sum32()%uint32(r.Max-r.Min+1))
}

// webServerList parses and validates the web_servers column.
func webServerList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(strings.ToLower(part))
		switch p {
		case "nginx", "apache", "openlitespeed", "none":
			if p != "none" {
				out = append(out, p)
			}
		}
	}
	return out
}

// ============================================================================
// Apache
// ============================================================================

// RenderApacheSite produces an Apache vhost. The site always runs as a
// PRIVATE backend on 127.0.0.1:<internalPort> — nginx owns the public ports.
func RenderApacheSite(v VhostSpec, internalPort int) string {
	if v.Suspended {
		return renderApacheSuspended(v, internalPort)
	}
	if v.QuotaExceeded {
		return renderApacheQuotaExceeded(v, internalPort)
	}
	serverNames := make([]string, 0, len(v.Domains))
	for _, d := range v.Domains {
		serverNames = append(serverNames, d.Domain)
	}

	// Apache needs a global Listen; keep it scoped to this site's loopback
	// port so nginx keeps :80/:443.
	listen := fmt.Sprintf("Listen 127.0.0.1:%d\n", internalPort)
	var php string
	if v.FpmSocket != "" {
		php = fmt.Sprintf(`
	<FilesMatch \.php$>
		SetHandler "proxy:unix:%s|fcgi://localhost"
	</FilesMatch>`, v.FpmSocket)
	}

	// User rewrite rules go through the shared rewrite core (allowlist +
	// smuggling checks); Apache renders its own syntax.
	rewrite := ""
	if strings.TrimSpace(v.RewriteRules) != "" {
		rewrite = "\n\t<IfModule mod_rewrite.c>\n\t\tRewriteEngine On\n"
		for _, raw := range strings.Split(v.RewriteRules, "\n") {
			line := sanitizeRewriteLine(v.WebsiteID, raw, apacheRewriteTokens)
			if line == "" {
				continue
			}
			rewrite += "\t\t" + strings.TrimRight(line, ";") + "\n"
		}
		rewrite += "\t</IfModule>"
	}

	// Domain-level redirects: Apache's Redirect directive (host-agnostic).
	for _, r := range normalizeRedirects(v.WebsiteID, v.Redirects) {
		rewrite += fmt.Sprintf("\n\tRedirect %d / %s", r.Status, r.To)
	}

	return fmt.Sprintf(`# managed by EpicPanel — website %s — DO NOT EDIT
%s<VirtualHost 127.0.0.1:%d>
	ServerName %s
	DocumentRoot %s
%s
	<Directory %s>
		Options -Indexes +FollowSymLinks
		AllowOverride All
		Require all granted
	</Directory>
%s
	ErrorLog /srv/epicpanel/websites/%s/logs/apache-error.log
	CustomLog /srv/epicpanel/websites/%s/logs/apache-access.log combined
</VirtualHost>
`, v.WebsiteID, listen, internalPort, strings.Join(serverNames, " "), v.DocumentRoot, php, v.DocumentRoot, rewrite, v.WebsiteID, v.WebsiteID)
}

// renderApacheSuspended serves a plain 503 "Account suspended" stub for every
// domain: a rewrite forces the 503 status, ErrorDocument supplies the body.
// No PHP handler, no proxy — but the log locations are preserved.
func renderApacheSuspended(v VhostSpec, internalPort int) string {
	serverNames := make([]string, 0, len(v.Domains))
	for _, d := range v.Domains {
		serverNames = append(serverNames, d.Domain)
	}
	listen := fmt.Sprintf("Listen 127.0.0.1:%d\n", internalPort)
	return fmt.Sprintf(`# managed by EpicPanel — website %s (suspended) — DO NOT EDIT
%s<VirtualHost 127.0.0.1:%d>
	ServerName %s
	DocumentRoot %s

	<Directory %s>
		Options -Indexes
		Require all granted
	</Directory>

	<IfModule mod_rewrite.c>
		RewriteEngine On
		RewriteRule ^ - [R=503,L]
	</IfModule>
	Alias /epicpanel_suspended.html /srv/epicpanel/default_pages/suspended.html
	ErrorDocument 503 /epicpanel_suspended.html

	ErrorLog /srv/epicpanel/websites/%s/logs/apache-error.log
	CustomLog /srv/epicpanel/websites/%s/logs/apache-access.log combined
</VirtualHost>
`, v.WebsiteID, listen, internalPort, strings.Join(serverNames, " "), v.DocumentRoot, v.DocumentRoot, v.WebsiteID, v.WebsiteID)
}

func renderApacheQuotaExceeded(v VhostSpec, internalPort int) string {
	serverNames := make([]string, 0, len(v.Domains))
	for _, d := range v.Domains {
		serverNames = append(serverNames, d.Domain)
	}
	listen := fmt.Sprintf("Listen 127.0.0.1:%d\n", internalPort)
	return fmt.Sprintf(`# managed by EpicPanel — website %s (quota exceeded) — DO NOT EDIT
%s<VirtualHost 127.0.0.1:%d>
	ServerName %s
	DocumentRoot %s

	<Directory %s>
		Options -Indexes
		Require all granted
	</Directory>

	<IfModule mod_rewrite.c>
		RewriteEngine On
		RewriteRule ^ - [R=508,L]
	</IfModule>
	Alias /epicpanel_quota.html /srv/epicpanel/default_pages/quota_exceeded.html
	ErrorDocument 508 /epicpanel_quota.html

	ErrorLog /srv/epicpanel/websites/%s/logs/apache-error.log
	CustomLog /srv/epicpanel/websites/%s/logs/apache-access.log combined
</VirtualHost>
`, v.WebsiteID, listen, internalPort, strings.Join(serverNames, " "), v.DocumentRoot, v.DocumentRoot, v.WebsiteID, v.WebsiteID)
}

// apacheListenRe extracts the loopback Listen port of a managed vhost.
var apacheListenRe = regexp.MustCompile(`(?m)^\s*Listen\s+127\.0\.0\.1:(\d+)\s*$`)

func apacheListenPort(conf string) int {
	m := apacheListenRe.FindStringSubmatch(conf)
	if m == nil {
		return 0
	}
	p, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return p
}

// EnsureApacheSite writes + enables the Apache vhost (idempotent, validated).
// The backend always binds 127.0.0.1:<internalPort> — never public ports.
func (e *Executor) EnsureApacheSite(ctx context.Context, v VhostSpec, internalPort int) error {
	return (&ApacheProvider{Ex: e}).ensureSite(ctx, v, internalPort)
}

// ensureSite is the Apache engine. Pipeline: verify port -> atomic write ->
// configtest (120s cap, restore on failure) -> enable-state rollback ->
// reload when the vhost already existed with the same Listen port, restart
// otherwise.
func (a *ApacheProvider) ensureSite(ctx context.Context, v VhostSpec, internalPort int) error {
	if a.Ex == nil {
		return fmt.Errorf("apache provider requires an executor")
	}
	if internalPort <= 0 {
		return fmt.Errorf("apache backend port not allocated")
	}
	// The port must be actually free (or already ours) before configuring.
	if err := verifyBackendPort(internalPort); err != nil {
		return fmt.Errorf("apache backend port %d: %w", internalPort, err)
	}
	if err := a.Ex.InstallRuntime(ctx, "", "apache", "2.4"); err != nil {
		return fmt.Errorf("apache runtime: %w", err)
	}
	availDir, enabledDir := a.sitesAvailableDir(), a.sitesEnabledDir()
	if err := os.MkdirAll(availDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(enabledDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(a.logsBaseDir(), v.WebsiteID, "logs"), 0o755); err != nil {
		return err
	}
	// The distro default vhost binds :80 (taken by nginx) — park it and give
	// Apache a global ServerName so configtest never warns.
	_ = a.Ex.run(ctx, "a2dissite", "000-default")
	_ = AtomicWriteFile(a.serverNameConfPath(), []byte("ServerName epicpanel.local\n"), 0o644)
	_ = a.Ex.run(ctx, "a2enconf", "epicpanel-servername")
	// ports.conf ships `Listen 80/443` — nginx owns those ports. Replace it
	// with an empty managed file: every backend site carries its own
	// `Listen 127.0.0.1:<port>` in its vhost.
	portsConf := "# managed by EpicPanel — do not edit\n"
	if b, err := os.ReadFile(a.portsConfPath()); err != nil || string(b) != portsConf {
		if err := AtomicWriteFile(a.portsConfPath(), []byte(portsConf), 0o644); err != nil {
			return err
		}
	}
	file := filepath.Join(availDir, "epicpanel-"+v.WebsiteID+".conf")
	content := RenderApacheSite(v, internalPort)

	// Reload (not restart) is enough when the vhost already existed with an
	// unchanged Listen port; a new vhost or a changed port needs a restart.
	prev, prevErr := os.ReadFile(file)
	samePort := prevErr == nil && apacheListenPort(string(prev)) == internalPort

	enabled := filepath.Join(enabledDir, "epicpanel-"+v.WebsiteID+".conf")
	_, statErr := os.Lstat(enabled)
	wasEnabled := statErr == nil
	// Validate the enabled state exactly as it will run: link first, roll the
	// link back when the site was not enabled before.
	if err := enableSiteFile(enabled, file); err != nil {
		return err
	}
	if err := SwapValidated(file, []byte(content), 0o644, func() error {
		return ValidateCmd(ctx, 120*time.Second, "apache2ctl", "configtest")
	}); err != nil {
		if !wasEnabled {
			_ = os.Remove(enabled)
		}
		return fmt.Errorf("apache configtest failed: %w", err)
	}
	_ = a.Ex.run(ctx, "a2ensite", "epicpanel-"+v.WebsiteID)
	action := "restart"
	if wasEnabled && samePort {
		action = "reload"
	}
	_ = a.Ex.run(ctx, "systemctl", action, "apache2")
	return nil
}

// RemoveApacheSite disables and deletes the site's Apache vhost (idempotent).
func (e *Executor) RemoveApacheSite(ctx context.Context, websiteID string) {
	(&ApacheProvider{Ex: e}).removeSite(ctx, websiteID)
}

// removeSite is the Apache removal engine.
func (a *ApacheProvider) removeSite(ctx context.Context, websiteID string) {
	enabled := filepath.Join(a.sitesEnabledDir(), "epicpanel-"+websiteID+".conf")
	avail := filepath.Join(a.sitesAvailableDir(), "epicpanel-"+websiteID+".conf")
	if _, err := os.Lstat(enabled); err == nil {
		_ = os.Remove(enabled)
		if a.Ex != nil {
			_ = a.Ex.run(ctx, "systemctl", "reload", "apache2")
		}
	}
	_ = os.Remove(avail)
}

// verifyBackendPort ensures the private port is usable by this backend:
// free on loopback, or already listening (a previous incarnation of the same
// backend — e.g. re-provision after an agent restart). Public/privileged
// ports can never appear here: the allocator's ranges prevent it.
func verifyBackendPort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("port %d outside unprivileged range", port)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return nil // free
	}
	_ = conn.Close()
	// Something listens: fine only if it already serves this backend. We
	// cannot probe identity generically; accept an existing listener only if
	// it is a loopback port within the managed ranges.
	for _, r := range []ports.Range{ports.Apache(), ports.OLS()} {
		if r.Contains(port) {
			return nil
		}
	}
	return fmt.Errorf("port %d is occupied outside managed backend ranges", port)
}

// stopApacheIfUnused stops the shared Apache service when no EpicPanel site
// vhost remains — one site's deletion must not disturb other tenants.
func (e *Executor) stopApacheIfUnused(ctx context.Context) {
	matches, _ := filepath.Glob(filepath.Join(apacheSitesEnabled, "epicpanel-*.conf"))
	if len(matches) == 0 {
		_ = e.run(ctx, "systemctl", "stop", "apache2")
		_ = e.run(ctx, "systemctl", "disable", "apache2")
	}
}

// normalizeOLSUser points OLS's worker user/group at www-data so it can
// traverse site trees (the ACL model grants www-data; nobody would 403).
func (o *OLSProvider) normalizeOLSUser(ctx context.Context) {
	confPath := o.httpdConfigPath()
	conf, err := os.ReadFile(confPath)
	if err != nil {
		return
	}
	out := string(conf)
	replaced := false
	for _, kv := range [][2]string{
		{"user", "www-data"},
		{"group", "www-data"},
	} {
		re := regexp.MustCompile(`(?m)^(\s*)` + kv[0] + `\s+\S+\s*$`)
		if re.MatchString(out) {
			// ${1} bracket form: "$1"+kv[0] would parse as group "1"+name.
			out = re.ReplaceAllString(out, "${1}"+kv[0]+" "+kv[1])
			replaced = true
		}
	}
	if replaced {
		_ = os.WriteFile(confPath, []byte(out), 0o600)
	}
}

// stopOLSIfUnused stops the shared OpenLiteSpeed service when no EpicPanel
// vhost remains registered.
func (e *Executor) stopOLSIfUnused(ctx context.Context) {
	confPath := "/usr/local/lsws/conf/httpd_config.conf"
	conf, err := os.ReadFile(confPath)
	if err != nil {
		return
	}
	if !strings.Contains(string(conf), "virtualhost epicpanel-") {
		_ = e.run(ctx, "systemctl", "stop", "lsws")
		_ = e.run(ctx, "systemctl", "disable", "lsws")
	}
}

// ============================================================================
// OpenLiteSpeed
// ============================================================================

// RenderOLSVhconf produces the OLS per-vhost config (XML-ish format).
func RenderOLSVhconf(v VhostSpec, internalPort int) string {
	if v.Suspended {
		return renderOLSSuspended(v)
	}
	if v.QuotaExceeded {
		return renderOLSQuotaExceeded(v)
	}
	docRoot := v.DocumentRoot
	var php string
	if v.FpmSocket != "" {
		// External PHP-FPM over its unix socket (uds:// + path w/o leading
		// slash). autoStart 0: the pool is managed by EpicPanel, not OLS.
		sock := strings.TrimPrefix(v.FpmSocket, "/")
		php = fmt.Sprintf(`
extProcessor fpm-%s {
  type                    fcgi
  address                 uds://%s
  maxConns                10
  persistConn             1
  autoStart               0
  initTimeout             60
  retryTimeout            0
  respBuffer              0
}

scriptHandler {
  add                     fcgi:fpm-%s php
}`, v.WebsiteID, sock, v.WebsiteID)
	}
	// OLS always serves the site on its own listener (port 80 when the site
	// is OLS-only; per-site internal port when nginx is the edge).
	// OLS rewrite rules map from the shared rewrite core where semantics
	// overlap.
	var rewrites strings.Builder
	for _, raw := range strings.Split(v.RewriteRules, "\n") {
		line := sanitizeRewriteLine(v.WebsiteID, raw, olsRewriteTokens)
		if line == "" {
			continue
		}
		fields := strings.Fields(strings.TrimRight(line, ";"))
		if len(fields) >= 3 && strings.EqualFold(fields[0], "rewrite") {
			rewrites.WriteString("    rewrite  " + fields[1] + "  " + strings.Trim(fields[2], "()") + "\n")
		}
	}
	// Domain-level redirects: host-conditioned rewrite rules.
	for _, r := range normalizeRedirects(v.WebsiteID, v.Redirects) {
		rewrites.WriteString(fmt.Sprintf("    rewriteCond  %%{HTTP_HOST}  ^%s$  [NC]\n", strings.ReplaceAll(r.From, ".", `\.`)))
		rewrites.WriteString(fmt.Sprintf("    rewriteRule  ^/.*$  %s  [R=%d,L]\n", r.To, r.Status))
	}
	rewriteBlock := ""
	if rewrites.Len() > 0 {
		rewriteBlock = fmt.Sprintf(`
  context / {
    location                %s
    allowBrowse             1
    rewrite  {
      enable                1
%s    }
  }`, docRoot, rewrites.String())
	}
	return fmt.Sprintf(`docRoot                   $VH_ROOT
vhDomain                  $VH_NAME
adminEmails               admin@epicpanel.local
enableGzip                1
enableIpGeo               0
errorlog $VH_ROOT/logs/ols-error.log {
  useServer               1
  logLevel                ERROR
}
accesslog $VH_ROOT/logs/ols-access.log {
  useServer               1
  rollingSize             10M
}
%s%s
`, php, rewriteBlock)
}

// renderOLSSuspended serves a plain 503 "Account suspended" stub for every
// request: the rewrite forces the status. No extProcessor (PHP), no proxy —
// but the log locations are preserved.
func renderOLSSuspended(v VhostSpec) string {
	return fmt.Sprintf(`# managed by EpicPanel — website %s (suspended) — DO NOT EDIT
docRoot                   $VH_ROOT
vhDomain                  $VH_NAME
adminEmails               admin@epicpanel.local
enableGzip                1
enableIpGeo               0
errorlog $VH_ROOT/logs/ols-error.log {
  useServer               1
  logLevel                ERROR
}
accesslog $VH_ROOT/logs/ols-access.log {
  useServer               1
  rollingSize             10M
}

errorpage 503 {
  url                     /srv/epicpanel/default_pages/suspended.html
}

# suspended: 503 "Account suspended" for every request (no PHP, no proxy)
context / {
  location                $VH_ROOT
  allowBrowse             0
  rewrite  {
    enable                1
    rewriteRule  ^/.*$  -  [R=503,L]
  }
}
}
`, v.WebsiteID)
}

func renderOLSQuotaExceeded(v VhostSpec) string {
	return fmt.Sprintf(`# managed by EpicPanel — website %s (quota exceeded) — DO NOT EDIT
docRoot                   $VH_ROOT
vhDomain                  $VH_NAME
adminEmails               admin@epicpanel.local
enableGzip                1
enableIpGeo               0
errorlog $VH_ROOT/logs/ols-error.log {
  useServer               1
  logLevel                ERROR
}
accesslog $VH_ROOT/logs/ols-access.log {
  useServer               1
  rollingSize             10M
}

errorpage 508 {
  url                     /srv/epicpanel/default_pages/quota_exceeded.html
}

# quota: 508 "Limit Exceeded" for every request
context / {
  location                $VH_ROOT
  allowBrowse             0
  rewrite  {
    enable                1
    rewriteRule  ^/.*$  -  [R=508,L]
  }
}
`, v.WebsiteID)
}

// EnsureOLSSite writes the vhconf, registers the listener+mapper in
// httpd_config.conf, validates and restarts OLS. internalPort>0 = nginx edge.
func (e *Executor) EnsureOLSSite(ctx context.Context, v VhostSpec, internalPort int) error {
	return (&OLSProvider{Ex: e}).ensureSite(ctx, v, internalPort)
}

// ensureSite is the OLS engine. Both httpd_config.conf and the per-vhost
// vhconf are backed up in memory BEFORE any edit; if the restart fails both
// are restored, the restart is retried against the known-good state and the
// error is returned.
func (o *OLSProvider) ensureSite(ctx context.Context, v VhostSpec, internalPort int) error {
	if o.Ex == nil {
		return fmt.Errorf("openlitespeed provider requires an executor")
	}
	if internalPort <= 0 {
		return fmt.Errorf("openlitespeed backend port not allocated")
	}
	confPath := o.httpdConfigPath()
	if _, err := os.Stat(confPath); err != nil {
		return fmt.Errorf("openlitespeed is not installed")
	}
	// The port must be actually free (or already ours) before configuring.
	if err := verifyBackendPort(internalPort); err != nil {
		return fmt.Errorf("openlitespeed backend port %d: %w", internalPort, err)
	}
	// OLS defaults to running as nobody, which cannot traverse the 0750 site
	// trees. Align it with www-data — the same identity the ACL grants cover.
	o.normalizeOLSUser(ctx)
	vhDir := o.vhostDirPath()
	if err := os.MkdirAll(filepath.Join(vhDir, v.WebsiteID, "html"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(o.logsBaseDir(), v.WebsiteID, "logs"), 0o755); err != nil {
		return err
	}
	vhconf := filepath.Join(vhDir, v.WebsiteID, "vhconf.conf")

	// In-memory backups of BOTH files (mode preserved) before any edit.
	vhBackup, vhMode, hadVH := "", os.FileMode(0o644), false
	if b, err := os.ReadFile(vhconf); err == nil {
		vhBackup, hadVH = string(b), true
		if fi, statErr := os.Stat(vhconf); statErr == nil {
			vhMode = fi.Mode().Perm()
		}
	}
	confBytes, err := os.ReadFile(confPath)
	if err != nil {
		return err
	}
	confMode := os.FileMode(0o640)
	if fi, statErr := os.Stat(confPath); statErr == nil {
		confMode = fi.Mode().Perm()
	}
	confBackup := string(confBytes)

	rollback := func() {
		restoreFile(vhconf, []byte(vhBackup), hadVH, vhMode)
		_ = os.WriteFile(confPath, []byte(confBackup), confMode)
	}

	if err := AtomicWriteFile(vhconf, []byte(RenderOLSVhconf(v, internalPort)), 0o644); err != nil {
		return err
	}

	// The backend listener is ALWAYS loopback: nginx owns the public ports.
	port := fmt.Sprintf("%d", internalPort)
	bind := "127.0.0.1"
	names := make([]string, 0, len(v.Domains))
	for _, d := range v.Domains {
		names = append(names, d.Domain)
	}

	confStr := string(confBytes)
	marker := "virtualhost " + v.WebsiteID
	if !strings.Contains(confStr, marker) {
		block := fmt.Sprintf("\nvirtualhost %s {\n  vhRoot                  /srv/epicpanel/websites/%s/public\n  configFile              %s\n  allowSymbolLink         1\n  enableScript            1\n  restrained              0\n}\n\nlistener epicpanel-%s {\n  address %s:%s\n  secure 0\n  map %s %s\n}\n",
			v.WebsiteID, v.WebsiteID, vhconf, v.WebsiteID, bind, port, v.WebsiteID, strings.Join(names, ", "))
		confStr += block
	} else {
		// Update listener port/names minimally: replace the listener block.
		confStr = replaceOLSListener(confStr, v.WebsiteID, bind, port, names)
	}
	if err := AtomicWriteFile(confPath, []byte(confStr), confMode); err != nil {
		rollback()
		return err
	}

	// Validate + restart; rollback + retry on failure.
	if err := o.restartOLS(ctx); err != nil {
		rollback()
		_ = o.restartOLS(ctx)
		return fmt.Errorf("openlitespeed restart failed (config restored): %w", err)
	}
	return nil
}

// restartOLS reloads OpenLiteSpeed. lswsctrl spawns a daemon that inherits
// stdout/stderr, which makes CombinedOutput block forever — always go through
// systemd (fast-fail) and fall back to a detached lswsctrl.
func (o *OLSProvider) restartOLS(ctx context.Context) error {
	if o.Ex.run(ctx, "systemctl", "restart", "lsws") == nil {
		return nil
	}
	// No systemd unit: run lswsctrl detached from our pipes so Wait returns.
	ctrl := o.lswsctrlPath()
	if _, statErr := os.Stat(ctrl); statErr == nil {
		cmd := exec.Command(ctrl, "restart")
		cmd.Stdout = nil
		cmd.Stderr = nil
		cmd.Stdin = nil
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start lswsctrl: %w", err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("lswsctrl restart: %w", err)
			}
			return nil
		case <-time.After(3 * time.Second):
			return nil // daemonizing: assume the restart is under way
		}
	}
	return fmt.Errorf("restart openlitespeed: systemctl failed and lswsctrl is unavailable")
}

// replaceOLSListener swaps the managed listener block for the site.
func replaceOLSListener(conf, websiteID, bind, port string, names []string) string {
	start := strings.Index(conf, "listener epicpanel-"+websiteID+" {")
	if start == -1 {
		return conf
	}
	end := strings.Index(conf[start:], "\n}") // closing brace of listener
	if end == -1 {
		return conf
	}
	end = start + end + 2
	newBlock := fmt.Sprintf("listener epicpanel-%s {\n  address %s:%s\n  secure 0\n  map %s %s\n}\n",
		websiteID, bind, port, websiteID, strings.Join(names, ", "))
	return conf[:start] + newBlock + conf[end:]
}

// RemoveOLSSite unregisters the vhost + listener (idempotent).
func (e *Executor) RemoveOLSSite(ctx context.Context, websiteID string) {
	(&OLSProvider{Ex: e}).removeSite(ctx, websiteID)
}

// removeSite is the OLS removal engine.
func (o *OLSProvider) removeSite(ctx context.Context, websiteID string) {
	confPath := o.httpdConfigPath()
	conf, err := os.ReadFile(confPath)
	if err != nil {
		return
	}
	confStr := string(conf)
	// remove listener block
	start := strings.Index(confStr, "listener epicpanel-"+websiteID+" {")
	if start != -1 {
		end := strings.Index(confStr[start:], "\n}")
		if end != -1 {
			confStr = confStr[:start] + confStr[start+end+2:]
		}
	}
	// remove virtualhost block
	start = strings.Index(confStr, "virtualhost "+websiteID+" {")
	if start != -1 {
		end := strings.Index(confStr[start:], "\n}")
		if end != -1 {
			confStr = confStr[:start] + confStr[start+end+2:]
		}
	}
	if confStr != string(conf) {
		_ = os.WriteFile(confPath, []byte(confStr), 0o640)
	}
	_ = os.RemoveAll(filepath.Join(o.vhostDirPath(), websiteID))
	if o.Ex != nil {
		_ = o.restartOLS(ctx)
	}
}

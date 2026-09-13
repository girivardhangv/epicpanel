package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Executor performs typed infrastructure operations on the local machine.
// It never runs a shell: every command is exec.Command with an argument list,
// so payloads cannot inject commands. Operations are idempotent — re-running
// them after a partial failure converges to the desired state.
type Executor struct {
	docRootBase string
	ctx         context.Context
	pm          PackageManager
}

func NewExecutor() *Executor {
	return &Executor{docRootBase: "/srv/epicpanel/websites", ctx: context.Background(), pm: DetectPackageManager()}
}

type ProvisionPayload struct {
	WebsiteID      string          `json:"website_id"`
	Organization   string          `json:"organization"`
	Name           string          `json:"name"`
	UnixUser       string          `json:"unix_user"`
	Runtime        string          `json:"runtime"`
	RuntimeVersion string          `json:"runtime_version,omitempty"`
	WebServer      string          `json:"web_server,omitempty"`
	BackendPort    int             `json:"backend_port,omitempty"`
	DocrootSuffix  string          `json:"docroot_suffix,omitempty"`
	PrimaryDomain  string          `json:"primary_domain"`
	RewriteRules   string          `json:"rewrite_rules,omitempty"`
	Domains        []DomainPayload `json:"domains,omitempty"`
	// Redirects are domain-level redirects (301/302/307/308) rendered by the
	// selected web server(s).
	Redirects []RedirectRule `json:"redirects,omitempty"`
	// Suspended serves a plain 503 stub for every domain (no PHP, no proxy).
	Suspended bool `json:"suspended,omitempty"`
	// FPM pool sizing from the org's hosting package; zero = agent defaults.
	FpmMemoryLimitMB int `json:"fpm_memory_limit_mb,omitempty"`
	FpmMaxChildren   int `json:"fpm_max_children,omitempty"`
	// PHPSettings are validated per-site php.ini overrides (allowlisted by the
	// control plane) rendered as php_admin_value/flag lines in the FPM pool.
	PHPSettings map[string]string `json:"php_settings,omitempty"`
	// RequestTerminateTimeout caps a single PHP request (seconds; 0 = default).
	RequestTerminateTimeout int `json:"request_terminate_timeout,omitempty"`
}

// effectiveDocroot resolves the serving directory: siteBase/public, or
// siteBase/<suffix> when a validated override is set. The suffix is
// re-validated here (defense in depth) and its directory is created when
// missing so framework layouts (e.g. Laravel "public") serve cleanly.
func effectiveDocroot(siteBase, suffix string) (string, error) {
	docRoot := filepath.Join(siteBase, "public")
	if strings.TrimSpace(suffix) == "" {
		return docRoot, nil
	}
	clean := filepath.Clean("/" + suffix) // anchors and removes any ".."
	rel := strings.TrimPrefix(clean, "/")
	if rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid docroot suffix %q", suffix)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("invalid docroot suffix %q", suffix)
		}
	}
	effective := filepath.Join(siteBase, rel)
	if !strings.HasPrefix(effective, siteBase+string(filepath.Separator)) {
		return "", fmt.Errorf("docroot suffix escapes the site tree")
	}
	if _, err := os.Stat(effective); os.IsNotExist(err) {
		_ = os.MkdirAll(effective, 0o755)
	}
	return effective, nil
}

// DomainPayload carries per-domain serving config (aliases + ssl).
type DomainPayload struct {
	Domain   string `json:"domain"`
	SSLMode  string `json:"ssl_mode"`
	CertPath string `json:"cert_path,omitempty"`
	KeyPath  string `json:"key_path,omitempty"`
}

type ProvisionOutcome struct {
	UnixUser     string `json:"unix_user"`
	DocumentRoot string `json:"document_root"`
}

const maxUserID = 65534

// ProvisionWebsite creates the dedicated Unix user and the isolated directory
// structure for a website. Safe to retry: existing user/dirs are detected and
// reused rather than duplicated.
func (e *Executor) ProvisionWebsite(ctx context.Context, payload ProvisionPayload) (*ProvisionOutcome, error) {
	if !validUnixUserName(payload.UnixUser) {
		return nil, fmt.Errorf("invalid unix user name %q", payload.UnixUser)
	}
	if _, err := uuid.Parse(payload.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id: %w", err)
	}

	siteBase := filepath.Join(e.docRootBase, payload.WebsiteID)
	// Standard layout first (public/, logs/, tmp/ always exist); the serving
	// docroot then resolves to the override when set (e.g. Laravel public/).
	stdPublic := filepath.Join(siteBase, "public")
	docRoot, err2 := effectiveDocroot(siteBase, payload.DocrootSuffix)
	if err2 != nil {
		return nil, fmt.Errorf("docroot: %w", err2)
	}
	logDir := filepath.Join(siteBase, "logs")
	tmpDir := filepath.Join(siteBase, "tmp")

	if err := os.MkdirAll(siteBase, 0o750); err != nil {
		return nil, fmt.Errorf("create site base: %w", err)
	}
	for _, dir := range []string{stdPublic, logDir, tmpDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	if docRoot != stdPublic {
		// The override dir is served by nginx/apache: mirror the ACLs.
		_ = grantWebServerAccess(siteBase)
		_ = exec.Command("setfacl", "-m", "u:"+webServerUser+":r-x", docRoot).Run()
		_ = exec.Command("setfacl", "-d", "-m", "u:"+webServerUser+":r-x", docRoot).Run()
	}

	uid, gid, err := e.ensureUnixUser(payload.UnixUser)
	if err != nil {
		return nil, err
	}

	if err := chownRecursive(siteBase, uid, gid); err != nil {
		return nil, fmt.Errorf("chown site tree: %w", err)
	}

	if err := grantWebServerAccess(siteBase); err != nil {
		return nil, fmt.Errorf("grant web server access: %w", err)
	}

	// Placeholder only for a truly empty docroot. NEVER drop an index.html
	// next to an existing index.php/index.htm: Apache's DirectoryIndex
	// prefers index.html, which would shadow Laravel/WordPress front
	// controllers forever (idempotent: never overwrites user content).
	indexPath := filepath.Join(docRoot, "index.html")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		hasApp := false
		for _, alt := range []string{"index.php", "index.htm"} {
			if _, err := os.Stat(filepath.Join(docRoot, alt)); err == nil {
				hasApp = true
				break
			}
		}
		if !hasApp {
			page := fmt.Sprintf("<!doctype html><html><body><h1>%s</h1><p>Provisioned by EpicPanel.</p></body></html>\n", payload.Name)
			if err := os.WriteFile(indexPath, []byte(page), 0o644); err != nil {
				return nil, fmt.Errorf("write index.html: %w", err)
			}
			_ = os.Chown(indexPath, uid, gid)
		}
	}

	// PHP sites get an isolated FPM pool under the shared runtime binary.
	fpmSocket := ""
	if payload.Runtime == "php" {
		if payload.RuntimeVersion == "" {
			return nil, fmt.Errorf("php website %s has no runtime_version in desired state", payload.WebsiteID)
		}
		// Reconcile: drop this site's pool from any other PHP version first,
		// so stale masters cannot hold the site socket.
		if err := e.CleanupOtherVersionPools(ctx, payload.RuntimeVersion, payload.WebsiteID); err != nil {
			return nil, fmt.Errorf("cleanup stale fpm pools: %w", err)
		}
		pool := PoolSpec{
			WebsiteID:               payload.WebsiteID,
			UnixUser:                payload.UnixUser,
			RuntimeVer:              payload.RuntimeVersion,
			DocumentRoot:            docRoot,
			PrimaryDomain:           payload.PrimaryDomain,
			PMMaxChildren:           payload.FpmMaxChildren,
			PHPSettings:             payload.PHPSettings,
			RequestTerminateTimeout: payload.RequestTerminateTimeout,
		}
		if payload.FpmMemoryLimitMB > 0 {
			pool.ProcessMemory = fmt.Sprintf("%dM", payload.FpmMemoryLimitMB)
		}
		if err := e.EnsurePool(ctx, pool); err != nil {
			return nil, fmt.Errorf("ensure fpm pool: %w", err)
		}
		fpmSocket = fmt.Sprintf("%s/%s.sock", fpmSocketDir, payload.WebsiteID)
	}

	// Web server serving configuration. Multi web server support:
	//   nginx            — nginx serves directly
	//   apache           — Apache on :80 (nginx vhost removed)
	//   openlitespeed    — OLS on :80 (nginx vhost removed)
	//   nginx,apache     — nginx edge + proxy to Apache internal port
	//   nginx,openlitespeed — nginx edge + proxy to OLS internal port
	wservers := webServerList(payload.WebServer)
	if payload.WebServer == "" {
		wservers = []string{"nginx"} // default
	}
	if payload.WebServer == "none" {
		wservers = nil
	}

	domains := make([]DomainSpec, 0, len(payload.Domains))
	for _, d := range payload.Domains {
		domains = append(domains, DomainSpec{
			Domain:   d.Domain,
			SSLMode:  d.SSLMode,
			CertPath: d.CertPath,
			KeyPath:  d.KeyPath,
		})
	}
	// Backward compatibility: primary_domain only (no domains list).
	if len(domains) == 0 && payload.PrimaryDomain != "" {
		domains = append(domains, DomainSpec{Domain: payload.PrimaryDomain, SSLMode: "none"})
	}

	buildVhost := func() VhostSpec {
		return VhostSpec{
			WebsiteID:    payload.WebsiteID,
			UnixUser:     payload.UnixUser,
			DocumentRoot: docRoot,
			FpmSocket:    fpmSocket,
			RewriteRules: payload.RewriteRules,
			BackendPort:  payload.BackendPort,
			Redirects:    payload.Redirects,
			Suspended:    payload.Suspended,
			Domains:      domains,
		}
	}

	hasNginx := false
	hasApache := false
	hasOLS := false
	for _, w := range wservers {
		switch w {
		case "nginx":
			hasNginx = true
		case "apache":
			hasApache = true
		case "openlitespeed":
			hasOLS = true
		}
	}

	// nginx: direct serving, or edge+proxy when a backend web server is set.
	// The backend port comes from the control plane (persisted, unique per
	// server); only legacy payloads fall back to the hash-based port.
	if hasNginx || (!hasApache && !hasOLS) {
		if err := e.InstallNginx(ctx); err != nil {
			return nil, fmt.Errorf("install nginx: %w", err)
		}
		vhost := buildVhost()
		if hasApache {
			vhost.ProxyPass = fmt.Sprintf("http://127.0.0.1:%d", apachePort(payload))
		} else if hasOLS {
			vhost.ProxyPass = fmt.Sprintf("http://127.0.0.1:%d", olsPort(payload))
		}
		ng := &NginxProvider{}
		if err := ng.Ensure(ctx, vhost); err != nil {
			return nil, fmt.Errorf("ensure vhost: %w", err)
		}
	} else {
		// nginx not selected: remove its vhost so apache/OLS can bind :80.
		ng := &NginxProvider{}
		_ = ng.Remove(ctx, payload.WebsiteID)
	}
	if hasApache {
		if err := e.EnsureApacheSite(ctx, buildVhost(), apachePort(payload)); err != nil {
			return nil, fmt.Errorf("ensure apache site: %w", err)
		}
	} else {
		e.RemoveApacheSite(ctx, payload.WebsiteID)
	}
	if hasOLS {
		if err := e.EnsureOLSSite(ctx, buildVhost(), olsPort(payload)); err != nil {
			return nil, fmt.Errorf("ensure openlitespeed site: %w", err)
		}
	} else {
		e.RemoveOLSSite(ctx, payload.WebsiteID)
	}

	slog.Info("website provisioned", "website", payload.WebsiteID, "user", payload.UnixUser, "uid", uid, "docroot", docRoot, "runtime", payload.Runtime, "runtime_version", payload.RuntimeVersion, "web_servers", payload.WebServer)
	return &ProvisionOutcome{UnixUser: payload.UnixUser, DocumentRoot: docRoot}, nil
}

// DeleteWebsite removes the site directory tree and any FPM pool config for
// the site. Deleting a missing tree is a success (idempotent).
func (e *Executor) DeleteWebsite(ctx context.Context, payload ProvisionPayload) error {
	if _, err := uuid.Parse(payload.WebsiteID); err != nil {
		return fmt.Errorf("invalid website id: %w", err)
	}
	siteBase := filepath.Join(e.docRootBase, payload.WebsiteID)
	if err := os.RemoveAll(siteBase); err != nil {
		return fmt.Errorf("remove site tree: %w", err)
	}
	if payload.Runtime == "php" && payload.RuntimeVersion != "" {
		if err := e.RemovePool(ctx, payload.RuntimeVersion, payload.WebsiteID); err != nil {
			return fmt.Errorf("remove fpm pool: %w", err)
		}
	}
	if payload.WebServer != "none" {
		// nginx is present in every serving mode — remove its vhost.
		ng := NginxProvider{}
		if err := ng.Remove(ctx, payload.WebsiteID); err != nil {
			return fmt.Errorf("remove vhost: %w", err)
		}
		// Backend web servers (proxy modes): remove their configs. Backends
		// are shared services — only stopped when no site uses them anymore.
		switch {
		case strings.Contains(payload.WebServer, "apache"):
			e.RemoveApacheSite(ctx, payload.WebsiteID)
			e.stopApacheIfUnused(ctx)
		case strings.Contains(payload.WebServer, "openlitespeed"):
			e.RemoveOLSSite(ctx, payload.WebsiteID)
			e.stopOLSIfUnused(ctx)
		}
	}
	// The websites row is deleted by the control plane after this job, which
	// releases the backend port automatically (allocation is DB-driven).
	slog.Info("website deleted", "website", payload.WebsiteID, "user_kept", payload.UnixUser)
	return nil
}

// ensureUnixUser returns the UID/GID for name, creating the system user when
// missing (idempotent). Uses useradd with -r (system account, no aging).
func (e *Executor) ensureUnixUser(name string) (uid, gid int, err error) {
	if u, lookupErr := user.Lookup(name); lookupErr == nil {
		id, convErr := strconv.Atoi(u.Uid)
		if convErr != nil {
			return 0, 0, fmt.Errorf("parse uid for %s: %w", name, convErr)
		}
		gidInt, convErr := strconv.Atoi(u.Gid)
		if convErr != nil {
			return 0, 0, fmt.Errorf("parse gid for %s: %w", name, convErr)
		}
		return id, gidInt, nil
	}

	cmd := exec.CommandContext(e.ctx, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Another run may have created it concurrently; re-check.
		if u, lookupErr := user.Lookup(name); lookupErr == nil {
			id, _ := strconv.Atoi(u.Uid)
			gidInt, _ := strconv.Atoi(u.Gid)
			return id, gidInt, nil
		}
		return 0, 0, fmt.Errorf("useradd %s: %s (%w)", name, strings.TrimSpace(string(out)), err)
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, fmt.Errorf("lookup after useradd: %w", err)
	}
	id, _ := strconv.Atoi(u.Uid)
	gidInt, _ := strconv.Atoi(u.Gid)
	return id, gidInt, nil
}

func chownRecursive(root string, uid, gid int) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(path, uid, gid)
	})
}

// webServerUser is the user nginx workers run as. The site tree stays 0750
// (owner-only, per isolation/SECURITY.md), so the web server is granted
// access via POSIX ACLs instead of loosening directory modes.
const webServerUser = "www-data"

// grantWebServerAccess lets nginx traverse the site base and read the docroot
// without making the tree world-readable: the base gets traverse-only (--x,
// no listing), public gets r-x plus a default ACL so files the site user
// uploads later are served too. Falls back to loosening modes when setfacl is
// unavailable or the filesystem rejects ACLs; logs/ and tmp/ stay 0750.
func grantWebServerAccess(siteBase string) error {
	docRoot := filepath.Join(siteBase, "public")
	if _, err := exec.LookPath("setfacl"); err == nil {
		ok := true
		for _, args := range [][]string{
			{"-m", "u:" + webServerUser + ":--x", siteBase},
			{"-m", "u:" + webServerUser + ":r-x", docRoot},
			{"-d", "-m", "u:" + webServerUser + ":r-x", docRoot},
		} {
			if out, err := exec.Command("setfacl", args...).CombinedOutput(); err != nil {
				slog.Warn("setfacl failed, falling back to chmod", "args", args, "out", strings.TrimSpace(string(out)), "err", err)
				ok = false
				break
			}
		}
		if ok {
			return nil
		}
	}
	// Fallback (or ACLs rejected by the filesystem): open up traversal.
	// logs/ and tmp/ are not touched and stay 0750.
	if err := os.Chmod(siteBase, 0o711); err != nil {
		return fmt.Errorf("chmod site base: %w", err)
	}
	return os.Chmod(docRoot, 0o755)
}

func validUnixUserName(name string) bool {
	if len(name) < 2 || len(name) > 32 {
		return false
	}
	if !strings.HasPrefix(name, "ep-") {
		return false
	}
	for _, c := range name[3:] {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

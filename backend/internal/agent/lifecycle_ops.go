package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// WEBSITE LIFECYCLE OPS (Phase 4) — suspend_website / resume_website.
//
// Suspend flips every serving config that currently exists for a website to
// a static 503 "Account suspended" stub; resume restores the exact original
// config from a sibling <file>.epicpanel-suspend-bak backup. Site files,
// databases, DNS, FPM pools and certificates are never touched — only the
// web-server vhost files plus a reload/restart.
// ============================================================================

// LifecycleJobPayload matches the suspend_website / resume_website payload.
type LifecycleJobPayload struct {
	WebsiteID string `json:"website_id"`
}

// LifecycleOutcome is the job result reported back to the control plane.
type LifecycleOutcome struct {
	Suspended      bool     `json:"suspended,omitempty"`
	Resumed        bool     `json:"resumed,omitempty"`
	AlreadyInState bool     `json:"already_in_state,omitempty"`
	Providers      []string `json:"providers,omitempty"`
	Reloaded       []string `json:"reloaded,omitempty"`
	Notes          []string `json:"notes,omitempty"`
}

// lifeSuspendBakSuffix marks the on-disk backup of the pre-suspension config,
// written next to the live file.
const lifeSuspendBakSuffix = ".epicpanel-suspend-bak"

// lifePaths holds the injectable filesystem locations. Production defaults
// come from lifeProdPaths; tests point every field at a temp dir.
type lifePaths struct {
	NginxAvail    string
	NginxEnabled  string
	ApacheAvail   string
	ApacheEnabled string
	OLSVhosts     string
	OLSHTTPD      string
	LSWSCtrl      string // empty = OLSProvider default (tests point at a stub)
}

func lifeProdPaths() lifePaths {
	return lifePaths{
		NginxAvail:    nginxAvailable,
		NginxEnabled:  nginxEnabled,
		ApacheAvail:   apacheSitesAvail,
		ApacheEnabled: apacheSitesEnabled,
		OLSVhosts:     olsVhostDir,
		OLSHTTPD:      "/usr/local/lsws/conf/httpd_config.conf",
	}
}

// lifePathsOverride swaps the filesystem locations for tests (nil = prod).
var lifePathsOverride *lifePaths

func lifeActivePaths() lifePaths {
	if lifePathsOverride != nil {
		return *lifePathsOverride
	}
	return lifeProdPaths()
}

// SuspendWebsite replaces the serving config of every web server currently
// hosting the site with a 503 stub. Idempotent and reversible: an already
// suspended site is detected and left untouched, and the original config is
// kept in <file>.epicpanel-suspend-bak for ResumeWebsite.
func (e *Executor) SuspendWebsite(ctx context.Context, payload LifecycleJobPayload) (*LifecycleOutcome, error) {
	if _, err := uuid.Parse(payload.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id: %w", err)
	}
	return e.lifeSuspend(ctx, payload.WebsiteID, lifeActivePaths())
}

// ResumeWebsite restores the pre-suspension config of every web server that
// has a suspend backup. Idempotent: sites without a backup are no-ops.
func (e *Executor) ResumeWebsite(ctx context.Context, payload LifecycleJobPayload) (*LifecycleOutcome, error) {
	if _, err := uuid.Parse(payload.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id: %w", err)
	}
	return e.lifeResume(ctx, payload.WebsiteID, lifeActivePaths())
}

// lifeTarget is one provider's live config file + its suspend backup.
type lifeTarget struct {
	name    string // nginx | apache | openlitespeed
	conf    string
	bak     string
	enabled string // enabled symlink path ("" for OLS)
}

// lifeTargets detects which web servers currently serve the site purely from
// config file existence, in a deterministic order.
func lifeTargets(id string, p lifePaths) []lifeTarget {
	var out []lifeTarget
	ng := filepath.Join(p.NginxAvail, "epicpanel-"+id+".conf")
	if lifeFileExists(ng) {
		out = append(out, lifeTarget{
			name:    "nginx",
			conf:    ng,
			bak:     ng + lifeSuspendBakSuffix,
			enabled: filepath.Join(p.NginxEnabled, "epicpanel-"+id+".conf"),
		})
	}
	ap := filepath.Join(p.ApacheAvail, "epicpanel-"+id+".conf")
	if lifeFileExists(ap) {
		out = append(out, lifeTarget{
			name:    "apache",
			conf:    ap,
			bak:     ap + lifeSuspendBakSuffix,
			enabled: filepath.Join(p.ApacheEnabled, "epicpanel-"+id+".conf"),
		})
	}
	ol := filepath.Join(p.OLSVhosts, id, "vhconf.conf")
	if lifeFileExists(ol) {
		out = append(out, lifeTarget{name: "openlitespeed", conf: ol, bak: ol + lifeSuspendBakSuffix})
	}
	return out
}

func lifeFileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func lifeLinkExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// lifeEnsureSuspendBak writes the backup file unless one already exists (the
// first pre-suspension config is the one resume must restore). Returns true
// when a new backup was created.
func lifeEnsureSuspendBak(t lifeTarget, current string) (bool, error) {
	if lifeFileExists(t.bak) {
		return false, nil
	}
	f, err := os.OpenFile(t.bak, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return false, fmt.Errorf("create suspend backup %s: %w", t.bak, err)
	}
	if _, err := f.WriteString(current); err != nil {
		_ = f.Close()
		_ = os.Remove(t.bak)
		return false, fmt.Errorf("write suspend backup %s: %w", t.bak, err)
	}
	return true, f.Close()
}

func (e *Executor) lifeSuspend(ctx context.Context, id string, p lifePaths) (*LifecycleOutcome, error) {
	targets := lifeTargets(id, p)
	out := &LifecycleOutcome{}
	if len(targets) == 0 {
		out.AlreadyInState = true
		out.Notes = append(out.Notes, "no serving configs found; nothing to suspend")
		return out, nil
	}
	changed := false
	for _, t := range targets {
		out.Providers = append(out.Providers, t.name)
		already, reloaded, notes, err := e.lifeSuspendTarget(ctx, id, t, p)
		if err != nil {
			return nil, err
		}
		out.Notes = append(out.Notes, notes...)
		if reloaded {
			out.Reloaded = append(out.Reloaded, t.name)
		}
		if !already {
			changed = true
		}
	}
	out.Suspended = changed
	out.AlreadyInState = !changed
	return out, nil
}

func (e *Executor) lifeResume(ctx context.Context, id string, p lifePaths) (*LifecycleOutcome, error) {
	targets := lifeTargets(id, p)
	out := &LifecycleOutcome{}
	if len(targets) == 0 {
		out.AlreadyInState = true
		out.Notes = append(out.Notes, "no serving configs found; nothing to resume")
		return out, nil
	}
	resumed := false
	for _, t := range targets {
		out.Providers = append(out.Providers, t.name)
		got, reloaded, notes, err := e.lifeResumeTarget(ctx, id, t, p)
		if err != nil {
			return nil, err
		}
		out.Notes = append(out.Notes, notes...)
		if reloaded {
			out.Reloaded = append(out.Reloaded, t.name)
		}
		if got {
			resumed = true
		}
	}
	out.Resumed = resumed
	out.AlreadyInState = !resumed
	return out, nil
}

func (e *Executor) lifeSuspendTarget(ctx context.Context, id string, t lifeTarget, p lifePaths) (already, reloaded bool, notes []string, err error) {
	switch t.name {
	case "nginx":
		return e.lifeSuspendNginx(ctx, id, t, p)
	case "apache":
		return e.lifeSuspendApache(ctx, id, t, p)
	default:
		return e.lifeSuspendOLS(ctx, id, t, p)
	}
}

func (e *Executor) lifeResumeTarget(ctx context.Context, id string, t lifeTarget, p lifePaths) (resumed, reloaded bool, notes []string, err error) {
	switch t.name {
	case "nginx":
		return e.lifeResumeNginx(ctx, t)
	case "apache":
		return e.lifeResumeApache(ctx, t)
	default:
		return e.lifeResumeOLS(ctx, t, p)
	}
}

// ============================================================================
// nginx
// ============================================================================

// lifeSuspendNginx backs up the vhost, swaps in the suspended stub under
// nginx -t validation and reloads. A missing nginx binary means the config
// file is an orphan: the stub is written without validation or reload.
func (e *Executor) lifeSuspendNginx(ctx context.Context, id string, t lifeTarget, p lifePaths) (bool, bool, []string, error) {
	current, err := os.ReadFile(t.conf)
	if err != nil {
		return false, false, nil, fmt.Errorf("read %s: %w", t.conf, err)
	}
	stub, err := lifeNginxStub(id, string(current))
	if err != nil {
		return false, false, nil, err
	}
	if string(current) == stub {
		return true, false, nil, nil
	}
	newBak, err := lifeEnsureSuspendBak(t, string(current))
	if err != nil {
		return false, false, nil, err
	}
	notes := []string{}
	if _, lookErr := exec.LookPath("nginx"); lookErr != nil {
		if err := AtomicWriteFile(t.conf, []byte(stub), 0o644); err != nil {
			return false, false, nil, err
		}
		notes = append(notes, "nginx binary missing; stub written without validation or reload (orphan config)")
		return false, false, notes, nil
	}
	wasEnabled := lifeLinkExists(t.enabled)
	if err := enableSiteFile(t.enabled, t.conf); err != nil {
		return false, false, nil, err
	}
	if err := SwapValidated(t.conf, []byte(stub), 0o644, func() error {
		return ValidateCmd(ctx, 120*time.Second, "nginx", "-t")
	}); err != nil {
		if !wasEnabled {
			_ = os.Remove(t.enabled)
		}
		if newBak {
			_ = os.Remove(t.bak)
		}
		return false, false, nil, fmt.Errorf("suspend nginx: %w", err)
	}
	if err := reloadNginx(ctx); err != nil {
		return false, false, nil, fmt.Errorf("suspend nginx reload: %w", err)
	}
	return false, true, notes, nil
}

// lifeResumeNginx restores the backup under nginx -t validation, removes the
// backup and reloads. Missing backup + live config = already resumed; both
// missing = the site was deleted meanwhile (no-op).
func (e *Executor) lifeResumeNginx(ctx context.Context, t lifeTarget) (bool, bool, []string, error) {
	bak, err := os.ReadFile(t.bak)
	if errors.Is(err, os.ErrNotExist) {
		if lifeFileExists(t.conf) {
			return false, false, nil, nil
		}
		return false, false, []string{"nginx config and backup absent; nothing to resume"}, nil
	}
	if err != nil {
		return false, false, nil, fmt.Errorf("read %s: %w", t.bak, err)
	}
	current, _ := os.ReadFile(t.conf)
	if string(current) == string(bak) {
		_ = os.Remove(t.bak)
		return false, false, []string{"suspend backup already matches live config; backup removed"}, nil
	}
	if _, lookErr := exec.LookPath("nginx"); lookErr != nil {
		if err := AtomicWriteFile(t.conf, bak, 0o644); err != nil {
			return false, false, nil, err
		}
		_ = os.Remove(t.bak)
		return true, false, []string{"nginx binary missing; backup restored without validation or reload (orphan config)"}, nil
	}
	if err := SwapValidated(t.conf, bak, 0o644, func() error {
		return ValidateCmd(ctx, 120*time.Second, "nginx", "-t")
	}); err != nil {
		return false, false, nil, fmt.Errorf("resume nginx: %w", err)
	}
	_ = os.Remove(t.bak)
	if err := reloadNginx(ctx); err != nil {
		return false, false, nil, fmt.Errorf("resume nginx reload: %w", err)
	}
	return true, true, nil, nil
}

// ============================================================================
// apache
// ============================================================================

// lifeSuspendApache backs up the vhost, swaps in the suspended stub under
// apache2ctl configtest validation and reloads. The stub keeps the original
// loopback Listen port so the edge-proxy contract is unchanged.
func (e *Executor) lifeSuspendApache(ctx context.Context, id string, t lifeTarget, p lifePaths) (bool, bool, []string, error) {
	current, err := os.ReadFile(t.conf)
	if err != nil {
		return false, false, nil, fmt.Errorf("read %s: %w", t.conf, err)
	}
	stub, err := lifeApacheStub(id, string(current))
	if err != nil {
		return false, false, nil, err
	}
	if string(current) == stub {
		return true, false, nil, nil
	}
	newBak, err := lifeEnsureSuspendBak(t, string(current))
	if err != nil {
		return false, false, nil, err
	}
	notes := []string{}
	if _, lookErr := exec.LookPath("apache2ctl"); lookErr != nil {
		if err := AtomicWriteFile(t.conf, []byte(stub), 0o644); err != nil {
			return false, false, nil, err
		}
		notes = append(notes, "apache2ctl missing; stub written without validation or reload (orphan config)")
		return false, false, notes, nil
	}
	wasEnabled := lifeLinkExists(t.enabled)
	if err := enableSiteFile(t.enabled, t.conf); err != nil {
		return false, false, nil, err
	}
	if err := SwapValidated(t.conf, []byte(stub), 0o644, func() error {
		return ValidateCmd(ctx, 120*time.Second, "apache2ctl", "configtest")
	}); err != nil {
		if !wasEnabled {
			_ = os.Remove(t.enabled)
		}
		if newBak {
			_ = os.Remove(t.bak)
		}
		return false, false, nil, fmt.Errorf("suspend apache: %w", err)
	}
	if err := e.run(ctx, "systemctl", "reload", "apache2"); err != nil {
		return false, false, nil, fmt.Errorf("suspend apache reload: %w", err)
	}
	return false, true, notes, nil
}

// lifeResumeApache mirrors lifeResumeNginx with apache2ctl configtest.
func (e *Executor) lifeResumeApache(ctx context.Context, t lifeTarget) (bool, bool, []string, error) {
	bak, err := os.ReadFile(t.bak)
	if errors.Is(err, os.ErrNotExist) {
		if lifeFileExists(t.conf) {
			return false, false, nil, nil
		}
		return false, false, []string{"apache config and backup absent; nothing to resume"}, nil
	}
	if err != nil {
		return false, false, nil, fmt.Errorf("read %s: %w", t.bak, err)
	}
	current, _ := os.ReadFile(t.conf)
	if string(current) == string(bak) {
		_ = os.Remove(t.bak)
		return false, false, []string{"suspend backup already matches live config; backup removed"}, nil
	}
	if _, lookErr := exec.LookPath("apache2ctl"); lookErr != nil {
		if err := AtomicWriteFile(t.conf, bak, 0o644); err != nil {
			return false, false, nil, err
		}
		_ = os.Remove(t.bak)
		return true, false, []string{"apache2ctl missing; backup restored without validation or reload (orphan config)"}, nil
	}
	if err := SwapValidated(t.conf, bak, 0o644, func() error {
		return ValidateCmd(ctx, 120*time.Second, "apache2ctl", "configtest")
	}); err != nil {
		return false, false, nil, fmt.Errorf("resume apache: %w", err)
	}
	_ = os.Remove(t.bak)
	if err := e.run(ctx, "systemctl", "reload", "apache2"); err != nil {
		return false, false, nil, fmt.Errorf("resume apache reload: %w", err)
	}
	return true, true, nil, nil
}

// ============================================================================
// openlitespeed
// ============================================================================

// lifeSuspendOLS backs up the vhconf, swaps in the suspended stub and
// restarts OLS (no validator exists for OLS). On restart failure the original
// content is restored and the restart retried, mirroring EnsureOLSSite.
func (e *Executor) lifeSuspendOLS(ctx context.Context, id string, t lifeTarget, p lifePaths) (bool, bool, []string, error) {
	current, err := os.ReadFile(t.conf)
	if err != nil {
		return false, false, nil, fmt.Errorf("read %s: %w", t.conf, err)
	}
	stub := RenderOLSVhconf(VhostSpec{WebsiteID: id, Suspended: true}, 0)
	if string(current) == stub {
		return true, false, nil, nil
	}
	newBak, err := lifeEnsureSuspendBak(t, string(current))
	if err != nil {
		return false, false, nil, err
	}
	notes := []string{}
	httpd, herr := os.ReadFile(p.OLSHTTPD)
	if herr != nil {
		if err := AtomicWriteFile(t.conf, []byte(stub), 0o644); err != nil {
			return false, false, nil, err
		}
		notes = append(notes, "httpd_config.conf missing; stub written without restart (orphan config)")
		return false, false, notes, nil
	}
	// Strictness guard: a registered OLS vhost must have a listener mapping
	// domains, or the config was not agent-rendered.
	if domains := lifeParseOLSDomains(string(httpd), id); len(domains) == 0 {
		return false, false, nil, fmt.Errorf("suspend openlitespeed: no domains mapped for site %s in %s (config not agent-rendered)", id, p.OLSHTTPD)
	}
	if err := AtomicWriteFile(t.conf, []byte(stub), 0o644); err != nil {
		return false, false, nil, err
	}
	prov := &OLSProvider{Ex: e, HTTPDConfig: p.OLSHTTPD, VhostDir: p.OLSVhosts, LSWSCtrl: p.LSWSCtrl}
	if err := prov.restartOLS(ctx); err != nil {
		restoreFile(t.conf, current, true, 0o644)
		_ = prov.restartOLS(ctx)
		if newBak {
			_ = os.Remove(t.bak)
		}
		return false, false, nil, fmt.Errorf("suspend openlitespeed: %w", err)
	}
	return false, true, notes, nil
}

// lifeResumeOLS restores the vhconf backup and restarts OLS with the same
// in-memory rollback contract as EnsureOLSSite (stub content restored and the
// restart retried when the restored config fails to come up).
func (e *Executor) lifeResumeOLS(ctx context.Context, t lifeTarget, p lifePaths) (bool, bool, []string, error) {
	bak, err := os.ReadFile(t.bak)
	if errors.Is(err, os.ErrNotExist) {
		if lifeFileExists(t.conf) {
			return false, false, nil, nil
		}
		return false, false, []string{"openlitespeed config and backup absent; nothing to resume"}, nil
	}
	if err != nil {
		return false, false, nil, fmt.Errorf("read %s: %w", t.bak, err)
	}
	prev, hadPrev := "", false
	if b, rerr := os.ReadFile(t.conf); rerr == nil {
		prev, hadPrev = string(b), true
	}
	if prev == string(bak) {
		_ = os.Remove(t.bak)
		return false, false, []string{"suspend backup already matches live config; backup removed"}, nil
	}
	if _, herr := os.Stat(p.OLSHTTPD); herr != nil {
		if err := AtomicWriteFile(t.conf, bak, 0o644); err != nil {
			return false, false, nil, err
		}
		_ = os.Remove(t.bak)
		return true, false, []string{"httpd_config.conf missing; backup restored without restart (orphan config)"}, nil
	}
	if err := AtomicWriteFile(t.conf, bak, 0o644); err != nil {
		return false, false, nil, err
	}
	prov := &OLSProvider{Ex: e, HTTPDConfig: p.OLSHTTPD, VhostDir: p.OLSVhosts, LSWSCtrl: p.LSWSCtrl}
	if err := prov.restartOLS(ctx); err != nil {
		// Roll back to the pre-resume state (the stub) and retry the restart.
		restoreFile(t.conf, []byte(prev), hadPrev, 0o644)
		_ = prov.restartOLS(ctx)
		return false, false, nil, fmt.Errorf("resume openlitespeed: %w", err)
	}
	_ = os.Remove(t.bak)
	return true, true, nil, nil
}

// ============================================================================
// Strict parsers over agent-rendered config formats + stub rendering.
// ============================================================================

var (
	lifeNginxServerNameRe = regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)
	lifeApacheNameRe      = regexp.MustCompile(`(?m)^\s*ServerName\s+(.+?)\s*$`)
	lifeApacheDocrootRe   = regexp.MustCompile(`(?m)^\s*DocumentRoot\s+(\S+)\s*$`)
)

// lifeParseNginxDomains extracts every server_name token from an
// agent-rendered nginx vhost (plain, redirect and TLS blocks).
func lifeParseNginxDomains(conf string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range lifeNginxServerNameRe.FindAllStringSubmatch(conf, -1) {
		for _, tok := range strings.Fields(m[1]) {
			d := strings.ToLower(strings.TrimSpace(tok))
			if d == "" || seen[d] || !validDomainName(d) {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// lifeParseApacheDomains extracts all names from ServerName directives (the
// agent renderer joins primary + aliases on one line).
func lifeParseApacheDomains(conf string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range lifeApacheNameRe.FindAllStringSubmatch(conf, -1) {
		for _, tok := range strings.Fields(m[1]) {
			d := strings.ToLower(strings.TrimSpace(tok))
			if d == "" || seen[d] || !validDomainName(d) {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// lifeParseOLSDomains extracts the mapped domains of the site's listener
// block from httpd_config.conf ("map epicpanel-<id> a.test, b.test").
func lifeParseOLSDomains(httpdConf, websiteID string) []string {
	re := regexp.MustCompile(`(?m)^\s*map\s+` + regexp.QuoteMeta(websiteID) + `\s+(.+?)\s*$`)
	m := re.FindStringSubmatch(httpdConf)
	if m == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.Split(m[1], ",") {
		d := strings.ToLower(strings.TrimSpace(tok))
		if d == "" || seen[d] || !validDomainName(d) {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// lifeNginxStub renders the suspended nginx vhost for the domains parsed from
// the current agent-rendered config. Zero parsed domains means the config was
// not written by EpicPanel — refuse (retryable) rather than guess.
func lifeNginxStub(id, current string) (string, error) {
	domains := lifeParseNginxDomains(current)
	if len(domains) == 0 {
		return "", fmt.Errorf("suspend nginx: no server_name found in %s (config not agent-rendered)", currentName(id))
	}
	spec := VhostSpec{WebsiteID: id, Suspended: true}
	for _, d := range domains {
		spec.Domains = append(spec.Domains, DomainSpec{Domain: d})
	}
	return RenderVhost(spec), nil
}

// lifeApacheStub renders the suspended Apache vhost, preserving the loopback
// Listen port and DocumentRoot of the current agent-rendered config.
func lifeApacheStub(id, current string) (string, error) {
	domains := lifeParseApacheDomains(current)
	if len(domains) == 0 {
		return "", fmt.Errorf("suspend apache: no ServerName found in vhost %s (config not agent-rendered)", id)
	}
	port := apacheListenPort(current)
	docRoot := "/srv/epicpanel/websites/" + id + "/public"
	if m := lifeApacheDocrootRe.FindStringSubmatch(current); m != nil {
		docRoot = m[1]
	}
	spec := VhostSpec{WebsiteID: id, Suspended: true, DocumentRoot: docRoot, BackendPort: port}
	for _, d := range domains {
		spec.Domains = append(spec.Domains, DomainSpec{Domain: d})
	}
	return RenderApacheSite(spec, port), nil
}

// currentName is a tiny helper for error messages.
func currentName(id string) string { return "epicpanel-" + id + ".conf" }

var _ = slog.Default // keep slog import if unused after refactors

package websites

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

// Settings keys holding the configurable hosting roots.
const (
	KeySitesRootLinux   = "websites.sites_root_linux"
	KeySitesRootWindows = "websites.sites_root_windows"
)

// Default sites roots. The prefix is a fixed convention (aaPanel-style
// /www/wwwroot on Linux, C:\www\wwwroot on Windows); the per-site directory
// is derived from the validated domain slug. Both remain overridable via
// system settings, but the panel UI no longer exposes free-form paths.
const (
	DefaultSitesRootLinux   = "/www/wwwroot"
	DefaultSitesRootWindows = `C:\www\wwwroot`
)

// SiteUser is the shared unprivileged account that owns site files on Linux
// (one user for all sites in this phase; per-site users are a later step).
const SiteUser = "epicpanel-sites"

// PathPlan is the resolved filesystem layout for one website, expressed in
// the AGENT's OS syntax (paths live on the managed server, not the panel).
type PathPlan struct {
	SiteRoot   string // e.g. /srv/panel/sites/example.com
	PublicDir  string // document root served by nginx
	LogsDir    string
	TmpDir     string
	PrivateDir string
	AccessLog  string
	ErrorLog   string
}

var winDriveRe = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func sepFor(agentOS string) string {
	if agentOS == "windows" {
		return "\\"
	}
	return "/"
}

// joinAgentPath concatenates elements with the agent OS separator. Elements
// must never contain separators themselves (domains are validated upstream).
func joinAgentPath(agentOS string, elems ...string) string {
	sep := sepFor(agentOS)
	return strings.Join(elems, sep)
}

// cleanRoot normalizes a sites root for the target OS: trims separators and
// verifies absolute form ("/..." on Linux, "X:\..." on Windows).
func cleanRoot(sitesRoot, agentOS string) (string, error) {
	r := strings.TrimRight(strings.TrimSpace(sitesRoot), `/\`)
	if r == "" {
		return "", errors.New("sites root is not configured")
	}
	if agentOS == "windows" {
		if !winDriveRe.MatchString(r + "\\") {
			return "", errors.New("sites root must be an absolute Windows path (e.g. C:\\Panel\\Sites)")
		}
		return filepath.ToSlash(r), nil // keep as given; separators normalized below
	}
	if !strings.HasPrefix(r, "/") {
		return "", errors.New("sites root must be an absolute path")
	}
	return r, nil
}

// Slug returns the filesystem-safe directory name for a domain: the validated
// domain with wildcards made explicit and stray characters replaced.
func Slug(domain string) string {
	s := strings.ToLower(domain)
	s = strings.ReplaceAll(s, "*.", "wildcard.")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// SitesRoot returns the configured hosting root for the given OS family.
func SitesRoot(agentOS, linuxVal, windowsVal string) string {
	if agentOS == "windows" {
		return firstNonEmpty(windowsVal, DefaultSitesRootWindows)
	}
	return firstNonEmpty(linuxVal, DefaultSitesRootLinux)
}

// ResolveSitePaths builds the layout for a domain under sitesRoot and proves
// the result cannot escape it. domain must already be validated; defense in
// depth rejects separators here anyway.
func ResolveSitePaths(sitesRoot, domain, agentOS string) (PathPlan, error) {
	if domain == "" {
		return PathPlan{}, errors.New("domain is required")
	}
	if strings.ContainsAny(domain, `/\`) || strings.Contains(domain, "..") || strings.Contains(domain, "*") {
		return PathPlan{}, errors.New("domain may not contain path separators or wildcards")
	}
	root, err := cleanRoot(sitesRoot, agentOS)
	if err != nil {
		return PathPlan{}, err
	}
	// Normalize root separators to the agent OS style.
	root = strings.ReplaceAll(root, "/", sepFor(agentOS))

	slug := Slug(domain)
	siteRoot := joinAgentPath(agentOS, root, slug)

	plan := PathPlan{
		SiteRoot:   siteRoot,
		PublicDir:  joinAgentPath(agentOS, siteRoot, "public"),
		LogsDir:    joinAgentPath(agentOS, siteRoot, "logs"),
		TmpDir:     joinAgentPath(agentOS, siteRoot, "tmp"),
		PrivateDir: joinAgentPath(agentOS, siteRoot, "private"),
		AccessLog:  joinAgentPath(agentOS, siteRoot, "logs", "access.log"),
		ErrorLog:   joinAgentPath(agentOS, siteRoot, "logs", "error.log"),
	}
	// Prefix proof on a normalized (forward-slash) form.
	norm := func(p string) string { return strings.ReplaceAll(strings.ReplaceAll(p, "\\", "/"), "//", "/") }
	if !strings.HasPrefix(norm(siteRoot)+"/", norm(root)+"/") {
		return PathPlan{}, errors.New("resolved path escapes the sites root")
	}
	return plan, nil
}

// ValidateDocumentRootOverride accepts an explicit document root only when it
// stays inside the site directory (operators may serve a subdirectory).
func ValidateDocumentRootOverride(sitesRoot, domain, override, agentOS string) (string, error) {
	trimmed := strings.TrimSpace(override)
	if trimmed == "" {
		return "", nil
	}
	plan, err := ResolveSitePaths(sitesRoot, domain, agentOS)
	if err != nil {
		return "", err
	}
	norm := func(p string) string { return strings.ReplaceAll(p, "\\", "/") }
	clean := strings.TrimRight(norm(trimmed), "/")
	root := norm(plan.SiteRoot)
	if !strings.HasPrefix(clean+"/", root+"/") {
		return "", errors.New("document root must stay inside the site directory")
	}
	if !filepath.IsAbs(strings.ReplaceAll(clean, "/", sepFor(agentOS))) && sepFor(agentOS) == "/" && !strings.HasPrefix(clean, "/") {
		return "", errors.New("document root override must be an absolute path")
	}
	if sepFor(agentOS) == "\\" && !winDriveRe.MatchString(clean) {
		return "", errors.New("document root override must be an absolute Windows path")
	}
	return strings.ReplaceAll(clean, "/", sepFor(agentOS)), nil
}

// LogsForSite derives the access/error log paths from a stored document root
// (.../public -> .../logs/...). Works for both OS syntaxes.
func LogsForSite(documentRoot string) (access, errorLog string, ok bool) {
	norm := strings.ReplaceAll(documentRoot, "\\", "/")
	sep := "/"
	if strings.Contains(documentRoot, "\\") {
		sep = "\\"
	}
	if !strings.HasSuffix(norm, "/public") {
		return "", "", false
	}
	base := strings.TrimSuffix(norm, "/public")
	base = strings.TrimSuffix(base, "/")
	base = strings.ReplaceAll(base, "/", sep)
	return base + sep + "logs" + sep + "access.log", base + sep + "logs" + sep + "error.log", true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

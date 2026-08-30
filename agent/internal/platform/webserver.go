package platform

import "context"

// SiteUser is the shared unprivileged system account that owns website files
// and runs FPM pools on Linux. Windows relies on inherited ACLs instead
// (documented isolation difference).
const SiteUser = "epicpanel-sites"

// DefaultSitesRoot returns the platform default hosting root.
func DefaultSitesRoot() string { return defaultSitesRoot() }

// NginxStatus reports detection of the web server. Installed=false is a
// normal, reportable state — never an error.
type NginxStatus struct {
	Installed  bool   `json:"installed"`
	Version    string `json:"version"`
	ConfigPath string `json:"config_path"`
	Running    bool   `json:"running"`
	Style      string `json:"style"` // sites-enabled | conf.d | custom
}

// NginxSite is one managed server block.
type NginxSite struct {
	Name    string // filesystem-safe slug; becomes <name>.conf
	Content string // full server block text
	Enable  bool
}

// NginxDeployResult reports the validate-and-commit outcome. The contract is
// fail-safe: when validation fails the previous configuration is restored and
// Deployed stays false.
type NginxDeployResult struct {
	Deployed      bool   `json:"deployed"`
	Validated     bool   `json:"validated"`
	ValidationOut string `json:"validation_output,omitempty"`
	RolledBack    bool   `json:"rolled_back,omitempty"`
}

// WebServerOps is the typed web-server control surface exposed to the panel.
// Implementations live in webserver_linux.go / webserver_windows.go.
type WebServerOps interface {
	Status(ctx context.Context) (*NginxStatus, error)
	DeploySite(ctx context.Context, site NginxSite) (*NginxDeployResult, error)
	RemoveSite(ctx context.Context, name string) error
	SetEnabled(ctx context.Context, name string, enable bool) error
	Reload(ctx context.Context) error
}

// validSiteName enforces the slug contract shared by every implementation:
// lowercase letters, digits, dots and hyphens only — the panel derives it
// from a validated domain, but the agent never trusts that.
func validSiteName(name string) bool {
	return ValidSiteSlug(name)
}

// ValidSiteSlug is the exported slug validator used by ops handlers.
func ValidSiteSlug(name string) bool {
	if name == "" || len(name) > 200 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
		default:
			return false
		}
	}
	return name[0] != '-' && name[0] != '.' && !contains(name, "..")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// parseNginxVersion extracts "1.24.0" from `nginx version: nginx/1.24.0`.
func parseNginxVersion(out string) (string, bool) {
	for _, line := range splitLines(out) {
		if i := indexOf(line, "nginx/"); i >= 0 {
			v := trimSpace(line[i+len("nginx/"):])
			if j := indexAny(v, " \t"); j > 0 {
				v = v[:j]
			}
			if v != "" {
				return v, true
			}
		}
	}
	return "", false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func indexAny(s, chars string) int {
	for i := 0; i < len(s); i++ {
		for _, c := range chars {
			if s[i] == byte(c) {
				return i
			}
		}
	}
	return -1
}

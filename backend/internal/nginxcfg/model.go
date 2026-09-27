package nginxcfg

// SiteConfig is the structured, context-aware per-site customization
// document (schema 2). Stored as JSONB in website_configs.config_json and
// carried verbatim in the provision payload. Every field is validated
// (ValidateSiteConfig) on the control plane at save time and re-validated
// by the agent at render time.
type SiteConfig struct {
	Schema int `json:"schema"`

	// ServerDirectives is the raw "Advanced" server-context snippet
	// (successor of the legacy rewrite_rules textarea). Lines are
	// validated against the CtxServer registry.
	ServerDirectives string `json:"server_directives,omitempty"`

	// RootLocation customizes EpicPanel's managed `location /` (the
	// try_files override WordPress/Laravel/SPA patterns need).
	RootLocation *RootLocationCfg `json:"root_location,omitempty"`

	// Locations are custom location blocks; EpicPanel generates the
	// `location <modifier> <match> { ... }` wrapper.
	Locations []CustomLocation `json:"locations,omitempty"`

	// Headers renders server-level add_header lines.
	Headers []HeaderRule `json:"headers,omitempty"`

	// IPAccess renders ordered allow/deny lines plus the default action.
	IPAccess *IPAccess `json:"ip_access,omitempty"`

	// ErrorPages overrides the panel's default status pages for the given
	// codes (the managed default for an overridden code is dropped).
	ErrorPages []ErrorPageRule `json:"error_pages,omitempty"`

	// ClientMaxBodySize is a validated nginx size ("32m"); rendered at
	// server level and inside the managed proxy block (proxy mode).
	ClientMaxBodySize string `json:"client_max_body_size,omitempty"`

	// AssetCaching generates one regex location caching static assets.
	AssetCaching *AssetCaching `json:"asset_caching,omitempty"`

	// Redirects are path-level redirects rendered as `rewrite` lines.
	// Whole-domain redirects (www↔non-www, domain→domain) are handled by
	// the dedicated domain-redirect feature, not here.
	Redirects []PathRedirect `json:"redirects,omitempty"`
}

// RootLocationCfg customizes the managed `location /` block.
type RootLocationCfg struct {
	// TryFiles replaces the managed default (`$uri $uri/ =404` for
	// file sites). Validated token list, e.g. `$uri $uri/ /index.php?$query_string`.
	TryFiles string `json:"try_files,omitempty"`
	// Extra holds additional location-context lines rendered inside the
	// managed location (headers, access control…). In proxy mode only a
	// safe subset is accepted (see proxySafeDirectives).
	Extra string `json:"extra,omitempty"`
}

// CustomLocation is one generated location block.
type CustomLocation struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`  // prefix | exact | regex | regex_nocase
	Match string `json:"match"` // prefix/exact: URI starting with /; regex: single regex token
	Body  string `json:"body"`  // location-context directive lines
}

// HeaderRule is one server-level add_header.
type HeaderRule struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Always bool   `json:"always,omitempty"`
}

// IPAccess is the ordered allow/deny list with a default action.
type IPAccess struct {
	Rules  []IPRule `json:"rules"`
	// Default is what unlisted sources get: "allow" or "deny".
	Default string `json:"default"`
}

// IPRule is one allow/deny entry (single host, CIDR, or "all").
type IPRule struct {
	CIDR   string `json:"cidr"`
	Action string `json:"action"` // allow | deny
}

// ErrorPageRule maps one status code to an internal error URI.
type ErrorPageRule struct {
	Status int    `json:"status"`
	URI    string `json:"uri"`
}

// AssetCaching caches static asset classes in one regex location.
type AssetCaching struct {
	// Classes: css, js, img, fonts, media (mapped to extensions at render).
	Classes []string `json:"classes"`
	// Expires is a validated nginx time ("30d", "7d", "1h"…).
	Expires string `json:"expires"`
}

// PathRedirect is one path-level redirect rendered as a rewrite.
type PathRedirect struct {
	// From is a URI path (rendered anchored: ^/old) or a full regex
	// token (when it starts with ^).
	From string `json:"from"`
	// To is a URI path or a URI with captures ($1…).
	To    string `json:"to"`
	// Code: 301 (permanent) or 302 (temporary).
	Code int `json:"code"`
}

// LocationKinds are the supported custom-location kinds.
const (
	KindPrefix     = "prefix"
	KindExact      = "exact"
	KindRegex      = "regex"
	KindRegexNoCase = "regex_nocase"
)

// AssetClasses maps the friendly class names to file extensions.
var AssetClasses = map[string][]string{
	"css":   {"css"},
	"js":    {"js", "mjs"},
	"img":   {"png", "jpg", "jpeg", "gif", "svg", "webp", "avif", "ico"},
	"fonts": {"woff", "woff2", "ttf", "otf", "eot"},
	"media": {"mp4", "webm", "mp3", "ogg", "wav"},
}

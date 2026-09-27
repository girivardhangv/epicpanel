// Package nginxcfg is the shared, context-aware nginx configuration core
// for EpicPanel's per-site customization system (ADR-068).
//
// It is imported by BOTH sides of the desired-state boundary:
//
//   - the control plane validates customer input at save time (API layer);
//   - the agent re-validates stored content at render time (defense in
//     depth — the agent never trusts control-plane state).
//
// The model is deliberately line/token oriented: nginx configuration is
// not fully parsed here (no quoting, no nested blocks beyond `if`), every
// accepted line is a single-token-per-argument directive, and the final
// authority for a candidate vhost remains `nginx -t` before reload.
package nginxcfg

import (
	"fmt"
)

// Ctx is a bitmask of nginx configuration contexts a directive may appear
// in. Contexts are the subset customers can reach through EpicPanel
// (server block, location block, if-in-location/if-in-server).
type Ctx uint8

const (
	CtxServer Ctx = 1 << iota
	CtxLocation
	CtxIf
)

func (c Ctx) Has(other Ctx) bool { return c&other != 0 }

func (c Ctx) String() string {
	switch {
	case c == CtxServer:
		return "server"
	case c == CtxLocation:
		return "location"
	case c == CtxIf:
		return "if"
	default:
		return "server/location"
	}
}

// Level classifies who may configure a directive.
type Level uint8

const (
	// LevelUser directives are customer-configurable through the validated
	// config system.
	LevelUser Level = iota
	// LevelRestricted directives exist in the registry ONLY so validation
	// can produce a helpful error ("use the Locations section instead");
	// they are never rendered from user input.
	LevelRestricted
)

// Directive describes one registry entry: the contexts it is valid in and
// its security classification. Argument shapes are validated separately
// per directive in validate.go (see argValidators).
type Directive struct {
	Name     string
	Contexts Ctx
	Level    Level
}

// Registry entries reflect the nginx directive index (nginx.com/en/docs).
// Context encodings below are the customer-reachable subset: a directive
// documented as "http, server, location, if" is encoded with all of
// CtxServer|CtxLocation|CtxIf. The per-version authority is nginx -t.
var registry = map[string]Directive{
	// --- flow control / rewriting ---
	"rewrite":  {Name: "rewrite", Contexts: CtxServer | CtxLocation | CtxIf, Level: LevelUser},
	"return":   {Name: "return", Contexts: CtxServer | CtxLocation | CtxIf, Level: LevelUser},
	"set":      {Name: "set", Contexts: CtxServer | CtxLocation | CtxIf, Level: LevelUser},
	"break":    {Name: "break", Contexts: CtxServer | CtxLocation | CtxIf, Level: LevelUser},
	"if":       {Name: "if", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"try_files": {Name: "try_files", Contexts: CtxServer | CtxLocation, Level: LevelUser},

	// --- headers / caching / mime ---
	"add_header": {Name: "add_header", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"expires":    {Name: "expires", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"index":      {Name: "index", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"autoindex":  {Name: "autoindex", Contexts: CtxServer | CtxLocation, Level: LevelUser},

	// --- error pages / request body ---
	"error_page":            {Name: "error_page", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"client_max_body_size":  {Name: "client_max_body_size", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"client_body_buffer_size": {Name: "client_body_buffer_size", Contexts: CtxServer | CtxLocation, Level: LevelUser},

	// --- access control ---
	"allow":                 {Name: "allow", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"deny":                  {Name: "deny", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"satisfy":               {Name: "satisfy", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"auth_basic":            {Name: "auth_basic", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"auth_basic_user_file":  {Name: "auth_basic_user_file", Contexts: CtxServer | CtxLocation, Level: LevelUser},

	// --- reverse proxy tuning (proxy_pass itself is allowed ONLY inside
	// custom location bodies — see argValidators — and only to loopback
	// targets; the FPM/unix handlers stay platform-managed) ---
	"proxy_pass":            {Name: "proxy_pass", Contexts: CtxLocation | CtxIf, Level: LevelUser},
	"proxy_set_header":      {Name: "proxy_set_header", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"proxy_http_version":    {Name: "proxy_http_version", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"proxy_read_timeout":    {Name: "proxy_read_timeout", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"proxy_send_timeout":    {Name: "proxy_send_timeout", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"proxy_connect_timeout": {Name: "proxy_connect_timeout", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"proxy_buffering":       {Name: "proxy_buffering", Contexts: CtxServer | CtxLocation, Level: LevelUser},
	"proxy_redirect":        {Name: "proxy_redirect", Contexts: CtxServer | CtxLocation, Level: LevelUser},

	// --- restricted: known by name so errors can route users to the right
	// UI section (or explain the platform-managed status); never rendered ---
	"include":         {Name: "include", Level: LevelRestricted},
	"root":            {Name: "root", Level: LevelRestricted},
	"alias":           {Name: "alias", Level: LevelRestricted},
	"listen":          {Name: "listen", Level: LevelRestricted},
	"server_name":     {Name: "server_name", Level: LevelRestricted},
	"location":        {Name: "location", Level: LevelRestricted},
	"fastcgi_pass":    {Name: "fastcgi_pass", Level: LevelRestricted},
	"uwsgi_pass":      {Name: "uwsgi_pass", Level: LevelRestricted},
	"scgi_pass":       {Name: "scgi_pass", Level: LevelRestricted},
	"grpc_pass":       {Name: "grpc_pass", Level: LevelRestricted},
	"auth_request":    {Name: "auth_request", Level: LevelRestricted},
	"access_log":      {Name: "access_log", Level: LevelRestricted},
	"error_log":       {Name: "error_log", Level: LevelRestricted},
	"upstream":        {Name: "upstream", Level: LevelRestricted},
	"map":             {Name: "map", Level: LevelRestricted},
	"perl":            {Name: "perl", Level: LevelRestricted},
	"perl_set":        {Name: "perl_set", Level: LevelRestricted},
	"js_content":      {Name: "js_content", Level: LevelRestricted},
	"js_set":          {Name: "js_set", Level: LevelRestricted},
	"daemon":          {Name: "daemon", Level: LevelRestricted},
}

// restrictedHint maps restricted directives to the actionable guidance
// shown to users instead of a bare "unsupported directive".
var restrictedHint = map[string]string{
	"include":      "include is platform-managed; snippets are validated and rendered by EpicPanel directly",
	"root":         "the document root is managed by EpicPanel (site settings control it)",
	"alias":        "alias can expose files outside your site and is not allowed",
	"listen":       "listening sockets are platform-managed",
	"server_name":  "domains are managed in the Domains section of the site",
	"fastcgi_pass": "the PHP handler is platform-managed; PHP settings live in the PHP section",
	"location":     "Detected a location block. Use the Locations tab — enter the path there and EpicPanel generates the location block automatically",
	"access_log":   "logging (and bandwidth accounting) is platform-managed",
	"error_log":    "logging is platform-managed; site logs are available in the Logs section",
	"auth_request": "auth_request can chain request handling and is not allowed",
	"upstream":     "upstream blocks are platform-managed; proxy directly to a loopback address",
	"map":          "map blocks live in the http context and are platform-managed",
	"perl":         "embedded interpreters are not available",
	"perl_set":      "embedded interpreters are not available",
	"js_content":   "njs scripting is not available",
	"js_set":       "njs scripting is not available",
	"daemon":       "nginx process control is platform-managed",
}

// Lookup returns the registry entry for a directive name.
func Lookup(name string) (Directive, bool) {
	d, ok := registry[name]
	return d, ok
}

// FriendlyError renders a validation failure with the section-25 style
// guidance: what was rejected and what to do instead.
func FriendlyError(line string, reason string) error {
	return fmt.Errorf("%w: %s (%s)", ErrValidation, line, reason)
}

// ErrValidation marks every customer-facing config validation failure so
// the API layer can map it to a 422 consistently.
var ErrValidation = errVal("invalid nginx configuration")

type errVal string

func (e errVal) Error() string { return string(e) }

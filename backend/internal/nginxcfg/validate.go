package nginxcfg

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// SiteRules is the validation context: what this site's serving looks
// like and which loopback ports are its own.
type SiteRules struct {
	PHP  bool // site serves PHP through the managed FPM handler
	Proxy bool // location / is the managed reverse-proxy block
	// OwnPorts are loopback ports belonging to THIS site (app process or
	// backend web server) — always-valid proxy_pass targets.
	OwnPorts []int
	// PortOwner resolves a loopback port to the owning website id (""
	// when unowned). Control-plane only; nil at the agent.
	PortOwner func(port int) string
	// MaxBodyBytes caps client_max_body_size (default 4 GiB).
	MaxBodyBytes int64
}

func (r SiteRules) maxBody() int64 {
	if r.MaxBodyBytes > 0 {
		return r.MaxBodyBytes
	}
	return 4 << 30
}

// sizeRe matches nginx size values ("10m", "1g", "64k", "0").
var sizeRe = regexp.MustCompile(`^\d+[kKmMgG]?$`)

// timeRe matches simple nginx time values ("30s", "5m", "1h", "7d").
var timeRe = regexp.MustCompile(`^\d+[hmsd]?$`)

// expiresRe matches expires values ("30d", "@63072000", "epoch", "max", "off").
var expiresRe = regexp.MustCompile(`^(@\d+|epoch|max|off|\d+[hmsdwy])$`)

// headerNameRe matches a syntactically valid HTTP header name.
var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// uriPathRe matches an internal URI used by error_page.
var uriPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/~!$&'*+,;=:@%-]*$`)

// statusCodesRe for error_page leading arguments.
func isStatusCode(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 300 && n <= 599
}

// ParseSizeBytes converts an nginx size value to bytes.
func ParseSizeBytes(s string) (int64, error) {
	if !sizeRe.MatchString(s) {
		return 0, fmt.Errorf("%w: %q is not a valid size (use e.g. 64k, 32m, 1g)", ErrValidation, s)
	}
	num := s
	mult := int64(1)
	switch strings.ToLower(s[len(s)-1:]) {
	case "k":
		mult, num = 1024, s[:len(s)-1]
	case "m":
		mult, num = 1024*1024, s[:len(s)-1]
	case "g":
		mult, num = 1024*1024*1024, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not a valid size", ErrValidation, s)
	}
	return n * mult, nil
}

// proxySafeDirectives is the subset accepted inside the managed reverse
// proxy block (proxy mode): anything that would reroute or terminate the
// proxied request (try_files, rewrite targets, fastcgi…) is excluded.
var proxySafeDirectives = map[string]bool{
	"add_header": true, "allow": true, "deny": true, "expires": true,
	"client_max_body_size": true, "proxy_read_timeout": true,
	"proxy_send_timeout": true, "proxy_connect_timeout": true,
	"proxy_buffering": true, "proxy_set_header": true,
}

// ValidateSnippet validates a raw directive snippet for a context. It is
// the context-aware successor of the flat allowlist: each line must be a
// registry directive whose Contexts include ctx (LevelUser), with a
// per-directive argument shape check.
func ValidateSnippet(text string, ctx Ctx, rules SiteRules) error {
	stmts, err := ParseSnippet(text)
	if err != nil {
		return err
	}
	inner := ctx
	for _, s := range stmts {
		if s.BlockShut {
			inner = ctx // back to the enclosing context after an if block
			continue
		}
		if s.BlockOpen {
			if !ctx.Has(CtxServer | CtxLocation) {
				return FriendlyError(s.Text, "if blocks are not valid in this context")
			}
			// Validate the condition's variable reference.
			m := ifOpenRe.FindStringSubmatch(s.Text)
			if m == nil || !checkVarRefs("$"+m[1]) {
				return FriendlyError(s.Text, "if condition references an unsupported variable")
			}
			inner = CtxIf
			continue
		}
		if err := validateDirectiveLine(s.Text, inner, rules); err != nil {
			return err
		}
	}
	return nil
}

// validateDirectiveLine checks one plain directive line against the
// registry, context and argument shape.
func validateDirectiveLine(line string, ctx Ctx, rules SiteRules) error {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	name := strings.ToLower(fields[0])
	d, ok := Lookup(name)
	if !ok {
		return FriendlyError(line, "unknown directive")
	}
	if d.Level != LevelUser {
		hint := restrictedHint[name]
		if hint == "" {
			hint = name + " is platform-managed"
		}
		return FriendlyError(line, hint)
	}
	if !d.Contexts.Has(ctx) {
		return FriendlyError(line, name+" is not valid in "+ctx.String()+" context")
	}
	args := fields[1:]
	return validateArgs(name, args, line, rules)
}

// validateArgs applies the per-directive argument schema.
func validateArgs(name string, args []string, line string, rules SiteRules) error {
	bad := func(reason string) error { return FriendlyError(line, reason) }
	switch name {
	case "break":
		if len(args) != 0 {
			return bad("break takes no arguments")
		}
	case "rewrite":
		if len(args) < 2 || len(args) > 3 {
			return bad("rewrite takes a regex, a replacement and an optional flag")
		}
		if !checkVarRefs(args[1]) {
			return bad("replacement references an unsupported variable")
		}
		if len(args) == 3 {
			switch args[2] {
			case "last", "break", "redirect", "permanent":
			default:
				return bad("rewrite flag must be last, break, redirect or permanent")
			}
		}
	case "return":
		if len(args) < 1 || len(args) > 2 {
			return bad("return takes a status code (and optionally a URI, text or URL)")
		}
		if len(args) == 2 {
			// code + text/URL: any 100-999 code.
			n, err := strconv.Atoi(args[0])
			if err != nil || n < 100 || n > 999 {
				return bad("return code must be 100-999")
			}
		} else if !(len(args[0]) == 3 && isStatusCode(args[0])) && !strings.HasPrefix(args[0], "http") {
			return bad("return needs a code (optionally with a text/URL) or a URL")
		}
	case "set":
		if len(args) != 2 {
			return bad("set takes a variable and a value")
		}
		if !strings.HasPrefix(args[0], "$") || strings.HasPrefix(args[0], "$epicpanel") || len(args[0]) < 2 {
			return bad("set target variable is not allowed")
		}
		for _, c := range args[0][1:] {
			if !isIdentChar(byte(c)) {
				return bad("set target variable name is invalid")
			}
		}
		if err := validateVarToken(args[1]); err != nil {
			return err
		}
	case "try_files":
		if len(args) < 1 {
			return bad("try_files needs at least one target")
		}
		for _, a := range args {
			if !checkVarRefs(a) {
				return bad("try_files references an unsupported variable")
			}
		}
	case "add_header":
		if len(args) < 2 || len(args) > 3 {
			return bad("add_header takes a header name, a value and optionally 'always'")
		}
		if !headerNameRe.MatchString(args[0]) {
			return bad("invalid header name")
		}
		if len(args) == 3 && args[2] != "always" {
			return bad("third add_header argument must be 'always'")
		}
	case "expires":
		if len(args) != 1 || !expiresRe.MatchString(args[0]) {
			return bad("expires takes a time (30d), @epoch, epoch, max or off")
		}
	case "index", "autoindex":
		if name == "autoindex" && (len(args) != 1 || (args[0] != "on" && args[0] != "off")) {
			return bad("autoindex takes on or off")
		}
		if name == "index" && len(args) < 1 {
			return bad("index needs at least one file name")
		}
	case "error_page":
		if len(args) < 2 {
			return bad("error_page takes status codes and a URI")
		}
		for _, a := range args[:len(args)-1] {
			if !isStatusCode(a) {
				return bad(a + " is not a status code")
			}
		}
		uri := args[len(args)-1]
		if !uriPathRe.MatchString(uri) || strings.Contains(uri, "..") || !checkVarRefs(uri) {
			return bad("error_page URI must be an internal path like /404.html")
		}
	case "client_max_body_size", "client_body_buffer_size":
		if len(args) != 1 {
			return bad(name + " takes one size value")
		}
		if _, err := ParseSizeBytes(args[0]); err != nil {
			return err
		}
		if name == "client_max_body_size" {
			if n, _ := ParseSizeBytes(args[0]); n > rules.maxBody() {
				return bad("client_max_body_size exceeds the maximum allowed (4g)")
			}
		}
	case "allow", "deny":
		if len(args) != 1 {
			return bad(name + " takes one address, CIDR or 'all'")
		}
		if err := validateIPArg(args[0]); err != nil {
			return err
		}
	case "satisfy":
		if len(args) != 1 || (args[0] != "all" && args[0] != "any") {
			return bad("satisfy takes all or any")
		}
	case "auth_basic":
		if len(args) != 1 {
			return bad("auth_basic takes 'off' or a single-word realm")
		}
	case "auth_basic_user_file":
		if len(args) != 1 || !isSiteRelativePath(args[0]) {
			return bad("auth_basic_user_file must be a relative path (EpicPanel places it inside your site directory)")
		}
	case "proxy_pass":
		if len(args) != 1 {
			return bad("proxy_pass takes one URL")
		}
		if _, err := ValidateProxyTarget(args[0], rules); err != nil {
			return err
		}
	case "proxy_http_version":
		if len(args) != 1 || (args[0] != "1.0" && args[0] != "1.1") {
			return bad("proxy_http_version takes 1.0 or 1.1")
		}
	case "proxy_read_timeout", "proxy_send_timeout", "proxy_connect_timeout":
		if len(args) != 1 || !timeRe.MatchString(args[0]) {
			return bad(name + " takes a time value like 60s")
		}
	case "proxy_buffering":
		if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
			return bad("proxy_buffering takes on or off")
		}
	case "proxy_redirect":
		if len(args) != 1 || (args[0] != "off" && args[0] != "default") {
			return bad("proxy_redirect takes off or default here")
		}
	case "proxy_set_header":
		if len(args) != 2 {
			return bad("proxy_set_header takes a header name and a value")
		}
		if !headerNameRe.MatchString(args[0]) && args[0] != "Upgrade" && args[0] != "Connection" {
			return bad("invalid header name")
		}
		if !checkVarRefs(args[1]) {
			return bad("value references an unsupported variable")
		}
	default:
		// Registry-known USER directives without a specific schema keep
		// the generic token check (already applied via Fields split).
	}
	// Generic: no argument may reference platform or unknown variables.
	for _, a := range args {
		if err := validateVarToken(a); err != nil {
			return err
		}
	}
	return nil
}

// validateIPArg accepts an IP, a CIDR, or "all".
func validateIPArg(arg string) error {
	if arg == "all" {
		return nil
	}
	if strings.Contains(arg, ":") {
		if _, _, err := net.ParseCIDR(arg); err == nil {
			return nil
		}
		if ip := net.ParseIP(arg); ip != nil && ip.To16() != nil && strings.Count(arg, ":") >= 2 {
			return nil
		}
		return FriendlyError(arg, "not a valid IPv6 address or CIDR")
	}
	if _, _, err := net.ParseCIDR(arg); err == nil {
		return nil
	}
	if ip := net.ParseIP(arg); ip != nil && ip.To4() != nil {
		return nil
	}
	return FriendlyError(arg, "not a valid IP address or CIDR")
}

// isSiteRelativePath accepts a relative path without traversal.
func isSiteRelativePath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || strings.ContainsAny(p, ";{}\"'`$ \t") {
		return false
	}
	return strings.Contains(p, ".") || strings.Contains(p, "/") || len(p) > 0
}

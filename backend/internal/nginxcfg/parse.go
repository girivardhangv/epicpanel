package nginxcfg

import (
	"fmt"
	"regexp"
	"strings"
)

// Stmt is one parsed statement of a raw snippet: a directive line, an
// `if (...) {` block opener, or a `}` closer. The parser is intentionally
// minimal — nginx-style quoting is not supported; every argument must be
// a single whitespace-free token (the UIs and structured fields never
// need quoted arguments, and the constraint closes quoting-based
// injection avenues by construction).
type Stmt struct {
	Text      string // normalized (trimmed) statement text
	BlockOpen bool   // `if (...) {`
	BlockShut bool   // `}`
}

var ifOpenRe = regexp.MustCompile(`^if\s*\(\s*\$([a-z0-9_]+)\s*(=|!=|~|~\*|!~|!~\*)\s*([^\s)]+)\s*\)\s*\{$`)

// ParseSnippet splits a raw snippet into statements. It performs ONLY
// structural checks (if-opener shape, stray braces); semantic validation
// happens in ValidateSnippet. Comment lines and blanks are dropped.
func ParseSnippet(text string) ([]Stmt, error) {
	var out []Stmt
	depth := 0
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "}" {
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("%w: unexpected '}' on line %d", ErrValidation, i+1)
			}
			out = append(out, Stmt{Text: "}", BlockShut: true})
			continue
		}
		if strings.HasSuffix(line, "{") {
			// The only block opener accepted from user input.
			if !ifOpenRe.MatchString(line) {
				if strings.Fields(line)[0] == "location" {
					return nil, FriendlyError(line, restrictedHint["location"])
				}
				return nil, FriendlyError(line, "only `if ($var op value) {` blocks are supported")
			}
			depth++
			out = append(out, Stmt{Text: line, BlockOpen: true})
			continue
		}
		// Plain directive line: no structural characters anywhere.
		if strings.ContainsAny(line, "{};`") {
			return nil, FriendlyError(line, "structural characters ; { } and backticks are not allowed in directive lines")
		}
		out = append(out, Stmt{Text: line})
	}
	if depth != 0 {
		return nil, fmt.Errorf("%w: unterminated `if` block (missing '}')", ErrValidation)
	}
	return out, nil
}

// IndentSnippet renders parsed statements with one tab of indentation per
// nesting level (the vhost renderer embeds user lines inside a server
// block that is already at one tab). Plain directive lines are terminated
// with ";" — the parser guarantees the input carries none, and nginx
// requires them.
func IndentSnippet(stmts []Stmt, base string) string {
	var b strings.Builder
	depth := 0
	for _, s := range stmts {
		switch {
		case s.BlockShut:
			depth--
			b.WriteString(base + strings.Repeat("\t", depth) + "}\n")
		case s.BlockOpen:
			b.WriteString(base + strings.Repeat("\t", depth) + s.Text + "\n")
			depth++
		default:
			b.WriteString(base + strings.Repeat("\t", depth) + s.Text + ";\n")
		}
	}
	return b.String()
}

// Terminate appends ";" to a plain directive line (no-op for block
// openers/closers). Used when rendering individual validated lines.
func Terminate(line string) string {
	if line == "}" || strings.HasSuffix(line, "{") {
		return line
	}
	return line + ";"
}

// varRefRe constrains nginx variable references to well-known captures
// and safe built-ins. $epicpanel_* is deliberately absent: those carry the
// platform's site-ID stamp and bandwidth accounting (see renderSiteIDLines)
// and must stay unspoofable. Mirrors the audit-S6 hardening.
var varRefRe = regexp.MustCompile(`^(\d|uri|args|query_string|request_uri|host|scheme|request_method|remote_addr|https|server_port|request_filename|is_args|http_[a-z0-9_]+)`)

func isIdentChar(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// validateVarToken validates every $-reference in a token. A `$` followed
// by an identifier must be a known safe variable; a `$` NOT followed by
// an identifier is a regex end-anchor (rewrite patterns like ^/old$) or a
// literal and is accepted — nginx -t remains the authority for syntax.
func validateVarToken(token string) error {
	for i := 0; i < len(token); i++ {
		if token[i] != '$' {
			continue
		}
		j := i + 1
		for j < len(token) && isIdentChar(token[j]) {
			j++
		}
		if j == i+1 { // anchor / literal, not a variable
			i = j
			continue
		}
		name := token[i+1 : j]
		if strings.HasPrefix(name, "epicpanel") {
			return FriendlyError(token, "references a platform-reserved variable")
		}
		if !varRefRe.MatchString(name) {
			return FriendlyError(token, "references an unsupported variable $"+name)
		}
		i = j - 1
	}
	return nil
}

// checkVarRefs validates every $-reference in a token (bool form used by
// structured-field validators).
func checkVarRefs(token string) bool {
	return validateVarToken(token) == nil
}

// loopbackHosts are the only proxy_pass hosts accepted from user input:
// a customer may reach loopback services, never other hosts or unix
// sockets (which belong to other tenants' FPM pools).
var loopbackHosts = map[string]bool{"127.0.0.1": true, "localhost": true, "[::1]": true}

// ValidateProxyTarget checks a proxy_pass argument: http(s) URL with a
// loopback host and optional port, no variables, no userinfo, no unix
// sockets. Returns the port (0 when absent). Ownership of the port
// (which website it belongs to) is a control-plane concern, passed in via
// SiteRules.PortOwner; the agent enforces the loopback form only — a
// control-plane compromise is outside the agent's threat model for
// desired-state content, but snippet-form injection is not.
func ValidateProxyTarget(arg string, rules SiteRules) (int, error) {
	if strings.ContainsAny(arg, "$;{}\"'`") || strings.Contains(arg, "@") {
		return 0, FriendlyError(arg, "proxy target contains unsupported characters")
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(arg, "http://"), "https://")
	if rest == arg {
		return 0, FriendlyError(arg, "proxy target must be an http(s) URL")
	}
	hostPort := rest
	if i := strings.IndexAny(hostPort, "/"); i >= 0 {
		hostPort = hostPort[:i]
	}
	port := 0
	host := hostPort
	if i := strings.LastIndex(hostPort, ":"); i >= 0 && !strings.HasSuffix(hostPort, "]") {
		host = hostPort[:i]
		var err error
		if _, err = fmt.Sscanf(hostPort[i+1:], "%d", &port); err != nil || port <= 0 || port > 65535 {
			return 0, FriendlyError(arg, "proxy target port is invalid")
		}
	}
	if !loopbackHosts[strings.ToLower(host)] {
		return 0, FriendlyError(arg, "proxy targets must be loopback (127.0.0.1) addresses")
	}
	if rules.PortOwner != nil {
		if owner := rules.PortOwner(port); owner != "" && !rules.ownsPort(port) {
			return 0, fmt.Errorf("%w: port %d belongs to another website", ErrValidation, port)
		}
	}
	return port, nil
}

func (r SiteRules) ownsPort(port int) bool {
	for _, p := range r.OwnPorts {
		if p == port {
			return true
		}
	}
	return false
}

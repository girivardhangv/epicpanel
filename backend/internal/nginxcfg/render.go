package nginxcfg

import (
	"fmt"
	"strings"
)

// UserLayer is the validated, render-ready form of a SiteConfig for one
// site. The nginx provider embeds these pieces into its managed blocks:
// everything here has passed ValidateSiteConfig-equivalent checks, so the
// renderer only assembles strings.
type UserLayer struct {
	// ServerLines render at server level, inside the marked user section.
	ServerLines []string
	// RootTryFiles overrides the managed location / try_files ("" = keep
	// the managed default).
	RootTryFiles string
	// RootExtra lines render inside the managed location / (file mode).
	RootExtra []string
	// ProxyExtra lines render inside the managed proxy block (proxy
	// mode; already filtered to proxySafeDirectives).
	ProxyExtra []string
	// Locations are fully formed custom location blocks (header + body),
	// rendered after the managed locations.
	Locations []RenderedLocation
	// ErrorPageCodes are the status codes the user overrode — the nginx
	// provider drops its managed default for those codes.
	ErrorPageCodes map[int]bool
	// ClientMaxBodySize is the validated size ("" = none).
	ClientMaxBodySize string
}

// RenderedLocation is one validated custom location block.
type RenderedLocation struct {
	Header string   // e.g. `location ~* \.(css|js)$ {`
	Lines  []string // body lines (indented by the provider)
}

// BuildUserLayer validates a SiteConfig and renders the user layer.
// Defense in depth: the agent calls this on stored content; any section
// that fails validation is DROPPED (returned in `dropped`) and logged —
// it is never rendered. The control plane calls the same function via
// ValidateSiteConfig for strict save-time rejection.
func BuildUserLayer(cfg *SiteConfig, rules SiteRules) (*UserLayer, []string) {
	layer := &UserLayer{ErrorPageCodes: map[int]bool{}}
	var dropped []string
	drop := func(section string, err error) {
		dropped = append(dropped, section+": "+err.Error())
	}

	if cfg == nil {
		return layer, nil
	}

	// client_max_body_size
	if cfg.ClientMaxBodySize != "" {
		if _, err := ParseSizeBytes(cfg.ClientMaxBodySize); err == nil {
			layer.ClientMaxBodySize = cfg.ClientMaxBodySize
		} else {
			drop("client_max_body_size", err)
		}
	}

	// headers
	for _, h := range cfg.Headers {
		if err := validateHeader(h); err != nil {
			drop("headers", err)
			continue
		}
		line := "add_header " + h.Name + " " + quoteIfNeeded(h.Value)
		if h.Always {
			line += " always"
		}
		layer.ServerLines = append(layer.ServerLines, line)
	}

	// IP access
	if cfg.IPAccess != nil {
		if err := validateIPAccess(cfg.IPAccess); err != nil {
			drop("ip_access", err)
		} else {
			for _, r := range cfg.IPAccess.Rules {
				layer.ServerLines = append(layer.ServerLines, r.Action+" "+r.CIDR)
			}
			layer.ServerLines = append(layer.ServerLines, cfg.IPAccess.Default+" all")
		}
	}

	// error pages
	for _, ep := range cfg.ErrorPages {
		if !isStatusCode(fmt.Sprint(ep.Status)) || !uriPathRe.MatchString(ep.URI) || strings.Contains(ep.URI, "..") {
			drop("error_pages", fmt.Errorf("invalid rule %d %s", ep.Status, ep.URI))
			continue
		}
		layer.ServerLines = append(layer.ServerLines, fmt.Sprintf("error_page %d %s", ep.Status, ep.URI))
		layer.ErrorPageCodes[ep.Status] = true
	}

	// redirects → rewrite lines
	for _, rd := range cfg.Redirects {
		if err := validateRedirect(rd); err != nil {
			drop("redirects", err)
			continue
		}
		from := rd.From
		if !strings.HasPrefix(from, "^") {
			from = "^" + regexpQuote(from)
		}
		flag := "redirect"
		if rd.Code == 301 || rd.Code == 308 {
			flag = "permanent"
		}
		layer.ServerLines = append(layer.ServerLines, "rewrite "+from+" "+rd.To+" "+flag)
	}

	// raw server directives (if blocks keep their structure)
	if stmts, err := ParseSnippet(cfg.ServerDirectives); err != nil {
		drop("server_directives", err)
	} else if err := ValidateSnippet(cfg.ServerDirectives, CtxServer, rules); err != nil {
		drop("server_directives", err)
	} else {
		for _, s := range stmts {
			layer.ServerLines = append(layer.ServerLines, s.Text)
		}
	}

	// root location overrides: file mode extends the managed location /
	// (any location-context lines incl. if blocks); proxy mode only the
	// proxy-safe subset (no if blocks) joins the managed proxy block.
	if cfg.RootLocation != nil {
		if err := validateRootLocation(cfg.RootLocation, rules); err != nil {
			drop("root_location", err)
		} else {
			if cfg.RootLocation.TryFiles != "" {
				layer.RootTryFiles = cfg.RootLocation.TryFiles
			}
			if cfg.RootLocation.Extra != "" {
				if stmts, err := ParseSnippet(cfg.RootLocation.Extra); err == nil {
					for _, s := range stmts {
						if rules.Proxy {
							if s.BlockOpen || s.BlockShut {
								continue
							}
							name := strings.ToLower(strings.Fields(s.Text)[0])
							if !proxySafeDirectives[name] {
								continue
							}
							layer.ProxyExtra = append(layer.ProxyExtra, s.Text)
							continue
						}
						layer.RootExtra = append(layer.RootExtra, s.Text)
					}
				}
			}
		}
	}

	// custom locations + asset caching location
	seen := map[string]bool{}
	for i := range cfg.Locations {
		loc := &cfg.Locations[i]
		if err := validateCustomLocation(loc, rules); err != nil {
			drop("location "+loc.Match, err)
			continue
		}
		if err := validateAgainstManaged(&SiteConfig{Locations: cfg.Locations[i : i+1]}, rules); err != nil {
			drop("location "+loc.Match, err)
			continue
		}
		key := loc.Kind + " " + loc.Match
		if seen[key] {
			drop("location "+loc.Match, fmt.Errorf("duplicate location"))
			continue
		}
		seen[key] = true
		modifier := map[string]string{KindPrefix: "", KindExact: "=", KindRegex: "~", KindRegexNoCase: "~*"}[loc.Kind]
		header := "location " + modifier + " " + loc.Match + " {"
		if modifier == "" {
			header = "location " + loc.Match + " {"
		}
		var body []string
		if stmts, err := ParseSnippet(loc.Body); err == nil {
			for _, s := range stmts {
				body = append(body, s.Text)
			}
		}
		layer.Locations = append(layer.Locations, RenderedLocation{Header: header, Lines: body})
	}
	if cfg.AssetCaching != nil {
		if err := validateAssetCaching(cfg.AssetCaching); err != nil {
			drop("asset_caching", err)
		} else {
			pattern := assetCachingRegex(cfg.AssetCaching)
			key := KindRegexNoCase + " " + pattern
			if !seen[key] {
				seen[key] = true
				layer.Locations = append(layer.Locations, RenderedLocation{
					Header: "location ~* " + pattern + " {",
					Lines:  []string{"expires " + cfg.AssetCaching.Expires},
				})
			}
		}
	}
	return layer, dropped
}

// quoteIfNeeded wraps a value in double quotes when it contains spaces so
// the rendered line stays a single nginx argument.
func quoteIfNeeded(v string) string {
	if strings.ContainsAny(v, " \t") {
		return `"` + v + `"`
	}
	return v
}

// regexpQuote escapes regex metacharacters in a plain path so a literal
// redirect source can be rendered as an anchored regex.
func regexpQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '.', '*', '+', '?', '(', ')', '[', ']', '{', '}', '\\', '^', '$', '|':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

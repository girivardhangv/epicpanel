package nginxcfg

import (
	"fmt"
	"regexp"
	"strings"
)

// matchTokenRe constrains location matchers: a single whitespace-free
// token without structural characters.
var matchTokenRe = regexp.MustCompile(`^[^\s;{}"'` + "`" + `]+$`)

// validateMatcher checks a location matcher token. A `$` immediately
// followed by an identifier would be a variable reference — location
// match patterns are static and cannot contain variables.
func validateMatcher(match string) bool {
	if !matchTokenRe.MatchString(match) {
		return false
	}
	for i := 0; i < len(match); i++ {
		if match[i] != '$' {
			continue
		}
		if i+1 < len(match) && isIdentChar(match[i+1]) {
			return false
		}
	}
	return true
}

// managedMatcherRe lists the platform-managed location patterns a custom
// location may not duplicate or shadow (the managed block is rendered
// first, so a shadowing user block would either be dead configuration or
// break certificate renewal / default error pages).
type managedPattern struct {
	kind  string // exact match on kind
	match string // matcher text ("" = any)
	why   string
}

var managedExactLocations = map[string]string{
	"/epicpanel-busy.html":    "the panel's busy page is platform-managed",
	"/epicpanel-notfound.html": "the panel's not-found page is platform-managed",
}

// ValidateSiteConfig validates a complete structured config for a site:
// every section's shape, the raw snippets' contexts, and cross-section
// conflicts (duplicate/shadowing locations).
func ValidateSiteConfig(cfg *SiteConfig, rules SiteRules) error {
	if cfg.Schema != 2 && cfg.Schema != 0 {
		return fmt.Errorf("%w: unsupported config schema %d", ErrValidation, cfg.Schema)
	}
	if err := ValidateSnippet(cfg.ServerDirectives, CtxServer, rules); err != nil {
		return fmt.Errorf("server directives: %w", err)
	}
	if cfg.RootLocation != nil {
		if err := validateRootLocation(cfg.RootLocation, rules); err != nil {
			return err
		}
	}
	seen := map[string]string{} // location key -> owner (id or "caching")
	for i := range cfg.Locations {
		loc := &cfg.Locations[i]
		if err := validateCustomLocation(loc, rules); err != nil {
			return err
		}
		key := loc.Kind + " " + loc.Match
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("%w: duplicate location %s (also defined by %s)", ErrValidation, key, prev)
		}
		seen[key] = "location " + loc.ID
	}
	if cfg.AssetCaching != nil {
		if err := validateAssetCaching(cfg.AssetCaching); err != nil {
			return err
		}
		key := KindRegexNoCase + " " + assetCachingRegex(cfg.AssetCaching)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: asset caching conflicts with a custom location", ErrValidation)
		}
		seen[key] = "asset caching"
	}
	for _, h := range cfg.Headers {
		if err := validateHeader(h); err != nil {
			return err
		}
	}
	if cfg.IPAccess != nil {
		if err := validateIPAccess(cfg.IPAccess); err != nil {
			return err
		}
	}
	for _, ep := range cfg.ErrorPages {
		if !isStatusCode(fmt.Sprint(ep.Status)) {
			return fmt.Errorf("%w: error page status %d is not a 3xx-5xx code", ErrValidation, ep.Status)
		}
		if !uriPathRe.MatchString(ep.URI) || strings.Contains(ep.URI, "..") {
			return fmt.Errorf("%w: error page URI must be an internal path like /404.html", ErrValidation)
		}
	}
	if cfg.ClientMaxBodySize != "" {
		if n, err := ParseSizeBytes(cfg.ClientMaxBodySize); err != nil {
			return err
		} else if n > rules.maxBody() {
			return fmt.Errorf("%w: client_max_body_size exceeds the maximum allowed (4g)", ErrValidation)
		}
	}
	for _, rd := range cfg.Redirects {
		if err := validateRedirect(rd); err != nil {
			return err
		}
	}
	return validateAgainstManaged(cfg, rules)
}

// validateAgainstManaged rejects custom locations that duplicate or
// shadow EpicPanel's managed blocks.
func validateAgainstManaged(cfg *SiteConfig, rules SiteRules) error {
	for i := range cfg.Locations {
		loc := &cfg.Locations[i]
		switch loc.Kind {
		case KindPrefix:
			if loc.Match == "/" {
				return fmt.Errorf("%w: custom location / conflicts with EpicPanel's managed default location — configure the Root Location section instead", ErrValidation)
			}
			if strings.HasPrefix(loc.Match, "/.well-known/acme-challenge") {
				return fmt.Errorf("%w: %s conflicts with EpicPanel's certificate-challenge location (SSL renewal would break)", ErrValidation, loc.Match)
			}
		case KindExact:
			if why, managed := managedExactLocations[loc.Match]; managed {
				return fmt.Errorf("%w: %s — %s", ErrValidation, loc.Match, why)
			}
		case KindRegex, KindRegexNoCase:
			if rules.PHP && strings.HasSuffix(loc.Match, `\.php$`) {
				return fmt.Errorf("%w: regex location %s conflicts with EpicPanel's managed PHP handler", ErrValidation, loc.Match)
			}
			if strings.Contains(loc.Match, `/\.`) {
				return fmt.Errorf("%w: regex location %s conflicts with the managed dotfile-deny location", ErrValidation, loc.Match)
			}
		}
	}
	return nil
}

func validateRootLocation(root *RootLocationCfg, rules SiteRules) error {
	if root.TryFiles != "" {
		if rules.Proxy {
			return fmt.Errorf("%w: try_files cannot be set while the site proxies to an application process", ErrValidation)
		}
		if err := validateArgs("try_files", strings.Fields(root.TryFiles), "try_files "+root.TryFiles, SiteRules{}); err != nil {
			return err
		}
	}
	if root.Extra != "" {
		if err := ValidateSnippet(root.Extra, CtxLocation, rules); err != nil {
			return fmt.Errorf("root location extras: %w", err)
		}
	}
	return nil
}

func validateCustomLocation(loc *CustomLocation, rules SiteRules) error {
	if loc.ID == "" {
		return fmt.Errorf("%w: custom location is missing an id", ErrValidation)
	}
	switch loc.Kind {
	case KindPrefix, KindExact:
		if !strings.HasPrefix(loc.Match, "/") || !validateMatcher(loc.Match) {
			return fmt.Errorf("%w: location %s: prefix/exact matchers must be a URI path like /api/", ErrValidation, loc.ID)
		}
	case KindRegex, KindRegexNoCase:
		if !validateMatcher(loc.Match) {
			return fmt.Errorf("%w: location %s: regex matcher must be a single token like \\.php$ — put the pattern in the matcher field, EpicPanel adds the location wrapper", ErrValidation, loc.ID)
		}
	default:
		return fmt.Errorf("%w: location %s has unsupported kind %q", ErrValidation, loc.ID, loc.Kind)
	}
	bodyRules := rules
	if loc.Kind == KindPrefix && loc.Match == "/" {
		bodyRules = rules // unreachable: root prefix is rejected earlier
	}
	if err := ValidateSnippet(loc.Body, CtxLocation, bodyRules); err != nil {
		return fmt.Errorf("location %s: %w", loc.Match, err)
	}
	return nil
}

func validateHeader(h HeaderRule) error {
	if !headerNameRe.MatchString(h.Name) {
		return fmt.Errorf("%w: %q is not a valid header name", ErrValidation, h.Name)
	}
	if h.Value == "" || strings.ContainsAny(h.Value, "\"'`;{}\r\n\t") {
		return fmt.Errorf("%w: header %s has an invalid value", ErrValidation, h.Name)
	}
	return nil
}

func validateIPAccess(ip *IPAccess) error {
	if ip.Default != "allow" && ip.Default != "deny" {
		return fmt.Errorf("%w: IP access default must be allow or deny", ErrValidation)
	}
	for _, r := range ip.Rules {
		if r.Action != "allow" && r.Action != "deny" {
			return fmt.Errorf("%w: IP rule action must be allow or deny", ErrValidation)
		}
		if err := validateIPArg(r.CIDR); err != nil {
			return err
		}
	}
	return nil
}

func validateAssetCaching(ac *AssetCaching) error {
	if len(ac.Classes) == 0 {
		return fmt.Errorf("%w: asset caching needs at least one asset class", ErrValidation)
	}
	for _, c := range ac.Classes {
		if _, ok := AssetClasses[c]; !ok {
			return fmt.Errorf("%w: unknown asset class %q (use css, js, img, fonts, media)", ErrValidation, c)
		}
	}
	if !expiresRe.MatchString(ac.Expires) || ac.Expires == "off" || ac.Expires == "epoch" {
		return fmt.Errorf("%w: asset caching expires must be a time like 30d or 7d", ErrValidation)
	}
	return nil
}

func validateRedirect(rd PathRedirect) error {
	if rd.Code != 301 && rd.Code != 302 && rd.Code != 307 && rd.Code != 308 {
		return fmt.Errorf("%w: redirect code must be 301, 302, 307 or 308", ErrValidation)
	}
	if rd.From == "" || rd.To == "" {
		return fmt.Errorf("%w: redirect needs a from path and a to target", ErrValidation)
	}
	if strings.HasPrefix(rd.From, "^") {
		if !matchTokenRe.MatchString(rd.From) {
			return fmt.Errorf("%w: redirect from-pattern must be a single regex token", ErrValidation)
		}
	} else if !strings.HasPrefix(rd.From, "/") || strings.ContainsAny(rd.From, " \t;{}\"'`$") {
		return fmt.Errorf("%w: redirect from must be a path like /old-path", ErrValidation)
	}
	if !strings.HasPrefix(rd.To, "/") && !strings.HasPrefix(rd.To, "http://") && !strings.HasPrefix(rd.To, "https://") {
		return fmt.Errorf("%w: redirect to must be a path or absolute URL", ErrValidation)
	}
	if strings.ContainsAny(rd.To, " \t;{}\"'`") {
		return fmt.Errorf("%w: redirect to must be a single token", ErrValidation)
	}
	if !checkVarRefs(rd.To) {
		return fmt.Errorf("%w: redirect to references an unsupported variable", ErrValidation)
	}
	return nil
}

// assetCachingRegex builds the regex matcher for the generated caching
// location (used for conflict keys and rendering).
func assetCachingRegex(ac *AssetCaching) string {
	exts := map[string]bool{}
	for _, c := range ac.Classes {
		for _, e := range AssetClasses[c] {
			exts[e] = true
		}
	}
	var list []string
	for e := range exts {
		list = append(list, e)
	}
	sortStrings(list)
	return `\.(` + strings.Join(list, "|") + `)$`
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

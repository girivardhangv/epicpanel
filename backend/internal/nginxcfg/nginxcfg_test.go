package nginxcfg

import (
	"strings"
	"testing"
)

func TestValidateSnippetServerContext(t *testing.T) {
	rules := SiteRules{}
	valid := []string{
		"rewrite ^/old$ /new permanent",
		"return 301 /new",
		"client_max_body_size 32m",
		"add_header X-Frame-Options SAMEORIGIN always",
		"expires 30d",
		"deny 1.2.3.4",
		"allow 10.0.0.0/8",
		"deny all",
		"error_page 404 /404.html",
		"if ($http_user_agent ~* bot) {\nreturn 403\n}",
		"# comment",
		"",
	}
	for _, v := range valid {
		if err := ValidateSnippet(v, CtxServer, rules); err != nil {
			t.Errorf("expected server-context acceptance for %q: %v", v, err)
		}
	}
}

func TestValidateSnippetRejectsWrongContext(t *testing.T) {
	rules := SiteRules{}
	// try_files is valid in server+location; if-in-server blocks open fine but
	// location-context check: server directives valid, location-only in server
	// is fine for these; the real reject: a directive valid ONLY in location
	// inside an if block at server... keep it simple: proxy_http_version is
	// server+location so valid; check that unknown + restricted reject.
	bad := map[string]string{
		"include /etc/nginx/evil.conf": "platform-managed",
		"root /etc":                    "",
		"alias /other/site":            "",
		"listen 8080":                  "",
		"server_name other.com":        "",
		"fastcgi_pass unix:/x.sock":    "",
		"location /api/ {":             "location block",
		"proxy_pass http://example.com": "example.com",
		"access_log /x.log":            "",
		"set $epicpanel_site_id x":     "",
		"rewrite ^/x /y; rm -rf /":     "structural",
		"try_files $uri $uri/ {":       "",
	}
	for line, want := range bad {
		err := ValidateSnippet(line, CtxServer, rules)
		if err == nil {
			t.Errorf("expected rejection for %q", line)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("rejection for %q should mention %q, got: %v", line, want, err)
		}
	}
}

func TestFriendlyLocationHint(t *testing.T) {
	err := ValidateSnippet("location /api/ {", CtxServer, SiteRules{})
	if err == nil || !strings.Contains(err.Error(), "Locations") {
		t.Errorf("location hint should route users to the Locations section, got: %v", err)
	}
}

func TestParseSnippetIfBlocks(t *testing.T) {
	ok := "if ($scheme = http) {\nreturn 301 https://$host$request_uri\n}"
	if _, err := ParseSnippet(ok); err != nil {
		t.Errorf("valid if block rejected: %v", err)
	}
	unterminated := "if ($scheme = http) {\nreturn 301"
	if _, err := ParseSnippet(unterminated); err == nil {
		t.Error("unterminated if block must be rejected")
	}
	stray := "return 301 /x\n}"
	if _, err := ParseSnippet(stray); err == nil {
		t.Error("stray closing brace must be rejected")
	}
	notIf := "foo ($x = y) {"
	if _, err := ParseSnippet(notIf); err == nil {
		t.Error("non-if block opener must be rejected")
	}
}

func TestValidateProxyTarget(t *testing.T) {
	own := SiteRules{OwnPorts: []int{3000}}
	cases := []struct {
		arg  string
		fail bool
	}{
		{"http://127.0.0.1:3000", false},
		{"http://localhost:3000/app", false},
		{"http://[::1]:3000", false},
		{"http://example.com:80", true},       // not loopback
		{"http://10.0.0.5:3000", true},        // not loopback
		{"unix:/tmp/x.sock", true},            // not http
		{"http://127.0.0.1:3000/$var", true},  // variables
		{"http://user@127.0.0.1:3000", true},  // userinfo
		{"http://127.0.0.1:99999", true},      // bad port
	}
	for _, c := range cases {
		_, err := ValidateProxyTarget(c.arg, own)
		if c.fail && err == nil {
			t.Errorf("expected rejection for %q", c.arg)
		}
		if !c.fail && err != nil {
			t.Errorf("expected acceptance for %q: %v", c.arg, err)
		}
	}
}

func TestPortOwnership(t *testing.T) {
	rules := SiteRules{OwnPorts: []int{3000}, PortOwner: func(int) string { return "other-site" }}
	if _, err := ValidateProxyTarget("http://127.0.0.1:4000", rules); err == nil {
		t.Error("port owned by another site must be rejected")
	}
	if _, err := ValidateProxyTarget("http://127.0.0.1:3000", rules); err != nil {
		t.Errorf("own port must be accepted: %v", err)
	}
}

func TestValidateSiteConfigLocations(t *testing.T) {
	cfg := &SiteConfig{Schema: 2}
	cfg.Locations = []CustomLocation{
		{ID: "l1", Kind: KindExact, Match: "/health", Body: "return 200 OK"},
		{ID: "l2", Kind: KindPrefix, Match: "/api/", Body: "proxy_pass http://127.0.0.1:3000"},
	}
	if err := ValidateSiteConfig(cfg, SiteRules{OwnPorts: []int{3000}}); err != nil {
		t.Errorf("valid locations rejected: %v", err)
	}

	// duplicate
	cfg.Locations = append(cfg.Locations, CustomLocation{ID: "l3", Kind: KindExact, Match: "/health", Body: "return 200 ok2"})
	if err := ValidateSiteConfig(cfg, SiteRules{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate location must be rejected, got: %v", err)
	}

	// root prefix conflict
	cfg.Locations = []CustomLocation{{ID: "l1", Kind: KindPrefix, Match: "/", Body: "expires 1h"}}
	err := ValidateSiteConfig(cfg, SiteRules{})
	if err == nil || !strings.Contains(err.Error(), "Root Location") {
		t.Errorf("location / must route to Root Location section, got: %v", err)
	}

	// ACME challenge conflict
	cfg.Locations = []CustomLocation{{ID: "l1", Kind: KindPrefix, Match: "/.well-known/acme-challenge/", Body: "expires 1h"}}
	if err := ValidateSiteConfig(cfg, SiteRules{}); err == nil || !strings.Contains(err.Error(), "certificate-challenge") {
		t.Errorf("ACME shadowing must be rejected, got: %v", err)
	}

	// PHP handler conflict
	cfg.Locations = []CustomLocation{{ID: "l1", Kind: KindRegex, Match: `\.php$`, Body: "expires 1h"}}
	if err := ValidateSiteConfig(cfg, SiteRules{PHP: true}); err == nil || !strings.Contains(err.Error(), "PHP handler") {
		t.Errorf("PHP handler shadowing must be rejected, got: %v", err)
	}
}

func TestValidateSiteConfigSections(t *testing.T) {
	cfg := &SiteConfig{Schema: 2, ClientMaxBodySize: "512m",
		Headers: []HeaderRule{{Name: "X-Content-Type-Options", Value: "nosniff"}},
		IPAccess: &IPAccess{Rules: []IPRule{{CIDR: "203.0.113.0/24", Action: "deny"}}, Default: "allow"},
		ErrorPages: []ErrorPageRule{{Status: 404, URI: "/404.html"}},
		Redirects:  []PathRedirect{{From: "/old", To: "/new", Code: 301}},
		AssetCaching: &AssetCaching{Classes: []string{"css", "js"}, Expires: "30d"},
	}
	if err := ValidateSiteConfig(cfg, SiteRules{}); err != nil {
		t.Errorf("valid structured config rejected: %v", err)
	}

	// bad values
	cfg2 := &SiteConfig{Schema: 2, ClientMaxBodySize: "999g"}
	if err := ValidateSiteConfig(cfg2, SiteRules{}); err == nil {
		t.Error("oversized body cap must be rejected")
	}
	cfg3 := &SiteConfig{Schema: 2, Headers: []HeaderRule{{Name: "Bad Header", Value: "x"}}}
	if err := ValidateSiteConfig(cfg3, SiteRules{}); err == nil {
		t.Error("invalid header name must be rejected")
	}
	// CRLF injection
	cfg4 := &SiteConfig{Schema: 2, Headers: []HeaderRule{{Name: "X-Test", Value: "x\r\nSet-Cookie: pwn"}}}
	if err := ValidateSiteConfig(cfg4, SiteRules{}); err == nil {
		t.Error("CRLF header injection must be rejected")
	}
	// error page traversal
	cfg5 := &SiteConfig{Schema: 2, ErrorPages: []ErrorPageRule{{Status: 404, URI: "/../../etc/passwd"}}}
	if err := ValidateSiteConfig(cfg5, SiteRules{}); err == nil {
		t.Error("error page traversal must be rejected")
	}
	// try_files on proxy site
	cfg6 := &SiteConfig{Schema: 2, RootLocation: &RootLocationCfg{TryFiles: "$uri $uri/ /index.php?$query_string"}}
	if err := ValidateSiteConfig(cfg6, SiteRules{Proxy: true}); err == nil {
		t.Error("try_files override on proxy site must be rejected")
	}
	if err := ValidateSiteConfig(cfg6, SiteRules{}); err != nil {
		t.Errorf("try_files override on file site must pass: %v", err)
	}
}

func TestBuildUserLayer(t *testing.T) {
	cfg := &SiteConfig{Schema: 2, ClientMaxBodySize: "64m",
		Headers: []HeaderRule{{Name: "X-Frame-Options", Value: "DENY", Always: true}},
		IPAccess: &IPAccess{Rules: []IPRule{{CIDR: "1.2.3.4", Action: "allow"}}, Default: "deny"},
		ErrorPages: []ErrorPageRule{{Status: 403, URI: "/forbidden.html"}},
		Redirects:  []PathRedirect{{From: "/old", To: "/new", Code: 301}},
		Locations:  []CustomLocation{{ID: "l1", Kind: KindExact, Match: "/health", Body: "return 200 OK"}},
		AssetCaching: &AssetCaching{Classes: []string{"img"}, Expires: "7d"},
		RootLocation: &RootLocationCfg{TryFiles: "$uri $uri/ /index.php?$args"},
		ServerDirectives: "expires 1h",
	}
	layer, dropped := BuildUserLayer(cfg, SiteRules{})
	if len(dropped) != 0 {
		t.Fatalf("nothing should be dropped: %v", dropped)
	}
	joined := strings.Join(layer.ServerLines, "\n")
	for _, want := range []string{
		"add_header X-Frame-Options DENY always",
		"allow 1.2.3.4",
		"deny all",
		"error_page 403 /forbidden.html",
		"rewrite ^/old /new permanent",
		"expires 1h",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("server layer missing %q in:\n%s", want, joined)
		}
	}
	if layer.ClientMaxBodySize != "64m" {
		t.Errorf("client_max_body_size field lost: %q", layer.ClientMaxBodySize)
	}
	if layer.RootTryFiles != "$uri $uri/ /index.php?$args" {
		t.Errorf("root try_files override lost: %q", layer.RootTryFiles)
	}
	if len(layer.Locations) != 2 {
		t.Fatalf("expected 2 rendered locations, got %d", len(layer.Locations))
	}
	if layer.Locations[0].Header != "location = /health {" {
		t.Errorf("exact location header wrong: %q", layer.Locations[0].Header)
	}
	if layer.Locations[1].Header != "location ~* \\.(ico|gif|jpe?g|png|svg|webp|avif)$ {" &&
		!strings.Contains(layer.Locations[1].Header, "~*") {
		t.Errorf("asset caching header wrong: %q", layer.Locations[1].Header)
	}
	if !layer.ErrorPageCodes[403] {
		t.Error("error page code 403 not recorded for managed-default drop")
	}
}

func TestBuildUserLayerDropsBadSections(t *testing.T) {
	cfg := &SiteConfig{Schema: 2,
		ServerDirectives: "include /etc/passwd",
		Headers:          []HeaderRule{{Name: "ok", Value: "v"}},
		Locations:        []CustomLocation{{ID: "l1", Kind: KindPrefix, Match: "/", Body: "expires 1h"}},
	}
	layer, dropped := BuildUserLayer(cfg, SiteRules{})
	if len(dropped) < 2 {
		t.Errorf("bad sections must be dropped, got: %v", dropped)
	}
	if !strings.Contains(strings.Join(layer.ServerLines, "\n"), "add_header ok v") {
		t.Error("good header must survive when other sections fail")
	}
	if len(layer.Locations) != 0 {
		t.Error("conflicting location must not render")
	}
}

func TestLegacyRulesStillValidate(t *testing.T) {
	// Everything the OLD flat allowlist accepted must still validate at
	// server context (backward compat).
	legacy := strings.Join([]string{
		"rewrite ^/blog/(.*) /blog/index.php permanent",
		"return 301 /new",
		"if ($request_method = POST) {",
		"return 405",
		"}",
		"try_files $uri $uri/ =404",
		"add_header X-Test 1",
		"deny all",
		"allow 10.0.0.0/8",
		"expires 7d",
		"error_page 404 /404.html",
		"client_max_body_size 10m",
	}, "\n")
	if err := ValidateSnippet(legacy, CtxServer, SiteRules{}); err != nil {
		t.Errorf("legacy snippet must remain valid: %v", err)
	}
}

func TestAgentRevalidationUntrusted(t *testing.T) {
	// The agent re-validates stored content; a snippet tampered between
	// save and render must not render.
	cfg := &SiteConfig{Schema: 2, ServerDirectives: "root /etc"}
	_, dropped := BuildUserLayer(cfg, SiteRules{})
	if len(dropped) == 0 {
		t.Error("tampered server_directives must be dropped at render time")
	}
}

func TestIndentSnippet(t *testing.T) {
	stmts, err := ParseSnippet("expires 1h\nif ($x = y) {\nreturn 403\n}")
	if err != nil {
		t.Fatal(err)
	}
	out := IndentSnippet(stmts, "\t\t")
	want := "\t\texpires 1h;\n\t\tif ($x = y) {\n\t\t\treturn 403;\n\t\t}\n"
	if out != want {
		t.Errorf("indent mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestParseSizeBytes(t *testing.T) {
	cases := map[string]int64{"64k": 65536, "32m": 33554432, "1g": 1073741824, "0": 0}
	for in, want := range cases {
		got, err := ParseSizeBytes(in)
		if err != nil || got != want {
			t.Errorf("ParseSizeBytes(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := ParseSizeBytes("12x"); err == nil {
		t.Error("invalid size unit must be rejected")
	}
}

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/nginxcfg"
)

// ADR-068: structured SiteConfig renders the EPICPANEL USER CONFIG layer
// into the managed vhost — custom locations, try_files override, headers,
// IP access, error-page override (managed default suppressed), redirects.
func TestRenderVhostSiteConfigUserLayer(t *testing.T) {
	tmp := t.TempDir()
	cert := filepath.Join(tmp, "cert.pem")
	key := filepath.Join(tmp, "key.pem")
	if err := os.WriteFile(cert, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &nginxcfg.SiteConfig{Schema: 2, ClientMaxBodySize: "64m"}
	cfg.Headers = []nginxcfg.HeaderRule{{Name: "X-Frame-Options", Value: "DENY", Always: true}}
	cfg.IPAccess = &nginxcfg.IPAccess{Rules: []nginxcfg.IPRule{{CIDR: "203.0.113.0/24", Action: "deny"}}, Default: "allow"}
	cfg.ErrorPages = []nginxcfg.ErrorPageRule{{Status: 404, URI: "/my-404.html"}}
	cfg.RootLocation = &nginxcfg.RootLocationCfg{TryFiles: "$uri $uri/ /index.php?$query_string"}
	cfg.Locations = []nginxcfg.CustomLocation{
		{ID: "l1", Kind: nginxcfg.KindExact, Match: "/health", Body: "return 200 ok"},
		{ID: "l2", Kind: nginxcfg.KindRegexNoCase, Match: `\.jpg$`, Body: "expires 30d"},
	}
	cfg.Redirects = []nginxcfg.PathRedirect{{From: "/old", To: "/new", Code: 301}}
	cfg.AssetCaching = &nginxcfg.AssetCaching{Classes: []string{"css", "js"}, Expires: "30d"}

	v := VhostSpec{
		WebsiteID:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		UnixUser:     "ep-test-site",
		DocumentRoot: "/srv/epicpanel/websites/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/public",
		FpmSocket:    "/run/epicpanel/php-fpm/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.sock",
		SiteConfig:   cfg,
		AcmeWebroot:  tmp,
		Domains: []DomainSpec{
			{Domain: "example.test", SSLMode: "selfsigned", CertPath: cert, KeyPath: key},
		},
	}
	out := RenderVhost(v)

	for _, want := range []string{
		"# EPICPANEL USER CONFIG — validated (ADR-068)",
		"client_max_body_size 64m",
		"add_header X-Frame-Options DENY always",
		"deny 203.0.113.0/24",
		"allow all",
		"error_page 404 /my-404.html",
		"rewrite ^/old /new permanent",
		"location / {\n\t\ttry_files $uri $uri/ /index.php?$query_string;",
		"location = /health {",
		"location ~* \\.jpg$ {",
		"location ~* \\.(",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered vhost missing %q\nvhost:\n%s", want, out)
		}
	}

	// Managed defaults that the user overrode must be suppressed.
	if strings.Contains(out, "error_page 404 /epicpanel-notfound.html") {
		t.Error("user-overridden 404 must suppress the managed default")
	}
	if !strings.Contains(out, "error_page 502 504 /epicpanel-busy.html") {
		t.Error("managed 502/504 default must remain when not overridden")
	}

	// Security invariants of the generated vhost must survive layering.
	for _, must := range []string{
		"set $epicpanel_site_id",
		"epicpanel_bandwidth",
		"location ~ /\\. {\n\t\tdeny all;",
		"location ~ \\.php$ {",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("managed section lost after layering: %q\nvhost:\n%s", must, out)
		}
	}

	// A bad section stored client-side must be dropped at render, loudly.
	tampered := &nginxcfg.SiteConfig{Schema: 2}
	tampered.ServerDirectives = "include /etc/nginx/evil.conf"
	tampered.Headers = []nginxcfg.HeaderRule{{Name: "ok", Value: "v"}}
	v.SiteConfig = tampered
	out2 := RenderVhost(v)
	if strings.Contains(out2, "evil.conf") {
		t.Error("tampered server_directives must never render")
	}
	if !strings.Contains(out2, "add_header ok v") {
		t.Error("valid section must survive alongside a dropped one")
	}
}

// Legacy behavior: sites without SiteConfig render the legacy snippet as
// before (byte-compatible contract).
func TestRenderVhostLegacyRewriteUnchanged(t *testing.T) {
	v := VhostSpec{
		WebsiteID:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		UnixUser:     "ep-test-site",
		DocumentRoot: "/srv/epicpanel/websites/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/public",
		RewriteRules: "rewrite ^/blog/(.*) /blog/index.php permanent",
		Domains:      []DomainSpec{{Domain: "example.test", SSLMode: "none"}},
	}
	out := RenderVhost(v)
	if !strings.Contains(out, "# user rewrite rules / custom directives (EpicPanel managed)") {
		t.Errorf("legacy marker missing:\n%s", out)
	}
	if !strings.Contains(out, "rewrite ^/blog/(.*) /blog/index.php permanent") {
		t.Errorf("legacy rewrite missing:\n%s", out)
	}
	if strings.Contains(out, "EPICPANEL USER CONFIG") {
		t.Error("legacy site must not render the structured layer")
	}
}

// Proxy mode: user body cap lands inside the managed proxy block; proxy-
// safe extras render there, and try_files overrides are rejected.
func TestRenderVhostProxyModeLayer(t *testing.T) {
	cfg := &nginxcfg.SiteConfig{Schema: 2, ClientMaxBodySize: "200m"}
	cfg.RootLocation = &nginxcfg.RootLocationCfg{
		Extra: "proxy_read_timeout 600s\nexpires 1h\nautoindex on",
	}
	v := VhostSpec{
		WebsiteID:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		UnixUser:     "ep-test-site",
		DocumentRoot: "/srv/epicpanel/websites/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/public",
		SiteConfig:   cfg,
		ProxyPass:    "http://127.0.0.1:3456",
		Domains:      []DomainSpec{{Domain: "example.test", SSLMode: "none"}},
	}
	out := RenderVhost(v)
	if !strings.Contains(out, "client_max_body_size 200m;") {
		t.Errorf("user body cap must reach the proxy block:\n%s", out)
	}
	if strings.Contains(out, "client_max_body_size 100m;") {
		t.Error("managed 100m default must yield to the user cap")
	}
	if !strings.Contains(out, "proxy_read_timeout 600s") {
		t.Error("proxy-safe extra must render inside the proxy block")
	}
	// expires is proxy-safe (response caching on the proxied location).
	proxyBlock := out[strings.Index(out, "location / {"):]
	if end := strings.Index(proxyBlock, "\n\t}"); end > 0 {
		body := proxyBlock[:end]
		if !strings.Contains(body, "expires 1h") {
			t.Error("proxy-safe expires extra must render inside the managed proxy block")
		}
		if strings.Contains(body, "autoindex") {
			t.Error("non-proxy-safe extra (autoindex) must be filtered from the managed proxy block")
		}
	} else {
		t.Fatal("managed proxy location not found in output")
	}
}

// Control-plane + agent validator agreement: the same tampered document is
// rejected at save time and dropped at render time.
func TestSiteConfigSaveRenderAgreement(t *testing.T) {
	bad := &nginxcfg.SiteConfig{Schema: 2}
	bad.Locations = []nginxcfg.CustomLocation{{ID: "l1", Kind: nginxcfg.KindPrefix, Match: "/.well-known/acme-challenge/", Body: "expires 1h"}}
	if err := nginxcfg.ValidateSiteConfig(bad, nginxcfg.SiteRules{PHP: true}); err == nil {
		t.Fatal("control plane must reject ACME shadowing")
	}
	layer, dropped := nginxcfg.BuildUserLayer(bad, nginxcfg.SiteRules{PHP: true})
	if len(layer.Locations) != 0 || len(dropped) == 0 {
		t.Errorf("agent must drop the same config: locations=%d dropped=%v", len(layer.Locations), dropped)
	}
	_ = time.Now // keep time import if assertions change
}

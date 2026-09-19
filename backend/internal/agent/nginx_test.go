package agent

import (
	"os"
	"strings"
	"testing"
)

func TestRenderVhostStatic(t *testing.T) {
	out := RenderVhost(VhostSpec{
		WebsiteID:    "11111111-1111-1111-1111-111111111111",
		DocumentRoot: "/srv/epicpanel/websites/11111111-1111-1111-1111-111111111111/public",
		Domains: []DomainSpec{
			{Domain: "shop.example.test", SSLMode: "none"},
			{Domain: "www.shop.example.test", SSLMode: "none"},
		},
	})
	for _, want := range []string{
		"server_name shop.example.test www.shop.example.test;",
		"root /srv/epicpanel/websites/11111111-1111-1111-1111-111111111111/public;",
		"try_files $uri $uri/ =404;",
		"location ~ /\\.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("static vhost missing %q", want)
		}
	}
	if strings.Contains(out, "fastcgi_pass") {
		t.Error("static vhost must not contain fastcgi_pass")
	}
	if strings.Contains(out, "listen 443") {
		t.Error("static vhost without ssl must not listen on 443")
	}
}

func TestRenderVhostPHPWithSSL(t *testing.T) {
	id := "22222222-2222-2222-2222-222222222222"
	certDir := t.TempDir()
	certPath := certDir + "/fullchain.pem"
	keyPath := certDir + "/privkey.pem"
	if err := os.WriteFile(certPath, []byte("CERT"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := RenderVhost(VhostSpec{
		WebsiteID:    id,
		DocumentRoot: "/srv/epicpanel/websites/" + id + "/public",
		FpmSocket:    "/run/epicpanel/php-fpm/" + id + ".sock",
		Domains: []DomainSpec{
			{Domain: "app.example.test", SSLMode: "selfsigned", CertPath: certPath, KeyPath: keyPath},
			{Domain: "plain.example.test", SSLMode: "none"},
		},
	})
	for _, want := range []string{
		"fastcgi_pass unix:/run/epicpanel/php-fpm/" + id + ".sock;",
		"listen 443 ssl;",
		"ssl_certificate " + certPath + ";",
		"return 301 https://$host$request_uri;",
		"server_name app.example.test;",   // secured block
		"server_name plain.example.test;", // plain block
		"location ~ /\\.",
		"index.php",
		"listen 80;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("php vhost missing %q", want)
		}
	}
	// app.example.test must appear in the redirect + 443 blocks, not the plain block.
	if !strings.Contains(out, "server {\n\tlisten 80;\n\tserver_name app.example.test;") {
		t.Error("secured domain should be redirected on port 80")
	}
}

func TestRenderVhostAcmeChallengeLocation(t *testing.T) {
	out := RenderVhost(VhostSpec{
		WebsiteID:    "44444444-4444-4444-4444-444444444444",
		DocumentRoot: "/srv/x",
		Domains:      []DomainSpec{{Domain: "le.example.test", SSLMode: "none"}},
	})
	if !strings.Contains(out, "location /.well-known/acme-challenge/") {
		t.Error("vhost must serve the ACME challenge webroot")
	}
	if !strings.Contains(out, "/var/www/_acme-challenge") {
		t.Error("vhost must reference the acme webroot path")
	}
}

func TestSecuredDomainsFiltering(t *testing.T) {
	v := VhostSpec{
		WebsiteID: "55555555-5555-5555-5555-555555555555",
		Domains: []DomainSpec{
			{Domain: "a.test", SSLMode: "none"},
			{Domain: "b.test", SSLMode: "selfsigned", CertPath: "/nonexistent/fullchain.pem", KeyPath: "/nonexistent/key.pem"},
			{Domain: "c.test", SSLMode: "letsencrypt"},
		},
	}
	secured := v.SecuredDomains()
	if len(secured) != 0 {
		t.Fatalf("secured domains with missing certs must be filtered, got %d", len(secured))
	}
}

func TestRenderVhostRedirectBlocks(t *testing.T) {
	id := "99999999-9999-9999-9999-999999999999"
	out := RenderVhost(VhostSpec{
		WebsiteID:    id,
		DocumentRoot: "/srv/epicpanel/websites/" + id + "/public",
		Domains: []DomainSpec{
			{Domain: "main.example.test", SSLMode: "none"},
			{Domain: "old.example.test", SSLMode: "none"},
			{Domain: "legacy.example.test", SSLMode: "none"},
		},
		Redirects: []RedirectRule{
			{From: "old.example.test", To: "https://main.example.test", Status: 302},
			{From: "legacy.example.test", To: "https://main.example.test/landing"},
		},
	})
	// Dedicated redirect server blocks with the requested status codes.
	for _, want := range []string{
		"server {\n\tlisten 80;\n\tserver_name old.example.test;\n\n\treturn 302 https://main.example.test;",
		"server {\n\tlisten 80;\n\tserver_name legacy.example.test;\n\n\treturn 301 https://main.example.test/landing;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("redirect block missing:\n%q\n---\n%s", want, out)
		}
	}
	// Redirected domains are excluded from the plain serving server_name.
	if strings.Contains(out, "server_name main.example.test old.example.test legacy.example.test;") {
		t.Errorf("redirected domains must not share the plain server_name:\n%s", out)
	}
	// The plain block still serves the non-redirected domain.
	if !strings.Contains(out, "server_name main.example.test;") {
		t.Errorf("plain block missing the serving domain:\n%s", out)
	}
}

func TestRenderVhostSuspended(t *testing.T) {
	id := "99999999-9999-9999-9999-999999999998"
	out := RenderVhost(VhostSpec{
		WebsiteID:    id,
		DocumentRoot: "/srv/epicpanel/websites/" + id + "/public",
		FpmSocket:    "/run/epicpanel/php-fpm/" + id + ".sock",
		ProxyPass:    "http://127.0.0.1:6600",
		Suspended:    true,
		Domains: []DomainSpec{
			{Domain: "app.example.test", SSLMode: "none"},
			{Domain: "www.app.example.test", SSLMode: "none"},
		},
	})
	for _, want := range []string{
		"return 503",
		"error_page 503 /suspended.html;",
		// The stub page lives in the shared default_pages dir (deployed by
		// InstallNginx, see pages.SuspendedHTML) — the vhost only points at it.
		"root /srv/epicpanel/default_pages;",
		"server_name app.example.test www.app.example.test;",
		"logs/nginx-access.log",
		"logs/nginx-error.log",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("suspended vhost missing %q:\n%s", want, out)
		}
	}
	// The site's own docroot must never be served while suspended (the only
	// root directive allowed is the default_pages stub location above).
	for _, banned := range []string{"fastcgi_pass", "proxy_pass", "try_files", "/srv/epicpanel/websites/" + id + "/public"} {
		if strings.Contains(out, banned) {
			t.Errorf("suspended vhost must not contain %q:\n%s", banned, out)
		}
	}
}

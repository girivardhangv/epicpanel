package agent

import (
	"context"
	"strings"
	"testing"
)

// App-runtime sites must render a hardened reverse-proxy vhost: websocket
// upgrade support (per-site map + Upgrade/Connection headers), forwarded
// headers, bounded timeouts and a request-body cap.
func TestRenderVhostAppProxy(t *testing.T) {
	v := VhostSpec{
		WebsiteID:    "7eja8c2e-1b2c-4d5e-9f01-23456789abcd",
		UnixUser:     "ep-org-site",
		DocumentRoot: "/srv/epicpanel/websites/7eja8c2e-1b2c-4d5e-9f01-23456789abcd/public",
		ProxyPass:    "http://127.0.0.1:12345",
		Domains:      []DomainSpec{{Domain: "app.example.com", SSLMode: "none"}},
	}
	out := RenderVhost(v)

	// websocket map: http-context, per-site variable name, no dashes
	if !strings.Contains(out, "map $http_upgrade $ep_ws_7eja8c2e_1b2c_4d5e_9f01_23456789abcd {") {
		t.Fatalf("per-site websocket map missing:\n%s", out)
	}
	if strings.Contains(out, "ep_ws_7eja8c2e-") {
		t.Fatalf("map variable name must not contain dashes (invalid nginx ident):\n%s", out)
	}
	for _, want := range []string{
		"proxy_pass http://127.0.0.1:12345;",
		"proxy_set_header Upgrade $http_upgrade;",
		"proxy_set_header Connection $ep_ws_7eja8c2e_1b2c_4d5e_9f01_23456789abcd;",
		"proxy_set_header X-Forwarded-Proto $scheme;",
		"proxy_connect_timeout 15s;",
		"proxy_read_timeout 300s;",
		"client_max_body_size 100m;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("hardened proxy block missing %q:\n%s", want, out)
		}
	}
	// Proxied sites must not fall back to file serving.
	if strings.Contains(out, "try_files") {
		t.Fatalf("proxied vhost must not render try_files:\n%s", out)
	}
}

// Static/PHP sites (no ProxyPass) must remain untouched: no map, no proxy.
func TestRenderVhostNoProxyUnchanged(t *testing.T) {
	v := VhostSpec{
		WebsiteID:    "aaaaaaaa-1b2c-4d5e-9f01-23456789abcd",
		UnixUser:     "ep-org-site",
		DocumentRoot: "/srv/epicpanel/websites/aaaaaaaa-1b2c-4d5e-9f01-23456789abcd/public",
		Domains:      []DomainSpec{{Domain: "static.example.com", SSLMode: "none"}},
	}
	out := RenderVhost(v)
	if strings.Contains(out, "map $http_upgrade") || strings.Contains(out, "proxy_pass") {
		t.Fatalf("non-proxy vhost must not contain map/proxy directives:\n%s", out)
	}
}

// RunSiteCommand re-validates the control-plane allowlist agent-side.
func TestRunSiteCommandAgentValidation(t *testing.T) {
	e := NewExecutor()
	cases := []struct {
		name    string
		argv    []string
		wantErr string
	}{
		{"empty", nil, "tokens"},
		{"too many", append(strings.Split(strings.Repeat("x ", 30), " "), "npm"), "tokens"},
		{"disallowed binary", []string{"curl", "http://evil.example"}, "not allowed"},
		{"shell metachar", []string{"npm", "install", "a`id`"}, "invalid token"},
		{"subshell", []string{"npm", "run", "$(whoami)"}, "invalid token"},
		{"semicolon", []string{"php", "-r", "1;2"}, "invalid token"},
		{"pipe", []string{"cat", "/etc/passwd|"}, "invalid token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.RunSiteCommand(context.Background(), CommandPayload{
				WebsiteID:    "aaaaaaaa-1b2c-4d5e-9f01-23456789abcd",
				DocumentRoot: "/srv/epicpanel/websites/aaaaaaaa-1b2c-4d5e-9f01-23456789abcd/public",
				Argv:         tc.argv,
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
	// Invalid document root must also be refused before any exec.
	_, err := e.RunSiteCommand(context.Background(), CommandPayload{
		WebsiteID:    "aaaaaaaa-1b2c-4d5e-9f01-23456789abcd",
		DocumentRoot: "/etc/nginx",
		Argv:         []string{"ls"},
	})
	if err == nil || !strings.Contains(err.Error(), "outside epicpanel tree") {
		t.Fatalf("docroot guard: want outside-epicpanel error, got %v", err)
	}
}

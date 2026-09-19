package agent

import (
	"strings"
	"testing"
)

// App-mode vhosts must proxy with WebSocket support: Upgrade + Connection
// (via the http-level map variable) are what keep socket.io / ws libraries
// working behind the panel's nginx edge.
func TestRenderVhostAppProxy(t *testing.T) {
	v := VhostSpec{
		WebsiteID:    "33333333-3333-3333-3333-333333333333",
		UnixUser:     "ep-org-site",
		DocumentRoot: "/srv/epicpanel/websites/33333333-3333-3333-3333-333333333333/public",
		ProxyPass:    "http://127.0.0.1:8100",
		Domains:      []DomainSpec{{Domain: "app.example.com", SSLMode: "none"}},
	}
	out := RenderVhost(v)
	for _, want := range []string{
		"proxy_pass http://127.0.0.1:8100;",
		"proxy_set_header Upgrade $http_upgrade;",
		"proxy_set_header Connection $epicpanel_connection_upgrade;",
		"proxy_read_timeout 300s;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("app-proxy vhost missing %q\ngot:\n%s", want, out)
		}
	}
	// Pure proxy mode serves no files: no try_files docroot section.
	if strings.Contains(out, "try_files") {
		t.Error("app-proxy vhost must not serve the docroot directly")
	}
}

// Docroot-mode vhosts must NOT carry proxy directives (a plain PHP site
// would render unusable if the proxy section leaked into it).
func TestRenderVhostDocrootNoProxy(t *testing.T) {
	v := VhostSpec{
		WebsiteID:    "33333333-3333-3333-3333-333333333333",
		UnixUser:     "ep-org-site",
		DocumentRoot: "/srv/epicpanel/websites/33333333-3333-3333-3333-333333333333/public",
		Domains:      []DomainSpec{{Domain: "www.example.com", SSLMode: "none"}},
	}
	out := RenderVhost(v)
	if strings.Contains(out, "proxy_pass") || strings.Contains(out, "Upgrade") {
		t.Errorf("docroot vhost must not contain proxy directives\ngot:\n%s", out)
	}
}

// deploySpecFromPayload must carry every field the build gate and app
// restart need — a missed field silently disables build gating.
func TestDeploySpecFromPayload(t *testing.T) {
	p := DeployJobPayload{
		WebsiteID:       "33333333-3333-3333-3333-333333333333",
		RepoURL:         "https://github.com/x/y",
		Branch:          "main",
		Runtime:         "node",
		RuntimeVersion:  "22",
		BuildCommand:    "npm run build",
		UnixUser:        "ep-org-site",
		StartupCommand:  "node dist/main.js",
		AppPort:         8123,
		AppDesiredState: "running",
		AppEnvEnc:       "ciphertext",
	}
	spec := deploySpecFromPayload(p)
	if spec.BuildCommand != p.BuildCommand || spec.UnixUser != p.UnixUser ||
		spec.StartupCommand != p.StartupCommand || spec.AppPort != p.AppPort ||
		spec.AppDesiredState != p.AppDesiredState || spec.AppEnvEnc != p.AppEnvEnc ||
		spec.RuntimeVersion != p.RuntimeVersion || spec.TokenCipherB64 != p.TokenEncrypted {
		t.Errorf("deploy spec lost payload fields: %+v", spec)
	}
}

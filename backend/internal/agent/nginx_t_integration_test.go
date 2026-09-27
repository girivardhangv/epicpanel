package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epicbyte/epicpanel/backend/internal/nginxcfg"
)

// Integration (spec §14/§21): a vhost rendered from a full structured
// SiteConfig must pass a REAL `nginx -t` in an isolated prefix — the final
// authority the production Ensure path also defers to. Skips when nginx is
// not on PATH (dev boxes without the binary).
func TestNginxTIntegrationSiteConfig(t *testing.T) {
	if _, err := exec.LookPath("nginx"); err != nil {
		t.Skip("nginx binary not available")
	}
	tmp := t.TempDir()
	cert := filepath.Join(tmp, "cert.pem")
	key := filepath.Join(tmp, "key.pem")
	// A real self-signed PEM: nginx -t loads and parses the key pair.
	if err := writeSelfSignedCert(cert, key); err != nil {
		t.Skipf("cannot generate test certificate: %v", err)
	}

	cfg := &nginxcfg.SiteConfig{Schema: 2, ClientMaxBodySize: "64m"}
	cfg.Headers = []nginxcfg.HeaderRule{
		{Name: "X-Frame-Options", Value: "DENY", Always: true},
		{Name: "X-Content-Type-Options", Value: "nosniff"},
	}
	cfg.IPAccess = &nginxcfg.IPAccess{
		Rules:   []nginxcfg.IPRule{{CIDR: "203.0.113.0/24", Action: "deny"}, {CIDR: "10.0.0.0/8", Action: "allow"}},
		Default: "deny",
	}
	cfg.ErrorPages = []nginxcfg.ErrorPageRule{{Status: 404, URI: "/404.html"}}
	cfg.RootLocation = &nginxcfg.RootLocationCfg{TryFiles: "$uri $uri/ /index.php?$query_string"}
	cfg.Locations = []nginxcfg.CustomLocation{
		{ID: "l1", Kind: nginxcfg.KindExact, Match: "/health", Body: "return 200 ok"},
		{ID: "l2", Kind: nginxcfg.KindPrefix, Match: "/api/", Body: "proxy_pass http://127.0.0.1:3456\nproxy_http_version 1.1\nproxy_set_header X-Real-IP $remote_addr"},
	}
	cfg.Redirects = []nginxcfg.PathRedirect{
		{From: "/old-page", To: "/new-page", Code: 301},
		{From: "^/posts/(.*)$", To: "/blog/$1", Code: 302},
	}
	cfg.AssetCaching = &nginxcfg.AssetCaching{Classes: []string{"css", "js", "img", "fonts"}, Expires: "30d"}

	v := VhostSpec{
		WebsiteID:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		UnixUser:     "ep-test-site",
		DocumentRoot: "/srv/epicpanel/websites/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/public",
		SiteConfig:   cfg,
		AcmeWebroot:  tmp,
		LogsBase:     tmp,
		BWLogPath:    filepath.Join(tmp, "bandwidth.log"),
		Domains: []DomainSpec{
			{Domain: "example.test", SSLMode: "selfsigned", CertPath: cert, KeyPath: key},
			{Domain: "plain.test", SSLMode: "none"},
		},
	}
	out := RenderVhost(v)

	// nginx -t opens listening sockets (the production Ensure path runs it
	// as root). The harness remaps the managed 80/443 listeners to
	// unprivileged ports — a no-op for the user-layer validation this test
	// exercises.
	out = strings.ReplaceAll(out, "listen 80;", "listen 18080;")
	out = strings.ReplaceAll(out, "listen 443 ssl;", "listen 18443 ssl;")

	// Isolated nginx instance: own prefix, own pid/logs, the platform
	// accounting log_format (what InstallNginx converges in production),
	// and the rendered vhost included at http level.
	confDir := filepath.Join(tmp, "conf")
	logsDir := filepath.Join(tmp, "logs")
	for _, d := range []string{confDir, logsDir, filepath.Join(tmp, "bwlogs"), filepath.Join(tmp, v.WebsiteID, "logs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "bw.conf"), []byte(bwAccountingLogFormat+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vhostPath := filepath.Join(confDir, "epicpanel-vhost.conf")
	if err := os.WriteFile(vhostPath, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	mainConf := "worker_processes 1;\nevents {}\nerror_log " + filepath.Join(logsDir, "error.log") +
		";\npid " + filepath.Join(tmp, "nginx.pid") +
		";\nhttp {\n\tinclude " + filepath.Join(tmp, "bw.conf") +
		";\n\tinclude " + vhostPath + ";\n}\n"
	mainPath := filepath.Join(confDir, "nginx.conf")
	if err := os.WriteFile(mainPath, []byte(mainConf), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("nginx", "-t", "-p", tmp+"/", "-c", mainPath)
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nginx -t rejected the layered vhost: %v\n%s", err, outBytes)
	}
	if !strings.Contains(string(outBytes), "syntax is ok") {
		t.Errorf("unexpected nginx -t output: %s", outBytes)
	}

	// Negative: the same pipeline must REJECT a vhost tampered post-save
	// with content our validators would never produce — nginx -t remains
	// the final gate before anything is reloaded in production.
	tampered := strings.Replace(out, "# EPICPANEL USER CONFIG — validated (ADR-068)",
		"# EPICPANEL USER CONFIG — validated (ADR-068)\n\ttotally_not_a_nginx_directive foo;", 1)
	if err := os.WriteFile(vhostPath, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd2 := exec.Command("nginx", "-t", "-p", tmp+"/", "-c", mainPath)
	out2, err2 := cmd2.CombinedOutput()
	if err2 == nil {
		t.Fatalf("nginx -t accepted a vhost with an injected listen directive:\n%s", out2)
	}
}

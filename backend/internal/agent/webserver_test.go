package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// Helpers: stub binaries on a temp PATH for rollback proofs.
// ============================================================================

func stubBin(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func setTestPath(t *testing.T, dirs ...string) {
	t.Helper()
	t.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))
}

func testVhostSpec(id string) VhostSpec {
	return VhostSpec{
		WebsiteID:    id,
		DocumentRoot: "/srv/epicpanel/websites/" + id + "/public",
		Domains:      []DomainSpec{{Domain: "site.example.test", SSLMode: "none"}},
	}
}

// ============================================================================
// Atomic write + validated swap.
// ============================================================================

func TestAtomicWriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conf", "file.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "one\n" {
		t.Fatalf("read back: %q err=%v", b, err)
	}
	if err := AtomicWriteFile(path, []byte("two\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "two\n" {
		t.Fatalf("overwrite failed: %q", b)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("perm = %v, want 0640", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestSwapValidated(t *testing.T) {
	dir := t.TempDir()

	// Failing validation with an existing file -> old content restored.
	path := filepath.Join(dir, "existing.conf")
	if err := os.WriteFile(path, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := SwapValidated(path, []byte("NEW"), 0o644, func() error { return errors.New("boom: syntax not ok") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want validation error, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "OLD" {
		t.Fatalf("old content not restored: %q", b)
	}

	// Failing validation with no previous file -> new file removed.
	missing := filepath.Join(dir, "missing.conf")
	err = SwapValidated(missing, []byte("NEW"), 0o644, func() error { return errors.New("nope") })
	if err == nil {
		t.Fatal("want error for failing validate on fresh file")
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("fresh file must be removed on failure, stat err=%v", statErr)
	}

	// Passing validation -> new content stays.
	ok := filepath.Join(dir, "ok.conf")
	if err := SwapValidated(ok, []byte("NEW"), 0o644, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ok); string(b) != "NEW" {
		t.Fatalf("validated content not kept: %q", b)
	}
}

func TestValidateCmd(t *testing.T) {
	dir := t.TempDir()
	stubBin(t, dir, "passer", "exit 0")
	stubBin(t, dir, "failer", "echo 'syntax is bad'; exit 3")
	stubBin(t, dir, "sleeper", "sleep 5")
	setTestPath(t, dir)
	ctx := context.Background()

	if err := ValidateCmd(ctx, 5*time.Second, "passer"); err != nil {
		t.Fatalf("passer: %v", err)
	}
	err := ValidateCmd(ctx, 5*time.Second, "failer")
	if err == nil || !strings.Contains(err.Error(), "syntax is bad") {
		t.Fatalf("failer: want tail output in error, got %v", err)
	}
	err = ValidateCmd(ctx, 100*time.Millisecond, "sleeper")
	if err == nil {
		t.Fatal("sleeper: want timeout error")
	}
}

// ============================================================================
// Render matrix: 3 providers x {static, php, redirects, suspended}.
// ============================================================================

func TestRenderMatrix(t *testing.T) {
	const id = "33333333-3333-3333-3333-333333333333"
	docroot := "/srv/epicpanel/websites/" + id + "/public"
	sock := "/run/epicpanel/php-fpm/" + id + ".sock"

	staticSpec := VhostSpec{
		WebsiteID:    id,
		DocumentRoot: docroot,
		Domains:      []DomainSpec{{Domain: "site.example.test", SSLMode: "none"}},
	}
	phpSpec := staticSpec
	phpSpec.FpmSocket = sock
	redirectSpec := staticSpec
	redirectSpec.Domains = append(redirectSpec.Domains, DomainSpec{Domain: "old.example.test", SSLMode: "none"})
	redirectSpec.Redirects = []RedirectRule{{From: "old.example.test", To: "https://new.example.test", Status: 301}}
	suspendedSpec := phpSpec // FPM set on purpose: suspended must drop it
	suspendedSpec.Suspended = true
	suspendedSpec.ProxyPass = "http://127.0.0.1:6600" // must be dropped too

	cells := []struct {
		provider string
		kind     string
		out      string
		want     []string
		deny     []string
	}{
		{"nginx", "static", RenderVhost(staticSpec),
			[]string{"server_name site.example.test;", "try_files $uri $uri/ =404;", "access_log "},
			[]string{"fastcgi_pass", "proxy_pass", "listen 443"}},
		{"nginx", "php", RenderVhost(phpSpec),
			[]string{"fastcgi_pass unix:" + sock + ";"},
			nil},
		{"nginx", "redirects", RenderVhost(redirectSpec),
			[]string{"server {\n\tlisten 80;\n\tserver_name old.example.test;\n", "return 301 https://new.example.test;"},
			[]string{"server_name site.example.test old.example.test"}},
		{"nginx", "suspended", RenderVhost(suspendedSpec),
			[]string{"return 503", "Account suspended", "nginx-access.log"},
			[]string{"fastcgi_pass", "proxy_pass", "try_files"}},
		{"apache", "static", RenderApacheSite(staticSpec, 6600),
			[]string{"DocumentRoot " + docroot, "Listen 127.0.0.1:6600", "apache-access.log"},
			[]string{"SetHandler", "proxy:unix"}},
		{"apache", "php", RenderApacheSite(phpSpec, 6600),
			[]string{"SetHandler \"proxy:unix:" + sock + "|fcgi://localhost\""},
			nil},
		{"apache", "redirects", RenderApacheSite(redirectSpec, 6600),
			[]string{"Redirect 301 / https://new.example.test"},
			nil},
		{"apache", "suspended", RenderApacheSite(suspendedSpec, 6600),
			[]string{"R=503", "Account suspended", "apache-error.log"},
			[]string{"SetHandler", "proxy:unix"}},
		{"ols", "static", RenderOLSVhconf(staticSpec, 7100),
			[]string{"docRoot", "ols-access.log"},
			[]string{"extProcessor", "fcgi"}},
		{"ols", "php", RenderOLSVhconf(phpSpec, 7100),
			[]string{"extProcessor fpm-" + id, "uds://" + strings.TrimPrefix(sock, "/")},
			nil},
		{"ols", "redirects", RenderOLSVhconf(redirectSpec, 7100),
			[]string{"rewriteCond  %{HTTP_HOST}  ^old\\.example\\.test$  [NC]", "rewriteRule  ^/.*$  https://new.example.test  [R=301,L]"},
			nil},
		{"ols", "suspended", RenderOLSVhconf(suspendedSpec, 7100),
			[]string{"503", "Account suspended", "ols-error.log"},
			[]string{"extProcessor", "fcgi"}},
	}

	for _, c := range cells {
		for _, want := range c.want {
			if !strings.Contains(c.out, want) {
				t.Errorf("%s/%s: output missing %q\n---\n%s", c.provider, c.kind, want, c.out)
			}
		}
		for _, deny := range c.deny {
			if strings.Contains(c.out, deny) {
				t.Errorf("%s/%s: output must not contain %q\n---\n%s", c.provider, c.kind, deny, c.out)
			}
		}
	}
}

func TestRedirectStatusDefaultsTo301(t *testing.T) {
	const id = "33333333-3333-3333-3333-333333333333"
	spec := testVhostSpec(id)
	spec.Redirects = []RedirectRule{{From: "old.example.test", To: "https://new.example.test"}}
	out := RenderVhost(spec)
	if !strings.Contains(out, "return 301 https://new.example.test;") {
		t.Fatalf("status 0 must default to 301:\n%s", out)
	}
	if !strings.Contains(RenderApacheSite(spec, 6600), "Redirect 301 / https://new.example.test") {
		t.Error("apache: status 0 must default to 301")
	}
	if !strings.Contains(RenderOLSVhconf(spec, 7100), "[R=301,L]") {
		t.Error("ols: status 0 must default to 301")
	}
}

func TestNormalizeRedirectsDropsInvalid(t *testing.T) {
	rules := []RedirectRule{
		{From: "ok.example.test", To: "https://x.test", Status: 307},
		{From: "Not A Domain", To: "https://x.test", Status: 301},       // bad domain
		{From: "ok.example.test", To: "java script:alert", Status: 301}, // bad target
		{From: "ok.example.test", To: "/rel", Status: 999},              // bad status
	}
	got := normalizeRedirects("some-id", rules)
	if len(got) != 1 || got[0].Status != 307 {
		t.Fatalf("want only the valid 307 rule, got %+v", got)
	}
}

// ============================================================================
// Shared rewrite core: smuggling checks across all three providers.
// ============================================================================

func TestRewriteCoreSmugglingDropped(t *testing.T) {
	const id = "33333333-3333-3333-3333-333333333333"
	spec := testVhostSpec(id)
	// The first line is a legit rewrite; the rest must all be dropped.
	spec.RewriteRules = strings.Join([]string{
		"rewrite ^/blog/(.*) /blog/index.php permanent",
		"rewrite ^/x /y; rm -rf /",        // smuggled ;
		"rewrite ^/x /y { bad }",          // structure chars
		"rewrite ^/x /y `backtick`",       // backtick
		"include /etc/nginx/evil.conf",    // not in token set
		"proxy_pass http://evil.internal", // not in token set
	}, "\n")

	out := RenderVhost(spec)
	if !strings.Contains(out, "rewrite ^/blog/(.*) /blog/index.php permanent") {
		t.Error("legit nginx rewrite line must survive")
	}
	for _, banned := range []string{"rm -rf", "`backtick`", "include /etc/nginx/evil.conf", "proxy_pass"} {
		if strings.Contains(out, banned) {
			t.Errorf("nginx output leaked %q", banned)
		}
	}
}

func TestOLSOnlyMapsRewriteLines(t *testing.T) {
	const id = "33333333-3333-3333-3333-333333333333"
	spec := testVhostSpec(id)
	spec.RewriteRules = strings.Join([]string{
		"rewrite ^/a/(.*) /a/index.php",
		"deny all",                 // nginx-only directive -> dropped for OLS
		"rewrite ^/bad /x; rm -rf", // smuggled -> dropped
	}, "\n")
	out := RenderOLSVhconf(spec, 7100)
	if !strings.Contains(out, "rewrite  ^/a/(.*)  /a/index.php") {
		t.Errorf("ols rewrite mapping missing:\n%s", out)
	}
	if strings.Contains(out, "deny") || strings.Contains(out, "rm -rf") {
		t.Errorf("ols leaked a non-rewrite or smuggled line:\n%s", out)
	}
}

// ============================================================================
// ROLLBACK PROOFS.
// ============================================================================

func TestNginxEnsureRollback(t *testing.T) {
	base := t.TempDir()
	failBin, passBin := filepath.Join(base, "fail"), filepath.Join(base, "pass")
	for _, d := range []string{failBin, passBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	failScript := `if [ "$1" = "-t" ]; then echo "nginx: configuration file /etc/nginx/nginx.conf test failed"; exit 1; fi
exit 0`
	stubBin(t, failBin, "nginx", failScript)
	stubBin(t, failBin, "systemctl", "exit 0")
	stubBin(t, passBin, "nginx", "exit 0")
	stubBin(t, passBin, "systemctl", "exit 0")

	id := "44444444-4444-4444-4444-444444444444"
	availDir := filepath.Join(base, "sites-available")
	enabledDir := filepath.Join(base, "sites-enabled")
	ng := &NginxProvider{
		AvailableDir: availDir,
		EnabledDir:   enabledDir,
		ACMEWebroot:  filepath.Join(base, "acme"),
		LogsBase:     filepath.Join(base, "srv"),
	}
	spec := testVhostSpec(id)
	newContent := RenderVhost(spec)
	avail := filepath.Join(availDir, "epicpanel-"+id+".conf")
	enabled := filepath.Join(enabledDir, "epicpanel-"+id+".conf")

	// Fresh site + failing nginx -t: error, no file, no enabled symlink.
	setTestPath(t, failBin)
	if err := ng.Ensure(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "nginx config validation failed") {
		t.Fatalf("fresh failing ensure: want validation error, got %v", err)
	}
	if _, err := os.Stat(avail); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed ensure must not leave the vhost file, err=%v", err)
	}
	if _, err := os.Lstat(enabled); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed ensure must not leave an enabled symlink, err=%v", err)
	}

	// Previous content + failing nginx -t: previous file content intact.
	oldContent := "# previous valid config\n"
	if err := os.MkdirAll(availDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(enabledDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(avail, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(avail, enabled); err != nil {
		t.Fatal(err)
	}
	if err := ng.Ensure(context.Background(), spec); err == nil {
		t.Fatal("existing failing ensure must error")
	}
	if b, _ := os.ReadFile(avail); string(b) != oldContent {
		t.Fatalf("previous content not intact: %q", b)
	}

	// Passing stub: ensure succeeds, enables and reloads.
	setTestPath(t, passBin)
	if err := ng.Ensure(context.Background(), spec); err != nil {
		t.Fatalf("passing ensure: %v", err)
	}
	if b, _ := os.ReadFile(avail); string(b) != newContent {
		t.Fatalf("new content not written:\n%s", b)
	}
	link, err := os.Readlink(enabled)
	if err != nil || link != avail {
		t.Fatalf("enabled symlink wrong: %q err=%v", link, err)
	}
}

func TestNginxEnsureIdempotentShortCircuit(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(base, "calls")
	stubBin(t, bin, "nginx", "exit 0")
	stubBin(t, bin, "systemctl", "exit 0")
	setTestPath(t, bin)

	id := "44444444-4444-4444-4444-444444444444"
	ng := &NginxProvider{
		AvailableDir: filepath.Join(base, "avail"),
		EnabledDir:   filepath.Join(base, "enabled"),
		ACMEWebroot:  filepath.Join(base, "acme"),
		LogsBase:     filepath.Join(base, "srv"),
	}
	spec := testVhostSpec(id)
	if err := ng.Ensure(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	// Second identical ensure must not re-run nginx -t (idempotency
	// short-circuit): swap the stub for a failing one and expect success.
	stubBin(t, bin, "nginx", `if [ "$1" = "-t" ]; then exit 1; fi
exit 0`)
	if err := ng.Ensure(context.Background(), spec); err != nil {
		t.Fatalf("identical ensure must short-circuit before validation: %v", err)
	}
	_ = calls
}

func TestApacheEnsureRollback(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stubBin(t, bin, "apache2", "exit 0")
	stubBin(t, bin, "apache2ctl", `if [ "$1" = "configtest" ] && [ -n "$FAIL_CONFIGTEST" ]; then echo "Syntax error on line 42 of epicpanel.conf"; exit 1; fi
exit 0`)
	stubBin(t, bin, "systemctl", `echo "$1 $2" >> "$EPICPANEL_CALLS"
exit 0`)
	stubBin(t, bin, "a2dissite", "exit 0")
	stubBin(t, bin, "a2enconf", "exit 0")
	stubBin(t, bin, "a2ensite", "exit 0")
	t.Setenv("EPICPANEL_CALLS", filepath.Join(base, "calls"))
	setTestPath(t, bin)

	id := "55555555-5555-5555-5555-555555555555"
	availDir := filepath.Join(base, "sites-available")
	enabledDir := filepath.Join(base, "sites-enabled")
	portsConf := filepath.Join(base, "ports.conf")
	serverNameConf := filepath.Join(base, "servername.conf")
	prov := &ApacheProvider{
		Ex:             NewExecutor(),
		SitesAvailable: availDir,
		SitesEnabled:   enabledDir,
		PortsConf:      portsConf,
		ServerNameConf: serverNameConf,
		LogsBase:       filepath.Join(base, "srv"),
	}
	spec := testVhostSpec(id)
	spec.BackendPort = 6600
	file := filepath.Join(availDir, "epicpanel-"+id+".conf")
	enabled := filepath.Join(enabledDir, "epicpanel-"+id+".conf")

	// Failing configtest with prior state: previous file intact, enable-state
	// rolled back (link stays because it existed before).
	oldContent := "# old apache vhost\nListen 127.0.0.1:6600\n"
	if err := os.MkdirAll(availDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(enabledDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, enabled); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAIL_CONFIGTEST", "1")
	if err := prov.Ensure(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "configtest failed") {
		t.Fatalf("want configtest error, got %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != oldContent {
		t.Fatalf("apache previous content not intact: %q", b)
	}
	if _, err := os.Lstat(enabled); err != nil {
		t.Fatalf("previously enabled site must stay enabled after rollback: %v", err)
	}

	// Failing configtest, fresh site: enable-state rollback removes the link.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(enabled); err != nil {
		t.Fatal(err)
	}
	if err := prov.Ensure(context.Background(), spec); err == nil {
		t.Fatal("fresh failing ensure must error")
	}
	if _, err := os.Lstat(enabled); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh failed ensure must not leave an enabled symlink: %v", err)
	}
	t.Setenv("FAIL_CONFIGTEST", "")

	// Passing configtest, existing vhost with unchanged Listen port -> reload.
	t.Setenv("EPICPANEL_CALLS", filepath.Join(base, "calls-reload"))
	if err := os.WriteFile(file, []byte(oldContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, enabled); err != nil {
		t.Fatal(err)
	}
	if err := prov.Ensure(context.Background(), spec); err != nil {
		t.Fatalf("passing ensure: %v", err)
	}
	callsB, _ := os.ReadFile(filepath.Join(base, "calls-reload"))
	if !strings.Contains(string(callsB), "reload apache2") {
		t.Fatalf("existing vhost with unchanged port must reload, calls: %s", callsB)
	}

	// New vhost (no previous file) -> restart.
	t.Setenv("EPICPANEL_CALLS", filepath.Join(base, "calls-restart"))
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := prov.Ensure(context.Background(), spec); err != nil {
		t.Fatalf("fresh passing ensure: %v", err)
	}
	callsB, _ = os.ReadFile(filepath.Join(base, "calls-restart"))
	if !strings.Contains(string(callsB), "restart apache2") {
		t.Fatalf("new vhost must restart, calls: %s", callsB)
	}
}

func TestOLSEnsureRollback(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stubBin(t, bin, "systemctl", `if [ "$1" = "restart" ] && [ "$2" = "lsws" ] && [ -n "$OLS_FAIL_RESTART" ]; then echo "job for lsws.service failed"; exit 1; fi
exit 0`)
	setTestPath(t, bin)

	id := "66666666-6666-6666-6666-666666666666"
	vhostDir := filepath.Join(base, "vhosts")
	httpdConf := filepath.Join(base, "httpd_config.conf")
	confOriginal := "# ols httpd config — no user/group lines\n"
	if err := os.WriteFile(httpdConf, []byte(confOriginal), 0o640); err != nil {
		t.Fatal(err)
	}
	vhconf := filepath.Join(vhostDir, id, "vhconf.conf")
	vhOriginal := "# old vhconf\n"
	if err := os.MkdirAll(filepath.Dir(vhconf), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vhconf, []byte(vhOriginal), 0o644); err != nil {
		t.Fatal(err)
	}
	// Detached-fallback stub: a real lswsctrl on the host would succeed where
	// the test needs a failure, so point the provider at a stub that reports
	// failure for the restart subcommand.
	ctrlStub := filepath.Join(base, "lswsctrl-fail", "lswsctrl")
	if err := os.MkdirAll(filepath.Dir(ctrlStub), 0o755); err != nil {
		t.Fatal(err)
	}
	stubBin(t, filepath.Dir(ctrlStub), "lswsctrl", `echo "lswsctrl: restart failed"; exit 1`)
	prov := &OLSProvider{
		Ex:          NewExecutor(),
		VhostDir:    vhostDir,
		HTTPDConfig: httpdConf,
		LogsBase:    filepath.Join(base, "srv"),
		LSWSCtrl:    ctrlStub,
	}
	spec := testVhostSpec(id)
	spec.BackendPort = 7100

	// Failing restart: BOTH conf files restored, error returned.
	t.Setenv("OLS_FAIL_RESTART", "1")
	err := prov.Ensure(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "openlitespeed restart failed") {
		t.Fatalf("want restart failure error, got %v", err)
	}
	if b, _ := os.ReadFile(httpdConf); string(b) != confOriginal {
		t.Fatalf("httpd_config.conf not restored: %q", b)
	}
	if b, _ := os.ReadFile(vhconf); string(b) != vhOriginal {
		t.Fatalf("vhconf not restored: %q", b)
	}

	// Passing restart: both files updated, listener registered.
	t.Setenv("OLS_FAIL_RESTART", "")
	if err := prov.Ensure(context.Background(), spec); err != nil {
		t.Fatalf("passing ensure: %v", err)
	}
	confB, _ := os.ReadFile(httpdConf)
	if !strings.Contains(string(confB), "virtualhost "+id) || !strings.Contains(string(confB), "address 127.0.0.1:7100") {
		t.Fatalf("httpd_config.conf missing vhost/listener:\n%s", confB)
	}
	vhB, _ := os.ReadFile(vhconf)
	if !strings.Contains(string(vhB), "docRoot") {
		t.Fatalf("vhconf not rendered:\n%s", vhB)
	}
}

// ============================================================================
// Payload wiring.
// ============================================================================

func TestProvisionPayloadNewFieldsJSON(t *testing.T) {
	body, err := json.Marshal(ProvisionPayload{
		WebsiteID:        "77777777-7777-7777-7777-777777777777",
		Redirects:        []RedirectRule{{From: "old.example.test", To: "https://new.example.test", Status: 301}},
		Suspended:        true,
		FpmMemoryLimitMB: 256,
		FpmMaxChildren:   20,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{
		`"redirects":[{"from":"old.example.test","to":"https://new.example.test","status":301}]`,
		`"suspended":true`,
		`"fpm_memory_limit_mb":256`,
		`"fpm_max_children":20`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("payload json missing %s: %s", want, s)
		}
	}
}

func TestRenderPoolConfigLimits(t *testing.T) {
	id := "88888888-8888-8888-8888-888888888888"
	out := RenderPoolConfig(PoolSpec{WebsiteID: id, UnixUser: "ep-test", RuntimeVer: "8.3", PMMaxChildren: 25, ProcessMemory: "512M"})
	if !strings.Contains(out, "pm.max_children = 25") {
		t.Errorf("pool missing tuned max_children:\n%s", out)
	}
	if !strings.Contains(out, "php_admin_value[memory_limit] = 512M") {
		t.Errorf("pool missing tuned memory_limit:\n%s", out)
	}
	def := RenderPoolConfig(PoolSpec{WebsiteID: id, UnixUser: "ep-test", RuntimeVer: "8.3"})
	if !strings.Contains(def, "pm.max_children = 10") || !strings.Contains(def, "php_admin_value[memory_limit] = 128M") {
		t.Errorf("pool defaults wrong:\n%s", def)
	}
}

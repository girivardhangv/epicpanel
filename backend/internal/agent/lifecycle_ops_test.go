package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// Lifecycle (suspend/resume) tests — temp dirs + stubbed binaries, mirroring
// the injection pattern of webserver_test.go.
// ============================================================================

type lifeFixture struct {
	base      string
	bin       string
	paths     lifePaths
	nginxConf string
	nginxLink string
	execLog   string
}

func newLifeFixture(t *testing.T, nginxTestFails bool) *lifeFixture {
	t.Helper()
	base := t.TempDir()
	t.Cleanup(func() { lifePathsOverride = nil })
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// nginx stub: -t fails or passes on demand; every invocation logged.
	nginxScript := `echo "$@" >> "$LIFE_EXEC_LOG"
if [ "$1" = "-t" ] && [ "$LIFE_FAIL" = "1" ]; then exit 1; fi
exit 0`
	// systemctl stub: log + succeed (reload/restart/enable/start all fine).
	stubBin(t, bin, "nginx", nginxScript)
	stubBin(t, bin, "systemctl", `echo "$@" >> "$LIFE_EXEC_LOG"
exit 0`)
	stubBin(t, bin, "apache2ctl", `echo "$@" >> "$LIFE_EXEC_LOG"
if [ "$1" = "configtest" ] && [ "$LIFE_FAIL" = "1" ]; then exit 1; fi
exit 0`)
	t.Setenv("LIFE_EXEC_LOG", filepath.Join(base, "exec.log"))
	if nginxTestFails {
		t.Setenv("LIFE_FAIL", "1")
	}
	f := &lifeFixture{
		base:      base,
		bin:       bin,
		execLog:   filepath.Join(base, "exec.log"),
		nginxConf: "site.conf original content\n\tserver_name a.example.test b.example.test;\n\troot /srv/epicpanel/websites/x/public;\n",
	}
	lifePathsOverride = &f.paths // set before fixture paths exist; fine, resolved lazily
	f.paths = lifePaths{
		NginxAvail:    filepath.Join(base, "sites-available"),
		NginxEnabled:  filepath.Join(base, "sites-enabled"),
		ApacheAvail:   filepath.Join(base, "apache-available"),
		ApacheEnabled: filepath.Join(base, "apache-enabled"),
		OLSVhosts:     filepath.Join(base, "lsws-vhosts"),
		OLSHTTPD:      filepath.Join(base, "httpd_config.conf"),
	}
	for _, d := range []string{f.paths.NginxAvail, f.paths.NginxEnabled, f.paths.ApacheAvail, f.paths.ApacheEnabled} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(f.paths.OLSVhosts, "wl", ""), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *lifeFixture) calls(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(f.execLog)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (f *lifeFixture) assertCallCount(t *testing.T, substr string, want int) {
	t.Helper()
	n := 0
	for _, c := range f.calls(t) {
		if strings.Contains(c, substr) {
			n++
		}
	}
	if n != want {
		t.Fatalf("calls containing %q: got %d, want %d (all: %v)", substr, n, want, f.calls(t))
	}
}

func writeLifeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const lifeTestID = "77777777-7777-7777-7777-777777777777"

func TestLifeNginxSuspendResumeRoundTrip(t *testing.T) {
	f := newLifeFixture(t, false)
	setTestPath(t, f.bin)
	conf := filepath.Join(f.paths.NginxAvail, "epicpanel-"+lifeTestID+".conf")
	writeLifeFile(t, conf, f.nginxConf)
	if err := os.Symlink(conf, filepath.Join(f.paths.NginxEnabled, "epicpanel-"+lifeTestID+".conf")); err != nil {
		t.Fatal(err)
	}

	ex := &Executor{}
	out, err := ex.SuspendWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID})
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if !out.Suspended || out.AlreadyInState {
		t.Fatalf("suspend outcome wrong: %+v", out)
	}
	bak := conf + lifeSuspendBakSuffix
	if b, err := os.ReadFile(bak); err != nil || string(b) != f.nginxConf {
		t.Fatalf("backup content mismatch: %q err=%v", b, err)
	}
	live, _ := os.ReadFile(conf)
	if !strings.Contains(string(live), "return 503") {
		t.Fatalf("live config is not a suspend stub:\n%s", live)
	}
	if strings.Contains(string(live), "fastcgi_pass") || strings.Contains(string(live), "proxy_pass") {
		t.Fatalf("stub must be a plain 503 without PHP or proxy:\n%s", live)
	}
	if !strings.Contains(string(live), "server_name a.example.test b.example.test;") {
		t.Fatalf("stub must preserve parsed domains:\n%s", live)
	}
	f.assertCallCount(t, "reload nginx", 1)

	// Idempotent suspend: no second reload.
	if _, err := ex.SuspendWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID}); err != nil {
		t.Fatalf("second suspend: %v", err)
	}
	f.assertCallCount(t, "reload nginx", 1)

	// Resume restores byte-identical original and removes the backup.
	res, err := ex.ResumeWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res.Resumed {
		t.Fatalf("resume outcome wrong: %+v", res)
	}
	got, err := os.ReadFile(conf)
	if err != nil || string(got) != f.nginxConf {
		t.Fatalf("resume did not restore original: %q err=%v", got, err)
	}
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Fatalf("backup must be removed after resume, err=%v", err)
	}
	f.assertCallCount(t, "reload nginx", 2)

	// Resume again: already resumed, no reload.
	if _, err := ex.ResumeWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID}); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	f.assertCallCount(t, "reload nginx", 2)
}

func TestLifeSuspendValidationRollback(t *testing.T) {
	f := newLifeFixture(t, true) // nginx -t always fails
	setTestPath(t, f.bin)
	conf := filepath.Join(f.paths.NginxAvail, "epicpanel-"+lifeTestID+".conf")
	writeLifeFile(t, conf, f.nginxConf)
	if err := os.Symlink(conf, filepath.Join(f.paths.NginxEnabled, "epicpanel-"+lifeTestID+".conf")); err != nil {
		t.Fatal(err)
	}

	ex := &Executor{}
	if _, err := ex.SuspendWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID}); err == nil {
		t.Fatal("suspend must fail when nginx -t rejects the stub")
	}
	got, err := os.ReadFile(conf)
	if err != nil || string(got) != f.nginxConf {
		t.Fatalf("failed suspend must leave the original config intact: %q err=%v", got, err)
	}
	if _, err := os.Stat(conf + lifeSuspendBakSuffix); !os.IsNotExist(err) {
		t.Fatalf("failed suspend must roll back its fresh backup, err=%v", err)
	}
}

func TestLifeResumeNoBackupNoop(t *testing.T) {
	f := newLifeFixture(t, false)
	setTestPath(t, f.bin)
	conf := filepath.Join(f.paths.NginxAvail, "epicpanel-"+lifeTestID+".conf")
	writeLifeFile(t, conf, f.nginxConf)

	ex := &Executor{}
	out, err := ex.ResumeWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if out.Resumed || !out.AlreadyInState {
		t.Fatalf("resume without backup must be a no-op: %+v", out)
	}
	got, _ := os.ReadFile(conf)
	if string(got) != f.nginxConf {
		t.Fatalf("no-op resume must not touch the config: %q", got)
	}
}

func TestLifeApacheRoundTripPreservesPort(t *testing.T) {
	f := newLifeFixture(t, false)
	setTestPath(t, f.bin)
	conf := filepath.Join(f.paths.ApacheAvail, "epicpanel-"+lifeTestID+".conf")
	original := `# managed by EpicPanel — website x — DO NOT EDIT
Listen 127.0.0.1:6612
<VirtualHost 127.0.0.1:6612>
	ServerName a.example.test b.example.test
	DocumentRoot /srv/epicpanel/websites/x/public

	<Directory /srv/epicpanel/websites/x/public>
		Options -Indexes +FollowSymLinks
		AllowOverride All
		Require all granted
	</Directory>
	ErrorLog /srv/epicpanel/websites/x/logs/apache-error.log
	CustomLog /srv/epicpanel/websites/x/logs/apache-access.log combined
</VirtualHost>
`
	writeLifeFile(t, conf, original)

	ex := &Executor{}
	out, err := ex.SuspendWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID})
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if !out.Suspended {
		t.Fatalf("suspend outcome wrong: %+v", out)
	}
	live, _ := os.ReadFile(conf)
	if !strings.Contains(string(live), "R=503") {
		t.Fatalf("apache stub must serve 503:\n%s", live)
	}
	if !strings.Contains(string(live), "Listen 127.0.0.1:6612") {
		t.Fatalf("apache stub must preserve the loopback Listen port:\n%s", live)
	}
	if !strings.Contains(string(live), "ServerName a.example.test b.example.test") {
		t.Fatalf("apache stub must preserve ServerName:\n%s", live)
	}

	res, err := ex.ResumeWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res.Resumed {
		t.Fatalf("resume outcome wrong: %+v", res)
	}
	got, _ := os.ReadFile(conf)
	if string(got) != original {
		t.Fatalf("apache resume must restore byte-identical config:\n--- got ---\n%s\n--- want ---\n%s", got, original)
	}
	if _, err := os.Stat(conf + lifeSuspendBakSuffix); !os.IsNotExist(err) {
		t.Fatalf("backup must be removed after resume, err=%v", err)
	}
}

func TestLifeOLSRoundTrip(t *testing.T) {
	f := newLifeFixture(t, false)
	// lswsctrl must be ABSENT so restartOLS uses the systemctl stub.
	// OLS vhost + listener registered in httpd_config.conf (agent-rendered).
	httpd := `user www-data
group www-data

virtualhost ` + lifeTestID + ` {
  vhRoot                  /srv/epicpanel/websites/` + lifeTestID + `/public
  configFile              ` + filepath.Join(f.paths.OLSVhosts, lifeTestID, "vhconf.conf") + `
  allowSymbolLink         1
  enableScript            1
  restrained              0
}

listener epicpanel-` + lifeTestID + ` {
  address 127.0.0.1:7112
  secure 0
  map ` + lifeTestID + ` a.example.test, b.example.test
}
`
	writeLifeFile(t, f.paths.OLSHTTPD, httpd)
	vhconf := filepath.Join(f.paths.OLSVhosts, lifeTestID, "vhconf.conf")
	original := "docRoot                   $VH_ROOT\nvhDomain                  $VH_NAME\nerrorlog $VH_ROOT/logs/ols-error.log {\n  useServer               1\n}\n"
	writeLifeFile(t, vhconf, original)

	setTestPath(t, f.bin)
	ex := &Executor{}
	out, err := ex.SuspendWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID})
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if !out.Suspended {
		t.Fatalf("suspend outcome wrong: %+v", out)
	}
	live, _ := os.ReadFile(vhconf)
	if !strings.Contains(string(live), "R=503") {
		t.Fatalf("OLS stub must serve 503:\n%s", live)
	}
	bak, err := os.ReadFile(vhconf + lifeSuspendBakSuffix)
	if err != nil || string(bak) != original {
		t.Fatalf("OLS backup mismatch: %q err=%v", bak, err)
	}
	f.assertCallCount(t, "restart lsws", 1)

	res, err := ex.ResumeWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res.Resumed {
		t.Fatalf("resume outcome wrong: %+v", res)
	}
	got, _ := os.ReadFile(vhconf)
	if string(got) != original {
		t.Fatalf("OLS resume must restore byte-identical config:\n%s", got)
	}
	f.assertCallCount(t, "restart lsws", 2)
}

func TestLifeOLSResumeRestartFailRollsBack(t *testing.T) {
	f := newLifeFixture(t, false)
	// systemctl stub that FAILS restarts of lsws but passes everything else.
	bin2 := filepath.Join(f.base, "bin2")
	if err := os.MkdirAll(bin2, 0o755); err != nil {
		t.Fatal(err)
	}
	// Never fall back to the real lswsctrl on this host: point it at a
	// hard-failing stub.
	ctrl := filepath.Join(bin2, "lswsctrl")
	stubBin(t, bin2, "lswsctrl", "exit 1")
	f.paths.LSWSCtrl = ctrl
	stubBin(t, bin2, "systemctl", `echo "$@" >> "$LIFE_EXEC_LOG"
if [ "$1" = "restart" ] && [ "$2" = "lsws" ]; then exit 1; fi
exit 0`)
	setTestPath(t, bin2)
	httpd := "virtualhost " + lifeTestID + " {\n  vhRoot /srv/epicpanel/websites/x/public\n}\n\nlistener epicpanel-" + lifeTestID + " {\n  address 127.0.0.1:7113\n  secure 0\n  map " + lifeTestID + " a.example.test\n}\n"
	writeLifeFile(t, f.paths.OLSHTTPD, httpd)
	vhconf := filepath.Join(f.paths.OLSVhosts, lifeTestID, "vhconf.conf")
	writeLifeFile(t, vhconf, "docRoot $VH_ROOT\n")

	ex := &Executor{}
	if _, err := ex.SuspendWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID}); err == nil {
		t.Fatal("suspend must fail when OLS restart fails")
	}
	// Original content restored after the failed restart.
	got, _ := os.ReadFile(vhconf)
	if string(got) != "docRoot $VH_ROOT\n" {
		t.Fatalf("failed suspend must restore original vhconf:\n%s", got)
	}
}

func TestLifeSuspendInvalidUUID(t *testing.T) {
	ex := &Executor{}
	if _, err := ex.SuspendWebsite(context.Background(), LifecycleJobPayload{WebsiteID: "not-a-uuid"}); err == nil {
		t.Fatal("invalid uuid must error")
	}
	if _, err := ex.ResumeWebsite(context.Background(), LifecycleJobPayload{WebsiteID: "not-a-uuid"}); err == nil {
		t.Fatal("invalid uuid must error")
	}
}

func TestLifeNoConfigsFound(t *testing.T) {
	f := newLifeFixture(t, false)
	setTestPath(t, f.bin)
	ex := &Executor{}
	out, err := ex.SuspendWebsite(context.Background(), LifecycleJobPayload{WebsiteID: lifeTestID})
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if !out.AlreadyInState || len(out.Providers) != 0 {
		t.Fatalf("no-config suspend must be an idempotent no-op: %+v", out)
	}
}

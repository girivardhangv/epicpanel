package websites

import (
	"strings"
	"testing"
)

func TestGenerateLinuxFPM(t *testing.T) {
	cfg := SiteConfig{
		Domain:       "example.com",
		Aliases:      []string{"shop.example.com"},
		IncludeWWW:   true,
		DocumentRoot: "/srv/panel/sites/example.com/public",
		PHPVersion:   "8.4",
		FastCGIAddr:  "unix:/run/php/epicpanel-example.com.sock",
		AccessLog:    "/srv/panel/sites/example.com/logs/access.log",
		ErrorLog:     "/srv/panel/sites/example.com/logs/error.log",
	}
	out := Generate(cfg)

	checks := []string{
		"server_name example.com www.example.com shop.example.com;",
		"root /srv/panel/sites/example.com/public;",
		"fastcgi_pass unix:/run/php/epicpanel-example.com.sock;",
		"fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;",
		"access_log /srv/panel/sites/example.com/logs/access.log;",
		"try_files $uri $uri/ /index.php?$query_string;",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("generated config missing %q\n---\n%s", want, out)
		}
	}
}

func TestGenerateWindowsFastCGI(t *testing.T) {
	out := Generate(SiteConfig{
		Domain:       "store.example.com",
		DocumentRoot: `C:\Panel\Sites\store.example.com\public`,
		PHPVersion:   "8.3",
		FastCGIAddr:  "127.0.0.1:9001",
		AccessLog:    `C:\Panel\Sites\store.example.com\logs\access.log`,
	})
	if !strings.Contains(out, "root C:/Panel/Sites/store.example.com/public;") {
		t.Errorf("windows path not normalized to forward slashes:\n%s", out)
	}
	if !strings.Contains(out, "fastcgi_pass 127.0.0.1:9001;") {
		t.Errorf("missing fastcgi_pass:\n%s", out)
	}
}

func TestGenerateStaticSite(t *testing.T) {
	out := Generate(SiteConfig{
		Domain:       "static.example.com",
		DocumentRoot: "/srv/panel/sites/static.example.com/public",
	})
	if strings.Contains(out, "fastcgi_pass") {
		t.Errorf("static site must not contain fastcgi_pass:\n%s", out)
	}
	if !strings.Contains(out, "try_files $uri $uri/ =404;") {
		t.Errorf("static site must use =404 fallback:\n%s", out)
	}
}

func TestGenerateSanitizesServerNames(t *testing.T) {
	// Defense in depth: even if validation upstream were bypassed, the
	// generator must never emit metacharacters into server_name.
	out := Generate(SiteConfig{
		Domain:       "example.com",
		Aliases:      []string{"bad;domain.example.com", "evil$(reboot).com"},
		DocumentRoot: "/srv/panel/sites/example.com/public",
	})
	if strings.Contains(out, "bad;domain") || strings.Contains(out, "evil$(reboot)") {
		t.Errorf("unsafe characters reached server_name:\n%s", out)
	}
}

func TestGenerateDotfilesBlocked(t *testing.T) {
	out := Generate(SiteConfig{
		Domain:       "example.com",
		DocumentRoot: "/srv/panel/sites/example.com/public",
	})
	if !strings.Contains(out, `location ~ /\.(?!well-known)`) {
		t.Errorf("dotfile deny rule missing:\n%s", out)
	}
}

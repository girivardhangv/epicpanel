package websites

import "testing"

func TestResolveSitePathsLinux(t *testing.T) {
	plan, err := ResolveSitePaths("/srv/panel/sites", "example.com", "linux")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []struct{ name, got, exp string }{
		{"root", plan.SiteRoot, "/srv/panel/sites/example.com"},
		{"public", plan.PublicDir, "/srv/panel/sites/example.com/public"},
		{"logs", plan.LogsDir, "/srv/panel/sites/example.com/logs"},
		{"access", plan.AccessLog, "/srv/panel/sites/example.com/logs/access.log"},
		{"error", plan.ErrorLog, "/srv/panel/sites/example.com/logs/error.log"},
	}
	for _, w := range want {
		if w.got != w.exp {
			t.Errorf("%s = %q, want %q", w.name, w.got, w.exp)
		}
	}
}

func TestResolveSitePathsWindows(t *testing.T) {
	plan, err := ResolveSitePaths(`C:\Panel\Sites`, "shop.example.com", "windows")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.PublicDir != `C:\Panel\Sites\shop.example.com\public` {
		t.Errorf("public = %q", plan.PublicDir)
	}
	if plan.AccessLog != `C:\Panel\Sites\shop.example.com\logs\access.log` {
		t.Errorf("access log = %q", plan.AccessLog)
	}
}

func TestResolveSitePathsRejectsEscapes(t *testing.T) {
	bad := []string{"../evil.com", "a/b.com", `a\b.com`, "..", "a..b.com"}
	for _, domain := range bad {
		if _, err := ResolveSitePaths("/srv/panel/sites", domain, "linux"); err == nil {
			t.Errorf("ResolveSitePaths(%q) = nil error, want rejection", domain)
		}
	}
	if _, err := ResolveSitePaths("relative/root", "example.com", "linux"); err == nil {
		t.Error("relative sites root must be rejected")
	}
	if _, err := ResolveSitePaths("", "example.com", "linux"); err == nil {
		t.Error("empty sites root must be rejected")
	}
}

func TestSlug(t *testing.T) {
	if Slug("Example.COM") != "example.com" {
		t.Errorf("Slug case handling: %q", Slug("Example.COM"))
	}
	if Slug("*.example.com") != "wildcard.example.com" {
		t.Errorf("Slug wildcard: %q", Slug("*.example.com"))
	}
}

func TestValidateDocumentRootOverride(t *testing.T) {
	root := "/srv/panel/sites"
	if got, err := ValidateDocumentRootOverride(root, "example.com", "/srv/panel/sites/example.com/public/app", "linux"); err != nil || got != "/srv/panel/sites/example.com/public/app" {
		t.Errorf("valid override rejected: %q %v", got, err)
	}
	if _, err := ValidateDocumentRootOverride(root, "example.com", "/srv/panel/sites/other.com/public", "linux"); err == nil {
		t.Error("override outside the site directory must be rejected")
	}
	if _, err := ValidateDocumentRootOverride(root, "example.com", "/etc/nginx", "linux"); err == nil {
		t.Error("override outside the sites root must be rejected")
	}
	if got, err := ValidateDocumentRootOverride(root, "example.com", "", "linux"); err != nil || got != "" {
		t.Errorf("empty override should pass through: %q %v", got, err)
	}
}

func TestLogsForSite(t *testing.T) {
	a, e, ok := LogsForSite("/srv/panel/sites/example.com/public")
	if !ok || a != "/srv/panel/sites/example.com/logs/access.log" || e != "/srv/panel/sites/example.com/logs/error.log" {
		t.Errorf("linux logs: ok=%v a=%q e=%q", ok, a, e)
	}
	a, e, ok = LogsForSite(`C:\Panel\Sites\example.com\public`)
	if !ok || a != `C:\Panel\Sites\example.com\logs\access.log` {
		t.Errorf("windows logs: ok=%v a=%q", ok, a)
	}
	if _, _, ok := LogsForSite("/opt/something"); ok {
		t.Error("non-site document root must not yield log paths")
	}
}

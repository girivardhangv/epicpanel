package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// ============================================================================
// ONE-CLICK LARAVEL (composer create-project, site's selected PHP version)
// ============================================================================

// LaravelPayload describes a one-click Laravel installation request.
type LaravelPayload struct {
	WebsiteID string `json:"website_id"`
	UnixUser  string `json:"unix_user"`
	// RuntimeVersion is the site's selected PHP minor (e.g. "8.3"); the
	// matching CLI binary runs composer so the app matches the FPM pool.
	RuntimeVersion string `json:"runtime_version"`
	SiteURL        string `json:"site_url"`
}

// LaravelOutcome reports the new serving docroot (<site>/app/public) so the
// control plane can re-point the website and reconcile the vhost.
type LaravelOutcome struct {
	Version      string `json:"version"`
	DocumentRoot string `json:"document_root"`
	Log          string `json:"log"`
}

const composerBin = "/usr/local/bin/composer"

var phpMinorRe = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// InstallLaravel scaffolds a fresh Laravel application into <site>/app via
// composer create-project, running under the SITE USER with the site's PHP
// version. Serving stays untouched until the control plane applies the
// outcome (docroot re-point + vhost reconcile) — a failed install never
// leaves a half-proxied site behind. Idempotent guard: refuses when the app
// tree already exists.
func (e *Executor) InstallLaravel(ctx context.Context, p LaravelPayload) (*LaravelOutcome, error) {
	if _, err := uuid.Parse(p.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	siteBase := filepath.Join(e.docRootBase, p.WebsiteID)
	appDir := filepath.Join(siteBase, "app")

	// Guard: never clobber an existing application tree (double-install or
	// user content).
	if entries, err := os.ReadDir(appDir); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("application directory already exists at %s", appDir)
	}

	minor := strings.TrimSpace(p.RuntimeVersion)
	if minor == "" {
		minor = latestInstalledPHPMinor(e)
	}
	if !phpMinorRe.MatchString(minor) {
		return nil, fmt.Errorf("invalid php runtime version %q", p.RuntimeVersion)
	}
	php := phpBinary(minor)
	if _, err := os.Stat(php); err != nil {
		return nil, fmt.Errorf("php %s is not installed on this server", minor)
	}

	var log strings.Builder

	// Composer + unzip (dist archives) — idempotent installs.
	if _, err := os.Stat(composerBin); os.IsNotExist(err) {
		log.WriteString("downloading composer...\n")
		if err := e.run(ctx, "curl", "-fsSL", "-o", composerBin, "https://getcomposer.org/composer-stable.phar"); err != nil {
			return nil, fmt.Errorf("download composer: %w", err)
		}
		_ = os.Chmod(composerBin, 0o755)
	}
	if _, err := exec.LookPath("unzip"); err != nil {
		log.WriteString("installing unzip...\n")
		if err := e.aptInstall(ctx, []string{"unzip", "zip"}, "", "unzip"); err != nil {
			return nil, fmt.Errorf("install unzip: %w", err)
		}
	}

	uid, gid, err := siteOwnerIDs(p.WebsiteID)
	if err != nil {
		return nil, fmt.Errorf("site owner: %w", err)
	}

	// create-project as the site user; composer state lives under the site's
	// tmp dir so nothing leaks into root's home.
	log.WriteString(fmt.Sprintf("composer create-project laravel/laravel (php %s)...\n", minor))
	if out, err := e.runAsSite(ctx, uid, gid, siteBase,
		"env",
		"COMPOSER_HOME="+filepath.Join(siteBase, "tmp", "composer"),
		"COMPOSER_PROCESS_TIMEOUT=600",
		php, composerBin,
		"create-project", "laravel/laravel", appDir,
		"--no-interaction", "--prefer-dist", "--no-progress",
	); err != nil {
		return nil, fmt.Errorf("composer create-project: %s (%w)", tailString(out, 800), err)
	}

	// Point the app at the site's public URL (create-project already ran
	// artisan key:generate via its post-create scripts).
	if siteURL := sanitizeEnvValue(p.SiteURL); siteURL != "" {
		envPath := filepath.Join(appDir, ".env")
		if data, err := os.ReadFile(envPath); err == nil {
			patched := regexp.MustCompile(`(?m)^APP_URL=.*$`).ReplaceAllString(string(data), "APP_URL="+siteURL)
			if err := os.WriteFile(envPath, []byte(patched), 0o640); err == nil {
				_ = os.Chown(envPath, uid, gid)
			}
		}
	}

	// Report the framework version for the job result (best-effort).
	version := ""
	if out, err := e.runAsSite(ctx, uid, gid, appDir, php, "artisan", "--version"); err == nil {
		if m := regexp.MustCompile(`\d+\.\d+\.\d+`).FindStringSubmatch(out); m != nil {
			version = m[0]
		}
	}

	// Serving layout: grant nginx traversal + read on the new docroot
	// (same ACL contract as the default public/ docroot).
	_ = chownRecursive(appDir, uid, gid)
	if err := grantAppDocroot(siteBase, "app"); err != nil {
		slog.Warn("laravel docroot ACL", "site", p.WebsiteID, "err", err)
	}

	newDocroot := filepath.Join(appDir, "public")
	log.WriteString("laravel installed; docroot " + newDocroot + "\n")
	slog.Info("laravel installed", "site", p.WebsiteID, "php", minor, "version", version)
	return &LaravelOutcome{Version: version, DocumentRoot: newDocroot, Log: log.String()}, nil
}

// grantAppDocroot mirrors grantWebServerAccess for an alternate docroot
// (<siteBase>/<rel>/public): traverse-only on the parents, r-x (+ default
// ACL) on the serving dir. Falls back to mode bits when setfacl is missing.
func grantAppDocroot(siteBase, rel string) error {
	appDir := filepath.Join(siteBase, rel)
	docRoot := filepath.Join(appDir, "public")
	if _, err := exec.LookPath("setfacl"); err == nil {
		ok := true
		for _, args := range [][]string{
			{"-m", "u:" + webServerUser + ":--x", siteBase},
			{"-m", "u:" + webServerUser + ":--x", appDir},
			{"-m", "u:" + webServerUser + ":r-x", docRoot},
			{"-d", "-m", "u:" + webServerUser + ":r-x", docRoot},
		} {
			if out, err := exec.Command("setfacl", args...).CombinedOutput(); err != nil {
				slog.Warn("setfacl failed, falling back to chmod", "args", args, "out", strings.TrimSpace(string(out)), "err", err)
				ok = false
				break
			}
		}
		if ok {
			return nil
		}
	}
	if err := os.Chmod(siteBase, 0o711); err != nil {
		return err
	}
	if err := os.Chmod(appDir, 0o711); err != nil {
		return err
	}
	return os.Chmod(docRoot, 0o755)
}

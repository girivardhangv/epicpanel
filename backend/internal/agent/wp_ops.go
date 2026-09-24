package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const defaultWPTheme = "twentytwentyfive"

// WPPayload describes a WordPress installation request.
type WPPayload struct {
	WebsiteID      string `json:"website_id"`
	UnixUser       string `json:"unix_user"`
	DocumentRoot   string `json:"document_root"`
	SiteURL        string `json:"site_url"`
	RuntimeVersion string `json:"runtime_version"`
	Title          string `json:"title"`
	AdminUser      string `json:"admin_user"`
	AdminEmail     string `json:"admin_email"`
	DBName         string `json:"db_name"`
	DBUser         string `json:"db_user"`
	DBPassword     string `json:"db_password"`
	// DBPasswordEnc carries the credential secretbox-encrypted (control plane
	// never stores it in plaintext in the jobs table).
	DBPasswordEnc string `json:"db_password_enc"`
	DBHost        string `json:"db_host"`
}

// resolveDBPassword prefers the encrypted form; a legacy plaintext field is
// honoured for old payloads.
func (p WPPayload) resolveDBPassword() (string, error) {
	if p.DBPasswordEnc != "" {
		return decodeDeployToken(p.DBPasswordEnc)
	}
	return p.DBPassword, nil
}

type WPOutcome struct {
	Version   string `json:"version"`
	SiteURL   string `json:"site_url"`
	AdminURL  string `json:"admin_url"`
	AdminUser string `json:"admin_user"`
	AdminPass string `json:"admin_password"`
}

// InstallWordPress deploys WordPress into the site's public dir via wp-cli:
// download core, create wp-config with the DB, run install. Idempotent guard:
// refuses when WordPress is already present.
func (e *Executor) InstallWordPress(ctx context.Context, p WPPayload) (*WPOutcome, error) {
	if _, err := uuid.Parse(p.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	if !safeIdentifier(p.DBName) || !safeIdentifier(p.DBUser) {
		return nil, fmt.Errorf("invalid database identifiers")
	}
	if strings.ContainsAny(p.AdminUser, "'\";") || strings.ContainsAny(p.AdminEmail, "'\";") {
		return nil, fmt.Errorf("invalid admin credentials")
	}
	docRoot := p.DocumentRoot
	if !strings.HasPrefix(docRoot, "/srv/epicpanel/websites/") {
		return nil, fmt.Errorf("document root outside epicpanel tree")
	}
	// Refuse double-install.
	if _, err := os.Stat(filepath.Join(docRoot, "wp-config.php")); err == nil {
		return nil, fmt.Errorf("wordpress already installed at this site")
	}
	if _, err := os.Stat(filepath.Join(docRoot, "wp-includes")); err == nil {
		return nil, fmt.Errorf("wordpress already installed at this site")
	}
	dbPassword, err := p.resolveDBPassword()
	if err != nil || dbPassword == "" {
		return nil, fmt.Errorf("database credential missing from payload")
	}

	// wp-cli binary (idempotent download).
	wpcli := "/usr/local/bin/wp"
	if _, err := os.Stat(wpcli); os.IsNotExist(err) {
		if err := e.run(ctx, "curl", "-fsSL", "-o", wpcli, "https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar"); err != nil {
			return nil, fmt.Errorf("download wp-cli: %w", err)
		}
		_ = os.Chmod(wpcli, 0o755)
	}

	uid, gid, err := siteOwnerIDs(p.WebsiteID)
	if err != nil {
		return nil, fmt.Errorf("site owner: %w", err)
	}

	// Run wp-cli under the SITE's selected PHP (the version the site serves
	// with), not the newest installed one — extensions and compatibility
	// differ per build. Fall back for legacy payloads without the field.
	phpMinor := p.RuntimeVersion
	if phpMinor == "" {
		phpMinor = latestInstalledPHPMinor(e)
	}
	wp := func(args ...string) (string, error) {
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(c, "sudo", "-u", "#"+fmt.Sprint(uid),
			phpBinary(phpMinor), wpcli, "--allow-root")
		cmd.Args = append(cmd.Args, args...)
		cmd.Args = append(cmd.Args, "--path="+docRoot)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	slog.Info("downloading wordpress core", "site", p.WebsiteID)
	if out, err := wp("core", "download", "--skip-content"); err != nil {
		return nil, fmt.Errorf("wp core download: %s (%w)", tailString(out, 400), err)
	}

	adminPass := randomToken(16)
	siteURL := p.SiteURL
	if siteURL == "" {
		siteURL = "http://localhost"
	}
	if _, err := wp("config", "create",
		"--dbname="+p.DBName,
		"--dbuser="+p.DBUser,
		"--dbpass="+dbPassword,
		"--dbhost="+p.DBHost,
		"--dbprefix=wp_",
		"--skip-check",
	); err != nil {
		return nil, fmt.Errorf("wp config create: %w", err)
	}

	if _, err := wp("core", "install",
		"--url="+siteURL,
		"--title="+sanitizeWPTitle(p.Title),
		"--admin_user="+p.AdminUser,
		"--admin_password="+adminPass,
		"--admin_email="+p.AdminEmail,
		"--skip-email",
	); err != nil {
		return nil, fmt.Errorf("wp core install: %w", err)
	}

	// core download --skip-content ships no theme; without one the front
	// page renders empty. Best-effort install of the bundled-era default.
	if out, err := wp("theme", "install", defaultWPTheme, "--activate"); err != nil {
		slog.Warn("wp default theme install failed", "site", p.WebsiteID,
			"out", tailString(out, 300), "err", err)
	}

	// Ownership + sane perms.
	_ = chownRecursive(docRoot, uid, gid)
	_ = os.Chmod(filepath.Join(docRoot, "wp-config.php"), 0o640)

	slog.Info("wordpress installed", "site", p.WebsiteID, "url", siteURL)
	return &WPOutcome{
		SiteURL:   siteURL,
		AdminURL:  strings.TrimSuffix(siteURL, "/") + "/wp-admin/",
		AdminUser: p.AdminUser,
		AdminPass: adminPass,
	}, nil
}

func sanitizeWPTitle(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "My WordPress Site"
	}
	return strings.Map(func(r rune) rune {
		if r == '\'' || r == '"' || r == ';' {
			return -1
		}
		return r
	}, t)
}

func randomToken(n int) string {
	b := make([]byte, n)
	const charset = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKMNPQRSTUVWXYZ23456789"
	for i := range b {
		b[i] = charset[int(rngByte())%len(charset)]
	}
	return string(b)
}

func rngByte() byte {
	var b [1]byte
	_, _ = cryptoRead(b[:])
	return b[0]
}

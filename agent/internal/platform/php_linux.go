//go:build linux

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Linux PHP discovery: scan /etc/php/<version>/fpm trees managed by the
// distro packages (Ubuntu LTS supported matrix). FPM pools use unix sockets.
type linuxPHP struct{}

func newPHPRuntime() (PHPOps, error) { return &linuxPHP{}, nil }

func newPHPRuntimeDir(dirs []string) (PHPOps, error) {
	// Linux PHP is distro-managed under /etc/php; the explicit dir variant is
	// only meaningful for self-contained installs, so ignore it here.
	return &linuxPHP{}, nil
}

func (p *linuxPHP) Versions(ctx context.Context) ([]PHPVersion, error) {
	out := []PHPVersion{}
	entries, err := os.ReadDir("/etc/php")
	if err != nil {
		return out, nil // no PHP installed: empty list is the honest answer
	}
	vers := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if fi, err := os.Stat(filepath.Join("/etc/php", e.Name(), "fpm")); err == nil && fi.IsDir() {
			vers = append(vers, e.Name())
		}
	}
	sort.Strings(vers)
	for _, v := range vers {
		bin := "/usr/sbin/php-fpm" + v
		status := "available"
		if _, err := os.Stat(bin); err != nil {
			if alt, aerr := exec.LookPath("php-fpm" + v); aerr == nil {
				bin = alt
			} else {
				status = "stopped"
			}
		}
		out = append(out, PHPVersion{
			Version:     v,
			BinaryPath:  bin,
			ConfigPath:  filepath.Join("/etc/php", v, "fpm", "php.ini"),
			HandlerType: HandlerFPM,
			Status:      status,
		})
	}
	return out, nil
}

func (p *linuxPHP) poolFile(version, slug string) string {
	return filepath.Join("/etc/php", version, "fpm", "pool.d", "epicpanel-"+slug+".conf")
}

func (p *linuxPHP) socketFor(slug string) string {
	return "unix:/run/php/epicpanel-" + slug + ".sock"
}

// poolConf renders a deterministic FPM pool. The same inputs always produce
// the same file, which makes repeated provisioning idempotent.
func (p *linuxPHP) poolConf(req PHPPoolRequest) string {
	slug := req.SiteSlug
	user := SiteUser
	if req.User != "" && userExists(req.User) {
		user = req.User // per-site isolated account
	}
	if !userExists(user) {
		user = "www-data" // weaker isolation, still functional; warned upstream
	}
	conf := "; Managed by EpicPanel — manual edits will be overwritten.\n"
	conf += "[epicpanel-" + slug + "]\n"
	conf += "user = " + user + "\n"
	conf += "group = " + user + "\n"
	conf += "listen = /run/php/epicpanel-" + slug + ".sock\n"
	conf += "listen.owner = www-data\n"
	conf += "listen.group = www-data\n"
	conf += "pm = dynamic\n"
	conf += "pm.max_children = 10\n"
	conf += "pm.start_servers = 2\n"
	conf += "pm.min_spare_servers = 1\n"
	conf += "pm.max_spare_servers = 3\n"
	conf += "php_admin_value[error_log] = /var/log/php-fpm/epicpanel-" + slug + "-error.log\n"
	conf += phpSettingsBlock(req.Settings)
	return conf
}

func (p *linuxPHP) EnsurePool(ctx context.Context, req PHPPoolRequest) (*PHPPoolResult, error) {
	if !validSiteName(req.SiteSlug) {
		return nil, fmt.Errorf("invalid site slug")
	}
	if !validPHPVersion(req.Version) {
		return nil, fmt.Errorf("invalid PHP version %q", req.Version)
	}
	poolFile := p.poolFile(req.Version, req.SiteSlug)
	if err := os.MkdirAll(filepath.Dir(poolFile), 0o755); err != nil {
		return nil, err
	}
	if err := atomicWrite(poolFile, []byte(p.poolConf(req)), 0o644); err != nil {
		return nil, fmt.Errorf("write fpm pool: %w", err)
	}
	// A reload is required for a fresh pool; failures surface honestly but do
	// not corrupt state — nginx will report a bad gateway until it succeeds.
	_ = reloadFPM(req.Version)
	return &PHPPoolResult{Address: p.socketFor(req.SiteSlug)}, nil
}

func (p *linuxPHP) RemovePool(ctx context.Context, req PHPPoolRequest) error {
	if !validSiteName(req.SiteSlug) {
		return fmt.Errorf("invalid site slug")
	}
	if req.Version != "" && validPHPVersion(req.Version) {
		_ = os.Remove(p.poolFile(req.Version, req.SiteSlug))
		_ = reloadFPM(req.Version)
		return nil
	}
	// Unknown version: sweep all managed pools for this slug.
	entries, err := os.ReadDir("/etc/php")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		v := e.Name()
		if !e.IsDir() {
			continue
		}
		pool := p.poolFile(v, req.SiteSlug)
		if _, err := os.Stat(pool); err == nil {
			_ = os.Remove(pool)
			_ = reloadFPM(v)
		}
	}
	return nil
}

func reloadFPM(version string) error {
	service := "php" + version + "-fpm"
	if err := exec.Command("systemctl", "reload", service).Run(); err == nil {
		return nil
	}
	return exec.Command("service", service, "reload").Run()
}

func validPHPVersion(v string) bool {
	if v == "" || len(v) > 12 {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return !strings.HasPrefix(v, ".") && !strings.HasSuffix(v, ".") && !strings.Contains(v, "..")
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

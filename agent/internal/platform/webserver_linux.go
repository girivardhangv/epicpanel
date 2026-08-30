//go:build linux

package platform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Linux nginx layout: prefer the Debian/Ubuntu sites-available/sites-enabled
// pair (the supported matrix), fall back to the stock conf.d include.
type linuxWebServer struct {
	bin          string
	confDir      string // /etc/nginx
	style        string // sites-enabled | conf.d
}

const (
	sitesAvailable = "sites-available"
	sitesEnabled   = "sites-enabled"
	confD          = "conf.d"
)

func newWebServer() (WebServerOps, error) {
	bin, err := exec.LookPath("nginx")
	if err != nil {
		return (*linuxWebServer)(nil), nil
	}
	confDir := "/etc/nginx"
	style := "conf.d"
	if fi, err := os.Stat(filepath.Join(confDir, sitesEnabled)); err == nil && fi.IsDir() {
		style = "sites-enabled"
	}
	return &linuxWebServer{bin: bin, confDir: confDir, style: style}, nil
}

func newWebServerDir(dir string) (WebServerOps, error) {
	// Linux: self-contained nginx may be at <dir>/sbin/nginx; fallback to
	// system detection if not found.
	bin := filepath.Join(dir, "sbin", "nginx")
	if _, err := os.Stat(bin); err != nil {
		return newWebServer()
	}
	confDir := filepath.Join(dir, "conf")
	style := "conf.d"
	if fi, err := os.Stat(filepath.Join(confDir, sitesEnabled)); err == nil && fi.IsDir() {
		style = "sites-enabled"
	}
	return &linuxWebServer{bin: bin, confDir: confDir, style: style}, nil
}

func (n *linuxWebServer) Status(ctx context.Context) (*NginxStatus, error) {
	st := &NginxStatus{Installed: n != nil, Style: n.style, ConfigPath: "/etc/nginx/nginx.conf"}
	if n == nil {
		return st, nil
	}
	out, err := exec.Command(n.bin, "-v").CombinedOutput()
	if v, ok := parseNginxVersion(string(out)); ok {
		st.Version = v
	}
	if err != nil && st.Version == "" {
		// -v exits 0 normally; a hard failure here means broken install.
		st.Installed = true // binary exists; keep installed=true, version empty
	}
	st.Running = nginxRunning()
	return st, nil
}

func nginxRunning() bool {
	data, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return false
	}
	// Heuristic: nginx default listens on :80 (0x0050) or :443 (0x01BB).
	return strings.Contains(string(data), ":0050 ") || strings.Contains(string(data), ":01BB ")
}

func (n *linuxWebServer) availablePath(name string) string {
	if n.style == "sites-enabled" {
		return filepath.Join(n.confDir, sitesAvailable, name+".conf")
	}
	return filepath.Join(n.confDir, confD, name+".conf")
}

func (n *linuxWebServer) enabledPath(name string) string {
	if n.style == "sites-enabled" {
		return filepath.Join(n.confDir, sitesEnabled, name+".conf")
	}
	return n.availablePath(name)
}

// validate runs nginx -t against the live tree; the caller guarantees the
// candidate file is already in place (with the old file backed up).
func (n *linuxWebServer) validate() (string, error) {
	cmd := exec.Command(n.bin, "-t")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// DeploySite writes the candidate, validates the whole configuration and
// rolls back to the previous file when validation fails. Existing sites are
// never lost to a bad deploy.
func (n *linuxWebServer) DeploySite(ctx context.Context, site NginxSite) (*NginxDeployResult, error) {
	res := &NginxDeployResult{}
	if n == nil {
		return res, errors.New("nginx is not installed on this server")
	}
	if !validSiteName(site.Name) {
		return res, errors.New("invalid site name")
	}

	if n.style == "sites-enabled" {
		_ = os.MkdirAll(filepath.Join(n.confDir, sitesAvailable), 0o755)
		_ = os.MkdirAll(filepath.Join(n.confDir, sitesEnabled), 0o755)
	} else {
		_ = os.MkdirAll(filepath.Join(n.confDir, confD), 0o755)
	}

	avail := n.availablePath(site.Name)
	var backup []byte
	hadBackup := false
	if old, err := os.ReadFile(avail); err == nil {
		backup = old
		hadBackup = true
	}

	write := func() error {
		tmp := avail + ".tmp"
		if err := os.WriteFile(tmp, []byte(site.Content), 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, avail)
	}
	restore := func() {
		if hadBackup {
			_ = os.WriteFile(avail, backup, 0o644)
		} else {
			_ = os.Remove(avail)
		}
		if n.style == "sites-enabled" {
			_ = os.Remove(n.enabledPath(site.Name))
		}
	}

	if err := write(); err != nil {
		return res, fmt.Errorf("write site config: %w", err)
	}

	if site.Enable {
		if n.style == "sites-enabled" {
			link := n.enabledPath(site.Name)
			_ = os.Remove(link)
			if err := os.Symlink(filepath.Join("..", sitesAvailable, site.Name+".conf"), link); err != nil {
				restore()
				return res, fmt.Errorf("enable site: %w", err)
			}
		}
		// conf.d style: the file itself is live once written.
	}

	out, verr := n.validate()
	res.ValidationOut = strings.TrimSpace(out)
	if verr != nil {
		restore()
		res.RolledBack = true
		res.ValidationOut = strings.TrimSpace(out)
		return res, nil
	}

	res.Deployed = true
	res.Validated = true
	return res, nil
}

func (n *linuxWebServer) RemoveSite(ctx context.Context, name string) error {
	if n == nil {
		return errors.New("nginx is not installed on this server")
	}
	if !validSiteName(name) {
		return errors.New("invalid site name")
	}
	if n.style == "sites-enabled" {
		_ = os.Remove(n.enabledPath(name))
	}
	return os.Remove(n.availablePath(name))
}

func (n *linuxWebServer) SetEnabled(ctx context.Context, name string, enable bool) error {
	if n == nil {
		return errors.New("nginx is not installed on this server")
	}
	if !validSiteName(name) {
		return errors.New("invalid site name")
	}
	if n.style == "sites-enabled" {
		link := n.enabledPath(name)
		if enable {
			_ = os.Remove(link)
			return os.Symlink(filepath.Join("..", sitesAvailable, name+".conf"), link)
		}
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	// conf.d style: enabled = .conf present; disabled = renamed aside.
	avail := n.availablePath(name)
	if enable {
		_, err := os.Stat(avail + ".disabled")
		if err == nil {
			return os.Rename(avail+".disabled", avail)
		}
		if os.IsNotExist(err) {
			if _, statErr := os.Stat(avail); statErr == nil {
				return nil // already enabled
			}
			return errors.New("site configuration does not exist")
		}
		return err
	}
	if _, err := os.Stat(avail); err == nil {
		return os.Rename(avail, avail+".disabled")
	}
	return nil
}

func (n *linuxWebServer) Reload(ctx context.Context) error {
	if n == nil {
		return errors.New("nginx is not installed on this server")
	}
	if err := exec.Command("systemctl", "reload", "nginx").Run(); err == nil {
		return nil
	}
	if err := exec.Command("service", "nginx", "reload").Run(); err == nil {
		return nil
	}
	out, err := exec.Command(n.bin, "-s", "reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("nginx reload failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// chownSiteTree hands the site directory to the site user (best effort).
func chownSiteTree(root, user string) error {
	return chownTree(root, user)
}

// chownTree recursively hands ownership of root to the user (Linux).
func chownTree(root, user string) error {
	u, err := lookupUser(user)
	if err != nil {
		return err
	}
	return filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return syscall.Chown(path, u.uid, u.gid)
	})
}

type simpleUser struct {
	uid, gid int
}

func lookupUser(name string) (*simpleUser, error) {
	out, err := exec.Command("getent", "passwd", name).Output()
	if err != nil {
		return nil, fmt.Errorf("user %s not found", name)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(fields) < 4 {
		return nil, fmt.Errorf("malformed passwd entry for %s", name)
	}
	var uid, gid int
	if _, err := fmt.Sscanf(fields[2], "%d", &uid); err != nil {
		return nil, err
	}
	if _, err := fmt.Sscanf(fields[3], "%d", &gid); err != nil {
		return nil, err
	}
	return &simpleUser{uid: uid, gid: gid}, nil
}

func userExists(name string) bool {
	_, err := lookupUser(name)
	return err == nil
}

// ensureSiteUser creates the shared site account once; idempotent.
func ensureSiteUser(name string) error {
	if userExists(name) {
		return nil
	}
	cmd := exec.Command("useradd",
		"--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--user-group", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Some distros lack /usr/sbin/nologin; retry with /bin/false.
		cmd = exec.Command("useradd", "--system", "--no-create-home", "--shell", "/bin/false", "--user-group", name)
		if out2, err2 := cmd.CombinedOutput(); err2 != nil {
			return fmt.Errorf("useradd failed: %s", strings.TrimSpace(string(out))+" / "+strings.TrimSpace(string(out2)))
		}
	}
	return nil
}

// EnsureSiteUserImpl is the exported hook the ops layer calls.
func EnsureSiteUserImpl(name string) error { return ensureSiteUser(name) }

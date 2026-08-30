//go:build windows

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
)

// Windows nginx layout: the agent manages <nginxDir>\conf\sites\*.conf and
// ensures nginx.conf includes that directory (Windows nginx ships without a
// sites include). Enabled/disabled is expressed by the .conf extension.
type windowsWebServer struct {
	nginxDir string
}

func newWebServer() (WebServerOps, error) {
	dir := os.Getenv("EPICPANEL_NGINX_DIR")
	if dir == "" {
		dir = `C:\nginx`
	}
	bin := filepath.Join(dir, "nginx.exe")
	if _, err := os.Stat(bin); err != nil {
		// Not installed at the conventional location: typed nil, methods are
		// nil-receiver safe.
		return (*windowsWebServer)(nil), nil
	}
	return &windowsWebServer{nginxDir: dir}, nil
}

func (w *windowsWebServer) exe() string { return filepath.Join(w.nginxDir, "nginx.exe") }

func (w *windowsWebServer) Status(ctx context.Context) (*NginxStatus, error) {
	if w == nil {
		return &NginxStatus{Installed: false, Style: "custom"}, nil
	}
	st := &NginxStatus{Installed: true, Style: "custom",
		ConfigPath: filepath.Join(w.nginxDir, "conf", "nginx.conf")}
	cmd := exec.Command(w.exe(), "-v")
	cmd.Dir = w.nginxDir
	out, err := cmd.CombinedOutput()
	if v, ok := parseNginxVersion(string(out)); ok {
		st.Version = v
	} else if err != nil {
		return nil, fmt.Errorf("nginx -v failed: %s", strings.TrimSpace(string(out)))
	}
	st.Running = w.running()
	return st, nil
}

func (w *windowsWebServer) running() bool {
	// nginx on Windows keeps a pid file at logs/nginx.pid while running.
	pidFile := filepath.Join(w.nginxDir, "logs", "nginx.pid")
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &pid); err != nil {
		return false
	}
	_, err = os.FindProcess(pid)
	return err == nil
}

func (w *windowsWebServer) sitesDir() string {
	return filepath.Join(w.nginxDir, "conf", "sites")
}

func (w *windowsWebServer) sitePath(name string) string {
	return filepath.Join(w.sitesDir(), name+".conf")
}

func (w *windowsWebServer) validate() (string, error) {
	cmd := exec.Command(w.exe(), "-t", "-p", w.nginxDir)
	cmd.Dir = w.nginxDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// ensureInclude guarantees nginx.conf pulls in sites/*.conf inside the http
// block. It is conservative: no edit happens when the include already exists,
// and a missing http block is an error rather than a guess.
func (w *windowsWebServer) ensureInclude() error {
	conf := filepath.Join(w.nginxDir, "conf", "nginx.conf")
	raw, err := os.ReadFile(conf)
	if err != nil {
		return fmt.Errorf("read nginx.conf: %w", err)
	}
	content := string(raw)
	if strings.Contains(content, "sites/*.conf") || strings.Contains(content, `sites\*.conf`) {
		return nil
	}
	idx := strings.Index(content, "http {")
	if idx < 0 {
		idx = strings.Index(content, "http{")
	}
	if idx < 0 {
		return errors.New("cannot find http block in nginx.conf; add 'include sites/*.conf;' manually")
	}
	insertAt := idx + len("http {")
	next := content[:insertAt] + "\n    include sites/*.conf;\n" + content[insertAt:]
	tmp := conf + ".tmp"
	if err := os.WriteFile(tmp, []byte(next), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, conf)
}

func (w *windowsWebServer) DeploySite(ctx context.Context, site NginxSite) (*NginxDeployResult, error) {
	res := &NginxDeployResult{}
	if w == nil {
		return res, errors.New("nginx is not installed on this server")
	}
	if !validSiteName(site.Name) {
		return res, errors.New("invalid site name")
	}
	if err := os.MkdirAll(w.sitesDir(), 0o755); err != nil {
		return res, err
	}
	if err := w.ensureInclude(); err != nil {
		return res, err
	}

	avail := w.sitePath(site.Name)
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
	}

	if !site.Enable {
		// Disabled sites live beside the enabled ones with a different suffix.
		if err := os.WriteFile(avail+".disabled", []byte(site.Content), 0o644); err != nil {
			return res, err
		}
		res.Deployed = true
		res.Validated = true
		return res, nil
	}

	if err := write(); err != nil {
		return res, fmt.Errorf("write site config: %w", err)
	}
	out, verr := w.validate()
	res.ValidationOut = strings.TrimSpace(out)
	if verr != nil {
		restore()
		res.RolledBack = true
		return res, nil
	}
	res.Deployed = true
	res.Validated = true
	return res, nil
}

func (w *windowsWebServer) RemoveSite(ctx context.Context, name string) error {
	if w == nil {
		return errors.New("nginx is not installed on this server")
	}
	if !validSiteName(name) {
		return errors.New("invalid site name")
	}
	_ = os.Remove(w.sitePath(name))
	_ = os.Remove(w.sitePath(name) + ".disabled")
	return nil
}

func (w *windowsWebServer) SetEnabled(ctx context.Context, name string, enable bool) error {
	if w == nil {
		return errors.New("nginx is not installed on this server")
	}
	if !validSiteName(name) {
		return errors.New("invalid site name")
	}
	avail := w.sitePath(name)
	if enable {
		if _, err := os.Stat(avail); err == nil {
			return nil
		}
		disabled := avail + ".disabled"
		if _, err := os.Stat(disabled); err != nil {
			return errors.New("site configuration does not exist")
		}
		return os.Rename(disabled, avail)
	}
	if _, err := os.Stat(avail); err != nil {
		return nil // already disabled / absent
	}
	return os.Rename(avail, avail+".disabled")
}

func (w *windowsWebServer) Reload(ctx context.Context) error {
	if w == nil {
		return errors.New("nginx is not installed on this server")
	}
	cmd := exec.Command(w.exe(), "-s", "reload", "-p", w.nginxDir)
	cmd.Dir = w.nginxDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nginx reload failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Site user management is a POSIX concept; on Windows the site directories
// rely on inherited ACLs (documented limitation).
func chownSiteTree(root, user string) error  { return errUnsupported }
func ensureSiteUser(name string) error       { return errUnsupported }
func userExists(name string) bool            { return false }

// EnsureSiteUserImpl is the exported hook the ops layer calls.
func EnsureSiteUserImpl(name string) error { return errUnsupported }

var errUnsupported = errors.New("operation not supported on windows")

//go:build windows

package platform

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Windows PHP discovery: php-cgi.exe trees under the configured roots, driven
// over TCP loopback FastCGI. The agent owns the php-cgi child processes.
type windowsPHP struct {
	mu       sync.Mutex
	procs    map[string]*exec.Cmd // "slug|version" -> running php-cgi
	phpDirs  []string
}

func newPHPRuntime() (PHPOps, error) {
	dirs := []string{}
	for _, env := range []string{"EPICPANEL_PHP_DIRS"} {
		if v := os.Getenv(env); v != "" {
			for _, d := range strings.Split(v, ";") {
				if d = strings.TrimSpace(d); d != "" {
					dirs = append(dirs, d)
				}
			}
		}
	}
	if len(dirs) == 0 {
		dirs = []string{`C:\PHP`, `C:\Program Files\PHP`}
	}
	return &windowsPHP{procs: map[string]*exec.Cmd{}, phpDirs: dirs}, nil
}

func newPHPRuntimeDir(dirs []string) (PHPOps, error) {
	if len(dirs) == 0 {
		return newPHPRuntime()
	}
	return &windowsPHP{procs: map[string]*exec.Cmd{}, phpDirs: dirs}, nil
}

// Versions probes the configured roots. Version numbers come from the
// directory name when it parses, otherwise from `php-cgi -v`.
func (p *windowsPHP) Versions(ctx context.Context) ([]PHPVersion, error) {
	out := []PHPVersion{}
	for _, root := range p.phpDirs {
		candidates := []string{root}
		if entries, err := os.ReadDir(root); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					candidates = append(candidates, filepath.Join(root, e.Name()))
				}
			}
		}
		for _, dir := range candidates {
			bin := filepath.Join(dir, "php-cgi.exe")
			if _, err := os.Stat(bin); err != nil {
				continue
			}
			v, ok := phpCGIVersion(bin)
			if !ok {
				continue
			}
			out = append(out, PHPVersion{
				Version:     v,
				BinaryPath:  bin,
				ConfigPath:  filepath.Join(dir, "php.ini"),
				HandlerType: HandlerFastCGI,
				Status:      "available",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func phpCGIVersion(bin string) (string, bool) {
	out, err := exec.Command(bin, "-v").CombinedOutput()
	if err != nil {
		return "", false
	}
	first := strings.SplitN(string(out), "\n", 2)[0]
	if i := strings.Index(first, "PHP "); i >= 0 {
		rest := strings.TrimSpace(first[i+len("PHP "):])
		if parts := strings.SplitN(rest, " ", 2); len(parts) > 0 {
			v := parts[0]
			if strings.Count(v, ".") >= 1 {
				return v, true
			}
		}
	}
	return "", false
}

// portFor derives a deterministic FastCGI port from the slug in the private
// loopback range; a live listener on that port is reused (idempotent restarts).
func (p *windowsPHP) portFor(slug string) (int, error) {
	base := 9100
	h := 0
	for _, r := range slug {
		h = (h*31 + int(r)) % 700
	}
	for attempt := 0; attempt < 8; attempt++ {
		port := base + ((h + attempt*37) % 700)
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		conn, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return port, nil // already served; reuse
		}
		// Verify we could bind something nearby; the chosen port must be free.
		l, err := net.Listen("tcp", addr)
		if err == nil {
			_ = l.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free FastCGI port found for %s", slug)
}

func (p *windowsPHP) EnsurePool(ctx context.Context, req PHPPoolRequest) (*PHPPoolResult, error) {
	if !validSiteName(req.SiteSlug) {
		return nil, fmt.Errorf("invalid site slug")
	}
	bin, err := p.binaryFor(ctx, req.Version)
	if err != nil {
		return nil, err
	}
	port, err := p.portFor(req.SiteSlug)
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	key := req.SiteSlug + "|" + req.Version
	p.mu.Lock()
	_, running := p.procs[key]
	p.mu.Unlock()
	if !running {
		cmd := exec.Command(bin, "-b", addr)
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start php-cgi: %w", err)
		}
		p.mu.Lock()
		p.procs[key] = cmd
		p.mu.Unlock()
	}
	return &PHPPoolResult{Address: addr}, nil
}

func (p *windowsPHP) binaryFor(ctx context.Context, version string) (string, error) {
	vers, err := p.Versions(ctx)
	if err != nil {
		return "", err
	}
	for _, v := range vers {
		if v.Version == version || strings.HasPrefix(v.Version, version) {
			return v.BinaryPath, nil
		}
	}
	return "", fmt.Errorf("PHP %s is not installed on this server", version)
}

func (p *windowsPHP) RemovePool(ctx context.Context, req PHPPoolRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, cmd := range p.procs {
		if req.SiteSlug == "" || strings.HasPrefix(key, req.SiteSlug+"|") {
			if cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			delete(p.procs, key)
		}
	}
	return nil
}

// validSiteName / validPHPVersion come from the shared + linux files; the
// windows build reuses them via the shared webserver.go and a local copy of
// the version validator (kept here to avoid cross-file build-tag coupling).
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

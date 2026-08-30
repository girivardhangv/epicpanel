// epicpanel CLI — administrator terminal for the same engine the web UI uses.
// Software operations call the shared internal/software manager directly (no
// duplicated install logic); status/doctor inspect the panel + host.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/agent/internal/software"
)

// version is overridden at release build time via
// -ldflags "-X main.version=vX.Y.Z". Defaults to dev.
var version = "dev"

const (
	versionCheckURL = "https://api.github.com/repos/%s/releases/latest"
	downloadBaseURL = "https://github.com/%s/releases/download/%s"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	switch args[0] {
	case "status":
		cmdStatus(ctx)
	case "doctor":
		cmdDoctor(ctx)
	case "update":
		cmdUpdate(ctx, args[1:])
	case "software":
		if len(args) < 2 {
			fmt.Println("usage: epicpanel software <list|install|remove|service> [name] [action]")
			os.Exit(2)
		}
		cmdSoftware(ctx, args[1], args[2:])
	case "version", "--version":
		fmt.Println("epicpanel", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`epicpanel - EpicPanel administrator CLI

Usage:
  epicpanel status                       Panel + service status
  epicpanel doctor                       Full system diagnostics
  epicpanel update                       Check for and apply the latest release
  epicpanel update --check               Only report whether an update exists
  epicpanel update --version vX.Y.Z      Install a specific release tag
  epicpanel software list                Detected software
  epicpanel software install <name>      Install a component
  epicpanel software remove <name>       Remove a component
  epicpanel software service <name> <action>   start|stop|restart|enable|disable
  epicpanel version`)
}

func cmdSoftware(ctx context.Context, sub string, rest []string) {
	mgr := software.NewManagerDir(nil, software.DefaultSoftwareDir())
	switch sub {
	case "list":
		comps := mgr.List(ctx)
		fmt.Printf("%-12s %-14s %-10s %-8s %-12s %s\n", "NAME", "CATEGORY", "STATUS", "SOURCE", "SERVICE", "VERSION")
		for _, c := range comps {
			status := "absent"
			if c.Installed {
				status = "installed"
			}
			src := c.Source
			if src == "" {
				src = "-"
			}
			svc := "-"
			if c.Service != "" {
				svc = map[bool]string{true: "running", false: "stopped"}[c.Running]
			}
			fmt.Printf("%-12s %-14s %-10s %-8s %-12s %s\n", c.Name, c.Category, status, src, svc, c.Version)
		}
	case "install":
		if len(rest) < 1 {
			fail("usage: epicpanel software install <name>")
		}
		fmt.Printf("Installing %s...\n", rest[0])
		res, err := mgr.Install(ctx, rest[0])
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("Done (exit %d).\n", res.ExitCode)
	case "remove":
		if len(rest) < 1 {
			fail("usage: epicpanel software remove <name>")
		}
		res, err := mgr.Remove(ctx, rest[0])
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("Done (exit %d).\n", res.ExitCode)
	case "service":
		if len(rest) < 2 {
			fail("usage: epicpanel software service <name> <action>")
		}
		res, err := mgr.Service(ctx, rest[0], rest[1])
		if err != nil {
			fail(err.Error())
		}
		fmt.Printf("exit %d\n", res.ExitCode)
	default:
		fail("unknown software subcommand: " + sub)
	}
}

// ---------------------------------------------------------------------------
// Self-update (epicpanel update)
// ---------------------------------------------------------------------------

// updateTarget is a binary we can install or replace.
type updateTarget struct {
	asset string // release asset name, e.g. epicpanel-panel_linux_amd64
	path  string // destination on disk
	label string // human name for messages
}

// updateTargets returns the set of binaries that make up an installation,
// resolved for the current OS/arch.
func updateTargets() []updateTarget {
	osName := runtime.GOOS
	arch := runtime.GOARCH
	ext := ""
	if osName == "windows" {
		ext = ".exe"
	}
	osArch := osName + "_" + arch + ext
	// Release assets follow <name>_<os>_<arch>[.exe].
	return []updateTarget{
		{asset: "epicpanel_" + osArch, path: panelBinaryPath(), label: "panel"},
		{asset: "epicpanel-agentd_" + osArch, path: agentdBinaryPath(), label: "agent"},
		{asset: "epicpanel-cli_" + osArch, path: cliSelfPath(), label: "CLI"},
	}
}

func repoSlug() string {
	if v := os.Getenv("EPICPANEL_REPO"); v != "" {
		return v
	}
	return "girivardhangv/epicpanel"
}

// latestRelease queries the GitHub API for the newest release tag.
func latestRelease() (string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(fmt.Sprintf(versionCheckURL, repoSlug()))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned HTTP %d (rate limited? set EPICPANEL_REPO)", resp.StatusCode)
	}
	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	return strings.TrimPrefix(payload.TagName, "v"), nil
}

func cmdUpdate(ctx context.Context, args []string) {
	checkOnly := false
	wantVersion := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--check":
			checkOnly = true
		case strings.HasPrefix(a, "--version="):
			wantVersion = strings.TrimPrefix(a, "--version=")
		case a == "--version" && i+1 < len(args):
			i++
			wantVersion = args[i]
		case strings.HasPrefix(a, "-"):
			fail("unknown update flag: " + a)
		default:
			wantVersion = a
		}
	}

	if wantVersion == "" {
		latest, err := latestRelease()
		if err != nil {
			fail(fmt.Sprintf("could not check for updates: %v", err))
		}
		wantVersion = latest
	}

	fmt.Printf("Current version: %s\n", version)
	fmt.Printf("Target version:  v%s\n", wantVersion)
	if strings.TrimPrefix(version, "v") == wantVersion {
		fmt.Println("Already up to date.")
		return
	}
	if checkOnly {
		fmt.Println("An update is available (run `epicpanel update` to apply).")
		return
	}

	// Download + verify each binary, then swap them into place.
	targets := updateTargets()
	staged := map[string]string{} // asset -> temp path
	for _, t := range targets {
		if t.asset == "" || t.path == "" {
			continue
		}
		fmt.Printf("  downloading %s...\n", t.asset)
		tmp, err := downloadReleaseAsset(wantVersion, t.asset)
		if err != nil {
			fail(fmt.Sprintf("download %s: %v", t.asset, err))
		}
		staged[t.asset] = tmp
	}

	for _, t := range targets {
		if t.asset == "" || t.path == "" {
			continue
		}
		if err := installStaged(staged[t.asset], t.path); err != nil {
			fail(fmt.Sprintf("install %s: %v", t.label, err))
		}
		fmt.Printf("  updated %s -> %s\n", t.label, t.path)
	}

	// Restart the panel service so the new binary takes effect.
	if runtime.GOOS != "windows" {
		if _, err := software.Run(ctx, "systemctl", "restart", "epicpanel"); err != nil {
			fmt.Println("  [warn] could not restart epicpanel service (restart manually):", err)
		} else {
			fmt.Println("  panel service restarted")
		}
	} else {
		fmt.Println("  note: restart the panel + agent processes to apply the update")
	}
	fmt.Println("Update complete.")
}

// downloadReleaseAsset fetches an asset and verifies its checksum against
// checksums.txt published with the release.
func downloadReleaseAsset(version, asset string) (string, error) {
	base := fmt.Sprintf(downloadBaseURL, repoSlug(), "v"+version)
	tmp, err := os.CreateTemp("", "epicpanel-update-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(base + "/" + asset)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d for %s", resp.StatusCode, asset)
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}

	// Best-effort checksum verification.
	sumResp, err := client.Get(base + "/checksums.txt")
	if err == nil {
		defer sumResp.Body.Close()
		if sumResp.StatusCode == http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(sumResp.Body, 1<<20))
			want := checksumFor(string(body), asset)
			if want != "" {
				got := hex.EncodeToString(h.Sum(nil))
				if !strings.EqualFold(got, want) {
					return "", fmt.Errorf("checksum mismatch for %s", asset)
				}
			}
		}
	}
	return tmp.Name(), nil
}

func checksumFor(sumfile, asset string) string {
	for _, line := range strings.Split(sumfile, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0]
		}
	}
	return ""
}

// installStaged moves a verified temp file into place, preserving permissions.
func installStaged(tmp, dest string) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Windows cannot overwrite a running exe; write next to it and report.
	if runtime.GOOS == "windows" {
		_ = os.Chmod(tmp, 0o755)
		return os.Rename(tmp, dest)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	return nil
}

// Binary locations are resolved so updates work for both installer-managed
// installs and manual layouts.

func panelBinaryPath() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\EpicPanel\epicpanel-panel.exe`
	}
	if v := os.Getenv("EPICPANEL_BIN"); v != "" {
		return v
	}
	return "/usr/local/bin/epicpanel-panel"
}

func agentdBinaryPath() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\EpicPanel\epicpanel-agentd.exe`
	}
	if v := os.Getenv("EPICPANEL_AGENTD"); v != "" {
		return v
	}
	return "/usr/local/bin/epicpanel-agentd"
}

func cliSelfPath() string {
	exe, err := os.Executable()
	if err == nil {
		return exe
	}
	if runtime.GOOS == "windows" {
		return `C:\Program Files\EpicPanel\epicpanel.exe`
	}
	return "/usr/local/bin/epicpanel"
}

func cmdStatus(ctx context.Context) {
	fmt.Println("EpicPanel status")
	fmt.Println("  " + checkLine(serviceActive(ctx, "epicpanel"), "panel service (epicpanel)"))
	fmt.Println("  " + checkLine(panelHealthy(), "panel HTTP health (/healthz)"))
}

func cmdDoctor(ctx context.Context) {
	mgr := software.NewManagerDir(nil, software.DefaultSoftwareDir())
	osi := mgr.OS()
	fmt.Println("EpicPanel")
	fmt.Println("  " + checkLine(true, "CLI installed (v"+version+")"))
	fmt.Println("  " + checkLine(serviceActive(ctx, "epicpanel"), "panel service running  (fix: systemctl start epicpanel)"))
	fmt.Println("  " + checkLine(panelHealthy(), "panel reachable on :8080  (fix: check EPICPANEL_SERVER_ADDR / firewall)"))
	fmt.Println("  " + checkLine(os.Geteuid() == 0 || runtime.GOOS == "windows", "running with privileges  (fix: run as root/admin)"))

	fmt.Println("\nServer")
	fmt.Println("  " + checkLine(true, fmt.Sprintf("OS: %s (%s), arch %s, pkg %s", osi.Distro, osi.Family, osi.Arch, osi.PackageManager)))
	fmt.Println("  " + checkLine(osi.PackageManager != "", "supported package manager  (fix: use Debian/Ubuntu/RHEL/Rocky/Alma)"))

	fmt.Println("\nSoftware")
	for _, c := range mgr.List(ctx) {
		if c.Installed {
			state := "stopped"
			if c.Running {
				state = "running"
			}
			if c.Service == "" {
				state = "installed"
			}
			fmt.Println("  " + checkLine(true, fmt.Sprintf("%s %s (%s)", c.DisplayName, c.Version, state)))
		} else {
			fmt.Println("  " + checkLine(false, fmt.Sprintf("%s not installed  (fix: epicpanel software install %s)", c.DisplayName, c.Name)))
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func serviceActive(ctx context.Context, name string) bool {
	res, err := software.Run(ctx, "systemctl", "is-active", name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(res.Stdout) == "active"
}

func panelHealthy() bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:8080/healthz")
	if err != nil {
		return false
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

func checkLine(ok bool, msg string) string {
	if ok {
		return "[OK] " + msg
	}
	return "[!!] " + msg
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	os.Exit(1)
}

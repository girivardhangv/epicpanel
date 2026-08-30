// epicpanel CLI — administrator terminal for the same engine the web UI uses.
// Software operations call the shared internal/software manager directly (no
// duplicated install logic); status/doctor inspect the panel + host.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/agent/internal/software"
)

const version = "0.1.0"

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
  epicpanel software list                Detected software
  epicpanel software install <name>      Install a component
  epicpanel software remove <name>       Remove a component
  epicpanel software service <name> <action>   start|stop|restart|enable|disable
  epicpanel version`)
}

func cmdSoftware(ctx context.Context, sub string, rest []string) {
	mgr := software.NewManager(nil)
	switch sub {
	case "list":
		comps := mgr.List(ctx)
		fmt.Printf("%-12s %-14s %-10s %-10s %s\n", "NAME", "CATEGORY", "STATUS", "SERVICE", "VERSION")
		for _, c := range comps {
			status := "absent"
			if c.Installed {
				status = "installed"
			}
			svc := "-"
			if c.Service != "" {
				svc = map[bool]string{true: "running", false: "stopped"}[c.Running]
			}
			fmt.Printf("%-12s %-14s %-10s %-10s %s\n", c.Name, c.Category, status, svc, c.Version)
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

func cmdStatus(ctx context.Context) {
	fmt.Println("EpicPanel status")
	fmt.Println("  " + checkLine(serviceActive(ctx, "epicpanel"), "panel service (epicpanel)"))
	fmt.Println("  " + checkLine(panelHealthy(), "panel HTTP health (/healthz)"))
}

func cmdDoctor(ctx context.Context) {
	mgr := software.NewManager(nil)
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

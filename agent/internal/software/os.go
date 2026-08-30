// Package software is EpicPanel's software-management engine: it detects the
// OS and package manager, and installs/removes/controls hosting components
// (Nginx, MariaDB, Redis, PHP, Node, Docker, …) through a strict allowlist of
// executables and provider-defined arguments. No user-supplied string is ever
// passed to a shell — the HTTP layer only selects a known component by name.
package software

import (
	"os"
	"runtime"
	"strings"
)

// OSInfo describes the detected platform and the package manager to use.
type OSInfo struct {
	Distro         string // e.g. ubuntu, rhel
	Family         string // debian, rhel, suse, windows, unknown
	Arch           string // amd64 | arm64
	PackageManager string // apt | dnf | zypper | winget
}

// Detect inspects the running OS. Linux reads /etc/os-release; Windows uses
// winget. Unsupported families still return a best-effort manager so callers
// can report honestly rather than guess.
func Detect() OSInfo {
	info := OSInfo{Arch: normalizeArch(runtime.GOARCH), Family: "unknown"}
	if runtime.GOOS == "windows" {
		info.PackageManager = "winget"
		info.Family = "windows"
		return info
	}
	if runtime.GOOS != "linux" {
		return info
	}

	fields := parseEnvFile("/etc/os-release")
	info.Distro = fields["ID"]
	like := fields["ID_LIKE"]
	blob := strings.ToLower(info.Distro + " " + like)

	switch {
	case strings.Contains(blob, "debian") || strings.Contains(blob, "ubuntu"):
		info.Family, info.PackageManager = "debian", "apt"
	case strings.Contains(blob, "suse") || strings.Contains(blob, "sles"):
		info.Family, info.PackageManager = "suse", "zypper"
	case strings.Contains(blob, "rhel") || strings.Contains(blob, "fedora") ||
		strings.Contains(blob, "centos") || strings.Contains(blob, "rocky") ||
		strings.Contains(blob, "alma"):
		info.Family, info.PackageManager = "rhel", "dnf"
	default:
		info.Family, info.PackageManager = "unknown", "apt" // conservative default
	}
	return info
}

func normalizeArch(a string) string {
	switch a {
	case "amd64", "x86_64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return a
	}
}

// parseEnvFile reads KEY="value" lines (e.g. /etc/os-release).
func parseEnvFile(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		out[strings.TrimSpace(k)] = v
	}
	return out
}

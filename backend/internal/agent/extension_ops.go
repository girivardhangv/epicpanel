package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ExtensionJobPayload matches runtimes.extensionJobPayload.
type ExtensionJobPayload struct {
	ExtensionID string `json:"extension_id"`
	RuntimeID   string `json:"runtime_id"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Name        string `json:"name"`
}

// extensionPackage maps a friendly extension name to its OS package base.
// PHP extensions use php<major>-<name>; OpenLiteSpeed extensions use
// lsphp<dotless major>-<name> from the LiteSpeed repository.
func extensionPackage(rtType, major, name string) ([]string, bool) {
	if rtType == "openlitespeed" || rtType == "lsphp" {
		ls := "lsphp" + strings.ReplaceAll(major, ".", "")
		switch name {
		case "mysql":
			return []string{ls + "-mysql"}, true
		case "opcache":
			return []string{ls + "-opcache"}, true
		default:
			return []string{ls + "-" + name}, true
		}
	}
	switch name {
	case "mysql":
		return []string{"php" + major + "-mysqli", "php" + major + "-mysql"}, true
	case "pgsql":
		return []string{"php" + major + "-pgsql"}, true
	case "imagick":
		return []string{"php" + major + "-imagick"}, true
	case "redis":
		return []string{"php" + major + "-redis"}, true
	case "opcache":
		return []string{"php" + major + "-opcache"}, true
	default:
		// Standard extension names map 1:1 (gd, curl, zip, mbstring, xml, ...).
		return []string{"php" + major + "-" + name}, true
	}
}

// InstallPHPExtension installs one PHP/LSPHP extension package for a version.
// Missing packages for exotic versions are a clean failure (state recorded),
// never a partial crash: apt is invoked per package.
func (e *Executor) InstallPHPExtension(ctx context.Context, rtType, version, name string) error {
	major := versionMajor(version)
	if major == "" {
		return fmt.Errorf("invalid PHP version %q", version)
	}
	// Frozen binaries (legacy static trees AND source builds' compiled-in
	// set): a bundled extension is already installed; anything outside the
	// compiled set on a frozen prefix cannot be attached via apt/pecl.
	if rtType != "openlitespeed" && rtType != "lsphp" {
		for _, cli := range []string{
			filepath.Join("/opt/epicpanel/php", major, "bin", "php"),      // source build
			filepath.Join("/opt/epicpanel/php-static", major, "bin", "php"), // legacy static
		} {
			if _, ferr := os.Stat(cli); ferr != nil {
				continue
			}
			if modules, merr := staticPHPModules(ctx, cli); merr == nil {
				for _, mod := range extNameToModules(name) {
					if modules[strings.ToLower(mod)] {
						return nil // compiled in — nothing to install
					}
				}
				return fmt.Errorf("extension %q is not part of the compiled PHP %s build (its extension set is fixed at compile time); add it via pecl with this version's phpize, or install PHP %s from distro or PPA repositories", name, major, major)
			}
			break
		}
	}
	packages, _ := extensionPackage(rtType, major, name)
	var installed bool
	var lastErr error
	for _, pkg := range packages {
		if dpkgInstalled(pkg) {
			installed = true
			continue
		}
		if err := e.aptInstall(ctx, []string{pkg}, "", version); err != nil {
			lastErr = err
			continue
		}
		installed = true
	}
	if !installed {
		return fmt.Errorf("extension %q is not available for %s %s: %v", name, rtType, major, lastErr)
	}
	if rtType != "openlitespeed" && rtType != "lsphp" {
		// New extensions need an FPM reload to be picked up by running pools.
		_ = e.run(ctx, "systemctl", "reload", phpFpmService(major))
	}
	return nil
}

// RemovePHPExtension purges an extension package (idempotent).
func (e *Executor) RemovePHPExtension(ctx context.Context, rtType, version, name string) error {
	major := versionMajor(version)
	if major == "" {
		return fmt.Errorf("invalid PHP version %q", version)
	}
	packages, _ := extensionPackage(rtType, major, name)
	args := append([]string{"purge", "-y"}, packages...)
	if err := e.run(ctx, "apt-get", args...); err != nil {
		// Packages not installed produce non-zero exit; treat as success if
		// nothing was installed in the first place.
		if !dpkgInstalledAny(packages) {
			return nil
		}
		return fmt.Errorf("apt-get purge: %w", err)
	}
	if rtType != "openlitespeed" && rtType != "lsphp" {
		_ = e.run(ctx, "systemctl", "reload", phpFpmService(major))
	}
	return nil
}

// staticPHPModules returns the lowercase module list of a PHP CLI (`php -m`),
// including Zend modules (opcache appears as "zend opcache").
func staticPHPModules(ctx context.Context, cli string) (map[string]bool, error) {
	out, err := exec.CommandContext(ctx, cli, "-m").Output()
	if err != nil {
		return nil, err
	}
	modules := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.ToLower(strings.TrimSpace(line))
		if name != "" {
			modules[name] = true
		}
	}
	return modules, nil
}

// extNameToModules maps a friendly extension name to the php -m module names
// that satisfy it (an extension may provide several modules).
func extNameToModules(name string) []string {
	switch name {
	case "mysql":
		return []string{"mysqli", "pdo_mysql", "mysqlnd"}
	case "pgsql":
		return []string{"pgsql", "pdo_pgsql"}
	case "xml":
		return []string{"dom", "simplexml", "xml", "xmlreader", "xmlwriter"}
	case "sqlite3":
		return []string{"sqlite3", "pdo_sqlite"}
	case "opcache":
		return []string{"zend opcache"}
	default:
		return []string{name}
	}
}

func dpkgInstalled(pkg string) bool {
	out, err := exec.Command("dpkg-query", "-W", "-f=${Status}", pkg).Output()
	return err == nil && strings.Contains(string(out), "install ok installed")
}

func dpkgInstalledAny(packages []string) bool {
	for _, p := range packages {
		if dpkgInstalled(p) {
			return true
		}
	}
	return false
}

// DetectedSoftware is one installed managed software item (detect_software job).
type DetectedSoftware struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

// DetectSoftware inventories managed software already present on the machine
// (used by the setup wizard and to adopt installs made during setup).
func (e *Executor) DetectSoftware(ctx context.Context) []DetectedSoftware {
	var out []DetectedSoftware

	// PHP: /etc/php/<major>/fpm presence + cli version.
	entries, _ := filepath.Glob("/etc/php/*")
	sort.Strings(entries)
	for _, dir := range entries {
		major := filepath.Base(dir)
		if _, err := os.Stat(filepath.Join(dir, "fpm")); err == nil {
			out = append(out, DetectedSoftware{Type: "php", Version: major})
		}
	}
	if v, ok := singleLineVersion(ctx, "node", "--version", "v"); ok {
		out = append(out, DetectedSoftware{Type: "node", Version: v})
	}
	if out2, err := exec.CommandContext(ctx, "python3", "--version").Output(); err == nil {
		// Python 3.12.7
		fields := strings.Fields(string(out2))
		if len(fields) == 2 {
			out = append(out, DetectedSoftware{Type: "python", Version: nodeMajor(strings.TrimPrefix(fields[1], "Python "))})
		}
	}
	if out3, err := exec.CommandContext(ctx, "go", "version").Output(); err == nil {
		fields := strings.Fields(string(out3))
		if len(fields) >= 3 {
			out = append(out, DetectedSoftware{Type: "go", Version: nodeMajor(strings.TrimPrefix(fields[2], "go"))})
		}
	}
	if dpkgInstalled("apache2") {
		out = append(out, DetectedSoftware{Type: "apache", Version: "2.4"})
	}
	if _, err := os.Stat("/usr/local/lsws/bin/openlitespeed"); err == nil {
		out = append(out, DetectedSoftware{Type: "openlitespeed", Version: "1.8"})
	}
	if dpkgInstalled("mariadb-server") {
		out = append(out, DetectedSoftware{Type: "mariadb", Version: "10.x"})
	}
	if dpkgInstalled("postgresql") || dpkgInstalled("postgresql-16") || dpkgInstalled("postgresql-15") {
		out = append(out, DetectedSoftware{Type: "postgres", Version: "16"})
	}
	if _, err := os.Stat("/srv/epicpanel/dbadmin/phpmyadmin/index.php"); err == nil {
		out = append(out, DetectedSoftware{Type: "dbtools", Version: "latest"})
	}
	return out
}

func singleLineVersion(ctx context.Context, bin string, args ...string) (string, bool) {
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil {
		return "", false
	}
	v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	if v == "" {
		return "", false
	}
	return v, true
}

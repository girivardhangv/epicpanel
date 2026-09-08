package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
)

// PackageManager abstracts the system package manager across Linux distros.
// The agent detects it at startup and routes all install/remove/purge
// operations through it. The software/runtime agent provides package NAMES;
// this layer provides the MECHANISM.
type PackageManager interface {
	// Name returns the detected manager: apt | dnf | yum | zypper | pacman | apk
	Name() string
	// Update refreshes package indexes. Best-effort: errors are tolerated
	// (installs proceed from cached indexes).
	Update(ctx context.Context) error
	// Install installs packages by name. Tolerant of missing individual
	// packages when tolerant=true.
	Install(ctx context.Context, packages []string) error
	// InstallBestEffort installs each package individually, skipping
	// unavailable ones (for optional extensions).
	InstallBestEffort(ctx context.Context, packages []string) error
	// Remove purges packages.
	Remove(ctx context.Context, packages []string) error
	// IsInstalled checks if a binary is available on PATH.
	IsInstalled(bin string) bool
}

var (
	pmOnce   sync.Once
	pmDetect PackageManager
)

// DetectPackageManager probes the system for the available package manager.
func DetectPackageManager() PackageManager {
	pmOnce.Do(func() {
		for _, candidate := range []PackageManager{
			&APTManager{}, &DNFManager{}, &YUMManager{},
			&ZypperManager{}, &PacmanManager{}, &APKManager{},
		} {
			if _, err := exec.LookPath(candidate.Name()); err == nil {
				pmDetect = candidate
				slog.Info("package manager detected", "manager", candidate.Name())
				return
			}
		}
		// fallback: apt is most common
		pmDetect = &APTManager{}
		slog.Warn("no package manager detected, defaulting to apt")
	})
	return pmDetect
}

// --- apt (Debian, Ubuntu, Mint, ...) ---

type APTManager struct{}

func (a *APTManager) Name() string { return "apt" }

func (a *APTManager) Update(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 120*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "apt-get", "update")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apt-get update: %s (%w)", tailString(string(out), 200), err)
	}
	return nil
}

func (a *APTManager) Install(ctx context.Context, packages []string) error {
	c, cancel := context.WithTimeout(ctx, 600*1000*1000*1000)
	defer cancel()
	args := append([]string{"install", "-y", "--no-install-recommends"}, packages...)
	cmd := exec.CommandContext(c, "apt-get", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apt-get install: %s (%w)", tailString(string(out), 300), err)
	}
	return nil
}

func (a *APTManager) InstallBestEffort(ctx context.Context, packages []string) error {
	for _, p := range packages {
		c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
		cmd := exec.CommandContext(c, "apt-get", "install", "-y", "--no-install-recommends", p)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			slog.Warn("skipping unavailable package", "package", p, "err", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func (a *APTManager) Remove(ctx context.Context, packages []string) error {
	args := append([]string{"purge", "-y"}, packages...)
	c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "apt-get", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apt-get purge: %s (%w)", tailString(string(out), 300), err)
	}
	return nil
}

func (a *APTManager) IsInstalled(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// --- dnf (Fedora, RHEL 8+, Rocky, Alma) ---

type DNFManager struct{}

func (d *DNFManager) Name() string { return "dnf" }

func (d *DNFManager) Update(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 120*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "dnf", "makecache", "--refresh", "-y")
	_, err := cmd.CombinedOutput()
	return err
}

func (d *DNFManager) Install(ctx context.Context, packages []string) error {
	c, cancel := context.WithTimeout(ctx, 600*1000*1000*1000)
	defer cancel()
	args := append([]string{"install", "-y"}, packages...)
	cmd := exec.CommandContext(c, "dnf", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("dnf install: %s (%w)", tailString(string(out), 300), err)
	}
	return nil
}

func (d *DNFManager) InstallBestEffort(ctx context.Context, packages []string) error {
	for _, p := range packages {
		c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
		cmd := exec.CommandContext(c, "dnf", "install", "-y", p)
		_, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			slog.Warn("skipping unavailable package", "package", p)
		}
	}
	return nil
}

func (d *DNFManager) Remove(ctx context.Context, packages []string) error {
	args := append([]string{"remove", "-y"}, packages...)
	c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "dnf", args...)
	_, err := cmd.CombinedOutput()
	return err
}

func (d *DNFManager) IsInstalled(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// --- yum (RHEL 7, CentOS 7) ---

type YUMManager struct{ DNFManager }

func (y *YUMManager) Name() string { return "yum" }

func (y *YUMManager) Update(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 120*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "yum", "makecache", "-y")
	_, err := cmd.CombinedOutput()
	return err
}

func (y *YUMManager) Install(ctx context.Context, packages []string) error {
	args := append([]string{"install", "-y"}, packages...)
	c, cancel := context.WithTimeout(ctx, 600*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "yum", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("yum install: %s (%w)", tailString(string(out), 300), err)
	}
	return nil
}

func (y *YUMManager) InstallBestEffort(ctx context.Context, packages []string) error {
	for _, p := range packages {
		c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
		cmd := exec.CommandContext(c, "yum", "install", "-y", p)
		_, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			slog.Warn("skipping unavailable package", "package", p)
		}
	}
	return nil
}

func (y *YUMManager) Remove(ctx context.Context, packages []string) error {
	args := append([]string{"remove", "-y"}, packages...)
	c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "yum", args...)
	_, err := cmd.CombinedOutput()
	return err
}

// --- zypper (openSUSE) ---

type ZypperManager struct{}

func (z *ZypperManager) Name() string { return "zypper" }

func (z *ZypperManager) Update(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 120*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "zypper", "--non-interactive", "refresh")
	_, err := cmd.CombinedOutput()
	return err
}

func (z *ZypperManager) Install(ctx context.Context, packages []string) error {
	args := append([]string{"--non-interactive", "install"}, packages...)
	c, cancel := context.WithTimeout(ctx, 600*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "zypper", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("zypper install: %s (%w)", tailString(string(out), 300), err)
	}
	return nil
}

func (z *ZypperManager) InstallBestEffort(ctx context.Context, packages []string) error {
	for _, p := range packages {
		c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
		cmd := exec.CommandContext(c, "zypper", "--non-interactive", "install", p)
		_, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			slog.Warn("skipping unavailable package", "package", p)
		}
	}
	return nil
}

func (z *ZypperManager) Remove(ctx context.Context, packages []string) error {
	args := append([]string{"--non-interactive", "remove"}, packages...)
	c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "zypper", args...)
	_, err := cmd.CombinedOutput()
	return err
}

func (z *ZypperManager) IsInstalled(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// --- pacman (Arch, Manjaro) ---

type PacmanManager struct{}

func (p *PacmanManager) Name() string { return "pacman" }

func (p *PacmanManager) Update(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 120*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "pacman", "-Sy", "--noconfirm")
	_, err := cmd.CombinedOutput()
	return err
}

func (p *PacmanManager) Install(ctx context.Context, packages []string) error {
	args := append([]string{"-S", "--noconfirm", "--needed"}, packages...)
	c, cancel := context.WithTimeout(ctx, 600*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "pacman", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pacman install: %s (%w)", tailString(string(out), 300), err)
	}
	return nil
}

func (p *PacmanManager) InstallBestEffort(ctx context.Context, packages []string) error {
	// pacman atomically installs all; try all first, then individually
	if err := p.Install(ctx, packages); err != nil {
		for _, pkg := range packages {
			p.Install(ctx, []string{pkg})
		}
	}
	return nil
}

func (p *PacmanManager) Remove(ctx context.Context, packages []string) error {
	args := append([]string{"-Rns", "--noconfirm"}, packages...)
	c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "pacman", args...)
	_, err := cmd.CombinedOutput()
	return err
}

func (p *PacmanManager) IsInstalled(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// --- apk (Alpine) ---

type APKManager struct{}

func (a *APKManager) Name() string { return "apk" }

func (a *APKManager) Update(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, 120*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "apk", "update")
	_, err := cmd.CombinedOutput()
	return err
}

func (a *APKManager) Install(ctx context.Context, packages []string) error {
	args := append([]string{"add", "--no-cache"}, packages...)
	c, cancel := context.WithTimeout(ctx, 600*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "apk", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apk add: %s (%w)", tailString(string(out), 300), err)
	}
	return nil
}

func (a *APKManager) InstallBestEffort(ctx context.Context, packages []string) error {
	for _, p := range packages {
		c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
		cmd := exec.CommandContext(c, "apk", "add", "--no-cache", p)
		_, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			slog.Warn("skipping unavailable package", "package", p)
		}
	}
	return nil
}

func (a *APKManager) Remove(ctx context.Context, packages []string) error {
	args := append([]string{"del"}, packages...)
	c, cancel := context.WithTimeout(ctx, 300*1000*1000*1000)
	defer cancel()
	cmd := exec.CommandContext(c, "apk", args...)
	_, err := cmd.CombinedOutput()
	return err
}

func (a *APKManager) IsInstalled(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

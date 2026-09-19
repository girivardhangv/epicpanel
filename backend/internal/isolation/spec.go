package isolation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Spec describes one site's sandbox. It is the INTEGRATION CONTRACT between
// this module and every other layer:
//
//   - The software/runtime agent registers runtime roots via RuntimeMounts
//     (read-only binds) — e.g. "/usr/local/go-1.22.2" or a PHP version root.
//     This module mounts them into the sandbox; it never installs runtimes.
//   - The packages/limits layer may override Rlimits per plan via the
//     same config file (fields are additive and validated here).
//
// The spec is persisted per site at ConfigPath(websiteID) by the agent
// reconciler and re-read on every session. Missing file => DefaultSpec()
// (still fully sandboxed; fail closed).
type Spec struct {
	Version       int           `json:"version"`
	WebsiteID     string        `json:"website_id"`
	SiteBase      string        `json:"site_base"`                // writable, mounted at /site
	HostUsername  string        `json:"host_username"`            // site Unix user
	UID           int           `json:"uid"`                      // site Unix uid (setuid target)
	GID           int           `json:"gid"`                      // site Unix gid
	RuntimeMounts []Mount       `json:"runtime_mounts"`           // SOFTWARE AGENT CONTRACT: read-only runtime roots
	ExtraRO       []Mount       `json:"extra_ro_binds,omitempty"` // additional read-only binds
	Rlimits       Rlimits       `json:"rlimits"`
	Network       NetworkPolicy `json:"network"`
}

// Mount is a read-only bind of src (host) at dst (sandbox).
type Mount struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// Rlimits are applied pre-exec and inherited by everything in the sandbox.
type Rlimits struct {
	MaxProc    int `json:"max_proc"`              // RLIMIT_NPROC
	MaxFiles   int `json:"max_files"`             // RLIMIT_NOFILE
	MaxMemMB   int `json:"max_mem_mb"`            // cgroup MemoryMax: web-terminal via systemd-run scope, SSH login shells via agent user-<uid>.slice convergence (no RLIMIT_AS — breaks VM-based runtimes)
	MaxFileMB  int `json:"max_file_mb"`           // RLIMIT_FSIZE
	CPUSeconds int `json:"cpu_seconds,omitempty"` // RLIMIT_CPU (0 = unlimited)
	CPUQuota   int `json:"cpu_quota,omitempty"`   // cgroup CPUQuota in percentage (e.g., 20 for 20%)
}

// NetworkPolicy is a hook for future network isolation. SharedHostNetwork is
// currently always true (hosting users need outbound access for git,
// composer, npm). When false, --unshare-net is applied (no outbound).
type NetworkPolicy struct {
	SharedHostNetwork bool `json:"shared_host_network"`
}

// DefaultRlimits are safe for normal hosting workloads.
func DefaultRlimits() Rlimits {
	return Rlimits{MaxProc: 256, MaxFiles: 1024, MaxMemMB: 2048, MaxFileMB: 2048, CPUSeconds: 0, CPUQuota: 0}
}

// DefaultNetwork returns the documented default: shared host network.
func DefaultNetwork() NetworkPolicy { return NetworkPolicy{SharedHostNetwork: true} }

// DefaultSpec returns a minimal, still-fully-sandboxed spec. Used (fail
// closed) when no config exists yet: no runtime mounts, only base system.
func DefaultSpec(websiteID, siteBase, hostUser string, uid, gid int) Spec {
	return Spec{
		Version:       1,
		WebsiteID:     websiteID,
		SiteBase:      siteBase,
		HostUsername:  hostUser,
		UID:           uid,
		GID:           gid,
		RuntimeMounts: nil,
		Rlimits:       DefaultRlimits(),
		Network:       DefaultNetwork(),
	}
}

var specDir = "/etc/epicpanel/sandbox"

// siteBasePrefix can be overridden in tests.
var siteBasePrefix = "/srv/epicpanel/websites/"

// ConfigPath is where a site's spec file lives on the server.
func ConfigPath(websiteID string) string { return filepath.Join(specDir, websiteID+".json") }

// SetConfigDir overrides the spec directory (tests).
func SetConfigDir(dir string) { specDir = dir }

// SetSiteBasePrefix overrides the allowed site-base prefix (tests).
func SetSiteBasePrefix(dir string) { siteBasePrefix = dir }

// Save writes the spec atomically (agent-side reconciler).
func (s *Spec) Save() error {
	if err := s.Validate(); err != nil {
		return fmt.Errorf("spec invalid: %w", err)
	}
	if err := os.MkdirAll(specDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := ConfigPath(s.WebsiteID) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ConfigPath(s.WebsiteID))
}

// Load reads a site's spec, or returns DefaultSpec when absent (fail closed:
// the caller still gets a sandbox — only narrower).
func Load(websiteID, siteBase, hostUser string, uid, gid int) Spec {
	b, err := os.ReadFile(ConfigPath(websiteID))
	if err != nil {
		return DefaultSpec(websiteID, siteBase, hostUser, uid, gid)
	}
	var s Spec
	if err := json.Unmarshal(b, &s); err != nil {
		// corrupted config: fail closed to defaults, never unsandboxed
		return DefaultSpec(websiteID, siteBase, hostUser, uid, gid)
	}
	if s.Version != 1 || s.WebsiteID != websiteID || s.SiteBase != siteBase {
		return DefaultSpec(websiteID, siteBase, hostUser, uid, gid)
	}
	if s.Rlimits.MaxProc <= 0 {
		s.Rlimits = DefaultRlimits()
	}
	if !s.Network.SharedHostNetwork && s.Rlimits == (Rlimits{}) {
		s.Rlimits = DefaultRlimits()
	}
	return s
}

// Validate enforces the security invariants of a spec before use.
func (s *Spec) Validate() error {
	if s.Version != 1 {
		return errors.New("unsupported spec version")
	}
	if parseUUID(s.WebsiteID) != nil {
		return errors.New("website_id must be a UUID")
	}
	if !strings.HasPrefix(s.SiteBase, siteBasePrefix) || strings.Contains(s.SiteBase, "..") {
		return errors.New("site_base outside /srv/epicpanel/websites/")
	}
	if s.UID == 0 || s.GID == 0 {
		return errors.New("refusing to sandbox as uid/gid 0")
	}
	for _, m := range append(append([]Mount{}, s.RuntimeMounts...), s.ExtraRO...) {
		if !filepath.IsAbs(m.Src) || !filepath.IsAbs(m.Dst) || strings.Contains(m.Dst, "..") {
			return fmt.Errorf("mount %s->%s must be absolute", m.Src, m.Dst)
		}
		// runtime mounts must never shadow the writable site mount
		if m.Dst == "/site" || strings.HasPrefix(m.Dst, "/site/") {
			return errors.New("mount destination may not overlap writable /site")
		}
		// runtime mounts must never expose the host's private dirs
		for _, denied := range []string{"/root", "/etc/shadow", "/var/lib/private", "/dev"} {
			if m.Src == denied {
				return fmt.Errorf("mount source %s is denied", m.Src)
			}
		}
	}
	if s.Rlimits.MaxProc <= 0 || s.Rlimits.MaxFiles <= 0 || s.Rlimits.MaxMemMB <= 0 {
		return errors.New("rlimits must be positive")
	}
	return nil
}

func parseUUID(s string) error {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return errors.New("not a uuid")
	}
	for _, c := range strings.ReplaceAll(s, "-", "") {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return errors.New("not a uuid")
		}
	}
	return nil
}

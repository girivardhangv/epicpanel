package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/isolation"
)

// SandboxSync reconciles the per-site sandbox spec files
// (/etc/epicpanel/sandbox/<websiteID>.json) from runtime inventory data.
//
// INTEGRATION CONTRACT (ADR-037): this is the ONLY touchpoint between the
// isolation layer and the software/runtime agent's data. It consumes a
// RuntimeInfo list (supplied by the caller — the agent daemon fetches it
// from the control plane, or a caller with DB access passes rows) and
// converts runtime roots into read-only mounts in the sandbox spec. It never
// installs, removes, or configures runtimes itself.
type SandboxSync struct{}

// RuntimeInfo is one installed runtime as reported by the software layer.
type RuntimeInfo struct {
	Type    string // php | node | python | go
	Version string // e.g. 8.5, 22, 1.22
}

// SyncSite builds and saves the sandbox spec for one website. siteUserUID /
// siteUserGID come from the site tree owner; runtimeRoots are supplied by
// the caller per the contract (empty = only system runtimes available).
func (SandboxSync) SyncSite(websiteID uuid.UUID, siteBase string, siteUserUID, siteUserGID int, runtimeRoots []string) error {
	spec := isolation.DefaultSpec(websiteID.String(), siteBase, "", siteUserUID, siteUserGID)
	for _, root := range runtimeRoots {
		if isSystemPath(root) {
			continue // already covered by the read-only system bind
		}
		spec.RuntimeMounts = append(spec.RuntimeMounts, isolation.Mount{Src: root, Dst: root})
	}
	if err := spec.Save(); err != nil {
		return fmt.Errorf("save spec: %w", err)
	}
	slog.Info("sandbox spec saved", "website", websiteID, "runtime_mounts", len(spec.RuntimeMounts))
	return nil
}

// runtimeRoot maps a runtime type/version to its host root directory.
// CONTRACT: the software agent owns the actual paths; this mapping only
// covers the two install layouts that exist today:
//   - apt/PPA runtimes (php, node, python): /usr — covered by system bind
//   - Go toolchains: /usr/local/go-<full-version> — outside /usr
func runtimeRoot(rtType, version string) string {
	switch rtType {
	case "go":
		matches, _ := filepath.Glob("/usr/local/go-" + version + ".*")
		if len(matches) > 0 {
			return matches[len(matches)-1]
		}
		return ""
	default:
		return "/usr" // system bind covers it
	}
}

func isSystemPath(root string) bool {
	return root == "/usr" || root == "/bin" || root == "/lib"
}

func ownerOf(path string) (int, int, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	if st, ok := info.Sys().(*syscallStatT); ok {
		return int(st.Uid), int(st.Gid), nil
	}
	return 0, 0, fmt.Errorf("stat unsupported")
}

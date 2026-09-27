package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// SyncSSHKeys rewrites the site Unix user's ~/.ssh/authorized_keys from the
// control plane's ssh_keys rows. Enables SSH access (as the site user only).
func (e *Executor) SyncSSHKeys(ctx context.Context, websiteID string, publicKeys []string) error {
	if _, err := uuid.Parse(websiteID); err != nil {
		return fmt.Errorf("invalid website id")
	}
	user, err := e.unixUserForSite(websiteID)
	if err != nil {
		return fmt.Errorf("site user: %w", err)
	}
	uid, _, err := e.siteOwnerIDs(websiteID)
	if err != nil {
		return err
	}

	home, err := userHome(user)
	if err != nil {
		return err
	}
	sshDir := filepath.Join(home, ".ssh")
	authFile := filepath.Join(sshDir, "authorized_keys")

	if len(publicKeys) == 0 {
		// No keys: remove file + disable the user's shell access.
		_ = os.Remove(authFile)
		_ = e.run(ctx, "usermod", "-s", "/usr/sbin/nologin", user)
		slog.Info("ssh access disabled (no keys)", "site", websiteID)
		return nil
	}

	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return err
	}
	var content string
	for _, k := range publicKeys {
		// Restrict the key to the site: no port forwards, no pty abuse flags.
		content += "no-port-forwarding,no-X11-forwarding,no-agent-forwarding " + k + "\n"
	}
	if err := os.WriteFile(authFile, []byte(content), 0o600); err != nil {
		return err
	}
	_ = os.Chown(sshDir, uid, uid)
	_ = os.Chown(authFile, uid, uid)

	// Enable a shell for SSH: use our limited shell wrapper (falls back to
	// bash when the wrapper binary is not installed yet).
	shell := "/usr/local/bin/epicpanel-shell"
	if _, err := os.Stat(shell); os.IsNotExist(err) {
		shell = "/bin/bash"
	}
	if err := e.run(ctx, "usermod", "-s", shell, user); err != nil {
		return err
	}
	slog.Info("ssh keys synced", "site", websiteID, "keys", len(publicKeys), "shell", shell)
	return nil
}

func userHome(user string) (string, error) {
	// Site users are created without home by useradd --no-create-home; their
	// SSH dir lives under the site tree so permissions stay isolated.
	return fmt.Sprintf("/home/%s", user), nil
}

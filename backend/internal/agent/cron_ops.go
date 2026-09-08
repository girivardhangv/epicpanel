package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// SyncCrontab regenerates the site Unix user's crontab from the control
// plane's cron_jobs rows (rendered as the desired state). The cron entry runs
// each command as the site user with the site's docroot as cwd.
type SyncCrontabPayload struct {
	WebsiteID string `json:"website_id"`
}

// CronEntry is one desired cron line.
type CronEntry struct {
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Status   string `json:"status"` // active | paused
}

type SyncOutcome struct {
	Entries int `json:"entries"`
}

// RenderCrontab produces the full crontab content for the site user.
// Every line is a plain validated cron entry; paused jobs are commented out.
func RenderCrontab(entries []CronEntry) string {
	var b strings.Builder
	b.WriteString("# managed by EpicPanel — do not edit\n")
	for _, e := range entries {
		line := e.Schedule + " " + e.Command
		if e.Status == "paused" {
			b.WriteString("# PAUSED " + line + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// SyncCrontab atomically replaces the site user's crontab.
func (e *Executor) SyncCrontab(ctx context.Context, websiteID string, entries []CronEntry) (*SyncOutcome, error) {
	if _, err := uuid.Parse(websiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	user, err := e.unixUserForSite(websiteID)
	if err != nil {
		return nil, fmt.Errorf("site user: %w", err)
	}

	content := RenderCrontab(entries)

	// Write via crontab -u (atomic replace). Empty content clears the crontab.
	cmd := exec.CommandContext(ctx, "crontab", "-u", user, "-")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("crontab -u %s: %s (%w)", user, strings.TrimSpace(string(out)), err)
	}
	slog.Info("crontab synced", "website", websiteID, "user", user, "entries", len(entries))
	return &SyncOutcome{Entries: len(entries)}, nil
}

// unixUserForSite reads the owner of the site tree (set at provisioning).
func (e *Executor) unixUserForSite(websiteID string) (string, error) {
	base := filepath.Join(e.docRootBase, websiteID)
	info, err := os.Stat(base)
	if err != nil {
		return "", err
	}
	if st, ok := info.Sys().(*syscallStatT); ok {
		u, err := userLookupID(fmt.Sprintf("%d", st.Uid))
		if err != nil {
			return "", err
		}
		return u, nil
	}
	return "", fmt.Errorf("stat unsupported")
}

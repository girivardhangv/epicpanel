package agent

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// SITE COMMANDS (composer / npm / artisan / node — run as the SITE USER)
// ============================================================================

// CommandPayload is a validated user command for one website. argv arrives
// pre-validated by the control plane; the agent re-validates before exec —
// the command is NEVER run through a shell.
type CommandPayload struct {
	WebsiteID      string   `json:"website_id"`
	UnixUser       string   `json:"unix_user"`
	Runtime        string   `json:"runtime"`
	RuntimeVersion string   `json:"runtime_version"`
	DocumentRoot   string   `json:"document_root"`
	Argv           []string `json:"argv"`
}

// CommandOutcome carries the combined command output (tail-capped).
type CommandOutcome struct {
	Output string `json:"output"`
	Dir    string `json:"dir"`
}

// siteCommandTokens re-validated agent-side. The control plane enforces the
// same set — this is defense in depth, not the primary gate.
var siteCommandTokens = map[string]bool{
	"composer": true, "php": true, "artisan": true, "wp": true,
	"node": true, "npm": true, "npx": true, "yarn": true, "pnpm": true,
	"python3": true, "pip3": true, "pip": true, "python": true,
	"git": true, "go": true, "grep": true, "cat": true, "ls": true,
}

// siteCommandTokenRe restricts every argv token: no shell metacharacters,
// no whitespace tricks — argv is built from these tokens verbatim.
const siteCommandMaxTokens = 24

func validSiteCommandToken(t string) bool {
	if t == "" || len(t) > 256 {
		return false
	}
	for _, c := range t {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.' || c == '/' || c == '@' ||
			c == ':' || c == '=' || c == '+' || c == '~' || c == '%':
		default:
			return false
		}
	}
	return true
}

// RunSiteCommand executes an allowlisted command inside the site tree as the
// site user. PHP commands (composer/php/artisan/wp) are pinned to the site's
// selected PHP version; node tooling resolves from PATH. Node apps run in
// <site>/app when it exists, everything else in the serving docroot.
func (e *Executor) RunSiteCommand(ctx context.Context, p CommandPayload) (*CommandOutcome, error) {
	if _, err := uuid.Parse(p.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	if len(p.Argv) == 0 || len(p.Argv) > siteCommandMaxTokens {
		return nil, fmt.Errorf("command must have 1..%d tokens", siteCommandMaxTokens)
	}
	for i, tok := range p.Argv {
		if i == 0 && !siteCommandTokens[tok] {
			return nil, fmt.Errorf("command %q is not allowed", tok)
		}
		if !validSiteCommandToken(tok) {
			return nil, fmt.Errorf("invalid token %q", tok)
		}
	}

	docRoot := p.DocumentRoot
	if !strings.HasPrefix(docRoot, "/srv/epicpanel/websites/") {
		return nil, fmt.Errorf("document root outside epicpanel tree")
	}
	workDir := docRoot
	if isAppRuntime(p.Runtime) {
		if appDir := filepath.Join(e.docRootBase, p.WebsiteID, "app"); fileExists(appDir) {
			workDir = appDir
		}
	}

	uid, gid, err := siteOwnerIDs(p.WebsiteID)
	if err != nil {
		return nil, fmt.Errorf("site owner: %w", err)
	}

	argv := make([]string, len(p.Argv))
	copy(argv, p.Argv)
	switch argv[0] {
	case "composer", "php", "artisan", "wp":
		minor := strings.TrimSpace(p.RuntimeVersion)
		if minor == "" || !phpMinorRe.MatchString(minor) {
			minor = latestInstalledPHPMinor(e)
		}
		php := phpBinary(minor)
		switch argv[0] {
		case "composer":
			argv = append([]string{php, composerBin}, argv[1:]...)
		case "php":
			argv = append([]string{php}, argv[1:]...)
		case "artisan":
			argv = append([]string{php, "artisan"}, argv[1:]...)
		case "wp":
			argv = append([]string{php, "/usr/local/bin/wp", "--path=" + workDir}, argv[1:]...)
		}
	}

	// setpriv drops to the site user; argv is exec'd directly (no shell).
	// No --inh-caps: setpriv variants reject numeric specs ("unknown
	// capability 0"), and dropping uid/gid already sheds capabilities.
	c, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	full := append([]string{"--reuid", fmt.Sprint(uid), "--regid", fmt.Sprint(gid),
		"--clear-groups", "--",
		"env", "HOME=" + workDir, "USER=" + fmt.Sprint(uid), "TERM=xterm",
		"PATH=/usr/local/bin:/usr/bin:/bin"}, argv...)
	cmd := exec.CommandContext(c, "setpriv", full...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	output := tailString(string(out), 16000)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("command timed out after 10m: %s", tailString(output, 800))
	}
	if err != nil {
		// A non-zero exit is a normal command outcome, not an agent failure —
		// the job must succeed so the result (with the output) reaches the UI.
		if ee, ok := err.(*exec.ExitError); ok {
			output += fmt.Sprintf("\n[exit status %d]", ee.ExitCode())
		} else {
			return nil, fmt.Errorf("run command: %w", err)
		}
	}
	return &CommandOutcome{Output: output, Dir: workDir}, nil
}

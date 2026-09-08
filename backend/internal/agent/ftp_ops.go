package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// FTP/SFTP ACCOUNT SYNC — converges vsftpd virtual users + OpenSSH
// internal-sftp accounts for one website. Mirrors the wire shape of
// ftpaccounts.SyncPayload (control plane); the agent does not import it.
// Payloads carry one-way $6$ SHA-512-crypt hashes only — plaintext never
// reaches the agent. No shell: every mutation is argv or an atomic file write.
// ============================================================================

const (
	ftpProtoFTP  = "ftp"
	ftpProtoSFTP = "sftp"
)

// FTPAccountSpec is one desired account (ftpaccounts.SyncAccount wire form).
type FTPAccountSpec struct {
	ID            string `json:"id"`
	UserName      string `json:"user_name"`
	Protocol      string `json:"protocol"`
	PasswordCrypt string `json:"password_crypt"`
	HomeDir       string `json:"home_dir"`
}

// FTPSyncPayload is the sync_ftp_accounts job payload. Zero accounts is a
// valid desired state: every managed account for the website is removed.
type FTPSyncPayload struct {
	WebsiteID string           `json:"website_id"`
	Accounts  []FTPAccountSpec `json:"accounts"`
}

// FTPOutcome reports convergence counts. Created/Removed count materialized
// or deleted accounts (sftp system users + stale ftp credentials/configs).
type FTPOutcome struct {
	Created   int  `json:"created"`
	Removed   int  `json:"removed"`
	SFTPUsers int  `json:"sftp_users"`
	FTPUsers  int  `json:"ftp_users"`
	Reloaded  bool `json:"reloaded"`
}

// ftpPaths holds every managed config location; injectable so tests can point
// the whole sync at a temp dir.
type ftpPaths struct {
	sshdDropIn   string
	virtualUsers string
	userConfDir  string
	pamFile      string
	mainConf     string
	unitDropIn   string
}

func defaultFTPPaths() ftpPaths {
	return ftpPaths{
		sshdDropIn:   "/etc/ssh/sshd_config.d/epicpanel-ftp.conf",
		virtualUsers: "/etc/vsftpd/epicpanel/virtual-users.txt",
		userConfDir:  "/etc/vsftpd/epicpanel/users",
		pamFile:      "/etc/pam.d/epicpanel-vsftpd",
		mainConf:     "/etc/vsftpd/epicpanel/vsftpd.conf",
		unitDropIn:   "/etc/systemd/system/vsftpd.service.d/epicpanel.conf",
	}
}

var (
	// ftpUserRe: control-plane DeriveUserName form, ≤32 chars total so it
	// always satisfies validUnixUserName.
	ftpUserRe  = regexp.MustCompile(`^ep-ftp-[a-z0-9-]{1,25}$`)
	ftpCryptRe = regexp.MustCompile(`^\$6\$[A-Za-z0-9./=$]{1,199}$`)
)

// SyncFTPAccounts converges the FTP/SFTP accounts of one website, idempotently:
// unchanged rendered files and unchanged users cause no writes and no reloads.
func (e *Executor) SyncFTPAccounts(ctx context.Context, payload FTPSyncPayload) (FTPOutcome, error) {
	return e.syncFTPAccounts(ctx, defaultFTPPaths(), payload)
}

func (e *Executor) syncFTPAccounts(ctx context.Context, p ftpPaths, payload FTPSyncPayload) (FTPOutcome, error) {
	out := FTPOutcome{}

	websiteID, err := ftpValidatePayload(payload, e.docRootBase)
	if err != nil {
		return out, err
	}
	prefix := "ep-ftp-" + websiteID[:8] + "-"

	var sftpList, ftpList []FTPAccountSpec
	for _, a := range payload.Accounts {
		if a.Protocol == ftpProtoSFTP {
			sftpList = append(sftpList, a)
		} else {
			ftpList = append(ftpList, a)
		}
	}
	out.SFTPUsers = len(sftpList)
	out.FTPUsers = len(ftpList)

	// Current state (reads only) + pure diff, before any mutation.
	sshdExisting := ftpReadFile(p.sshdDropIn)
	virtualExisting := ftpReadFile(p.virtualUsers)
	existingPerUser := ftpExistingPerUser(p.userConfDir, prefix)

	removedSFTP := ftpRemovals(ftpParseMatchUsers(sshdExisting), sftpList, prefix)
	curFTP := append(ftpParseVirtualUserNames(virtualExisting), existingPerUser...)
	removedFTP := ftpRemovals(curFTP, ftpList, prefix)
	out.Removed = len(removedSFTP) + len(removedFTP)

	virtualNames := map[string]bool{}
	for _, u := range ftpParseVirtualUserNames(virtualExisting) {
		virtualNames[u] = true
	}
	perUserNames := map[string]bool{}
	for _, u := range existingPerUser {
		perUserNames[u] = true
	}
	for _, a := range ftpList {
		if !virtualNames[a.UserName] && !perUserNames[a.UserName] {
			out.Created++
		}
	}

	// --- SFTP: system user + one-way hash + home dir -------------------
	for _, a := range sftpList {
		isNew, err := e.ftpEnsureSFTPUser(ctx, a)
		if isNew {
			out.Created++
		}
		if err != nil {
			return out, err
		}
	}

	// SFTP drop-in: rebuild this site's Match blocks, keep other sites'.
	// No ChrootDirectory: the site tree is owned by the site unix user while
	// sshd requires a root-owned chroot path; ForceCommand internal-sftp with
	// a fixed start directory is the safe equivalent (these users authenticate
	// by password and are separate system users — no authorized_keys).
	sshdChanged := false
	desiredSSHD := ftpRenderSFTPConf(sshdExisting, sftpList, prefix)
	switch {
	case desiredSSHD == "":
		if sshdExisting != "" {
			if err := os.Remove(p.sshdDropIn); err != nil && !os.IsNotExist(err) {
				return out, fmt.Errorf("remove %s: %w", p.sshdDropIn, err)
			}
			sshdChanged = true
		}
	case desiredSSHD != sshdExisting:
		if err := os.MkdirAll(filepath.Dir(p.sshdDropIn), 0o755); err != nil {
			return out, fmt.Errorf("create %s: %w", filepath.Dir(p.sshdDropIn), err)
		}
		if err := SwapValidated(p.sshdDropIn, []byte(desiredSSHD), 0o644, func() error {
			return ValidateCmd(ctx, validateSwapTimeout, "sshd", "-t")
		}); err != nil {
			return out, fmt.Errorf("sshd config: %w", err)
		}
		sshdChanged = true
	}

	// Delete sftp system users only after the drop-in no longer references
	// them. The prefix guard in ftpRemovals makes cross-site deletion impossible.
	for _, u := range removedSFTP {
		if _, err := user.Lookup(u); err != nil {
			continue
		}
		if err := e.run(ctx, "userdel", u); err != nil {
			return out, fmt.Errorf("userdel %s: %w", u, err)
		}
	}

	// --- FTP: vsftpd virtual users -------------------------------------
	ftpChanged := false
	if len(ftpList) > 0 || len(removedFTP) > 0 || virtualExisting != "" || len(existingPerUser) > 0 {
		if len(ftpList) > 0 {
			if err := e.ftpEnsurePackages(ctx); err != nil {
				return out, err
			}
		}
		for _, d := range []string{filepath.Dir(p.virtualUsers), p.userConfDir, filepath.Dir(p.pamFile), filepath.Dir(p.unitDropIn)} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return out, fmt.Errorf("create %s: %w", d, err)
			}
		}

		mainContent := ftpRenderVSFTPMainConf(p.userConfDir)
		if ftpReadFile(p.mainConf) != mainContent {
			var err error
			if _, lookErr := exec.LookPath("vsftpd"); lookErr == nil {
				err = SwapValidated(p.mainConf, []byte(mainContent), 0o644, func() error {
					return ftpCheckVSFTPDConf(ctx, "vsftpd", p.mainConf)
				})
			} else {
				err = AtomicWriteFile(p.mainConf, []byte(mainContent), 0o644)
			}
			if err != nil {
				return out, fmt.Errorf("vsftpd main config: %w", err)
			}
			ftpChanged = true
		}

		pamContent := ftpRenderPAM(p.virtualUsers)
		if changed, err := ftpWriteFileIfChanged(p.pamFile, pamContent, 0o644); err != nil {
			return out, fmt.Errorf("pam config: %w", err)
		} else if changed {
			ftpChanged = true
		}

		dropIn := ftpRenderUnitDropIn(p.mainConf)
		if changed, err := ftpWriteFileIfChanged(p.unitDropIn, dropIn, 0o644); err != nil {
			return out, fmt.Errorf("vsftpd unit drop-in: %w", err)
		} else if changed {
			ftpChanged = true
		}

		// Credential file: prefix-scoped rebuild (0600, root-only).
		virtualContent := ftpRenderVirtualUsers(virtualExisting, ftpList, prefix)
		if changed, err := ftpWriteFileIfChanged(p.virtualUsers, virtualContent, 0o600); err != nil {
			return out, fmt.Errorf("virtual users file: %w", err)
		} else if changed {
			ftpChanged = true
		}

		desiredPerUser := map[string]string{}
		for _, a := range ftpList {
			desiredPerUser[a.UserName] = ftpRenderPerUserConf(a)
		}
		for name, content := range desiredPerUser {
			changed, err := ftpWriteFileIfChanged(filepath.Join(p.userConfDir, name), content, 0o644)
			if err != nil {
				return out, fmt.Errorf("per-user config %s: %w", name, err)
			}
			if changed {
				ftpChanged = true
			}
		}
		for _, name := range removedFTP {
			if err := os.Remove(filepath.Join(p.userConfDir, name)); err != nil && !os.IsNotExist(err) {
				return out, fmt.Errorf("remove per-user config %s: %w", name, err)
			}
			ftpChanged = true
		}
	}

	// Reloads — skipped entirely when nothing changed (idempotent re-runs).
	if sshdChanged {
		if err := e.run(ctx, "systemctl", "reload", "sshd"); err != nil {
			if err2 := e.run(ctx, "systemctl", "reload", "ssh"); err2 != nil {
				return out, fmt.Errorf("reload sshd: %v (fallback: %v)", err, err2)
			}
		}
		out.Reloaded = true
	}
	if ftpChanged {
		if _, lookErr := exec.LookPath("vsftpd"); lookErr == nil {
			if err := e.run(ctx, "systemctl", "daemon-reload"); err != nil {
				return out, fmt.Errorf("daemon-reload: %w", err)
			}
			_ = e.run(ctx, "systemctl", "enable", "vsftpd")
			if err := e.run(ctx, "systemctl", "restart", "vsftpd"); err != nil {
				return out, fmt.Errorf("restart vsftpd: %w", err)
			}
			out.Reloaded = true
		}
	}

	slog.Info("ftp accounts synced", "website", websiteID, "sftp", out.SFTPUsers, "ftp", out.FTPUsers,
		"created", out.Created, "removed", out.Removed, "reloaded", out.Reloaded)
	return out, nil
}

// ============================================================================
// VALIDATION
// ============================================================================

// ftpValidatePayload checks the whole desired state up front: uuid site id,
// prefix-bound user names, ftp/sftp protocol, $6$ hashes and site-scoped
// home dirs. Nothing is executed before this passes.
func ftpValidatePayload(payload FTPSyncPayload, siteBase string) (string, error) {
	id, err := uuid.Parse(payload.WebsiteID)
	if err != nil {
		return "", fmt.Errorf("invalid website id: %w", err)
	}
	websiteID := id.String()
	seen := map[string]bool{}
	for _, a := range payload.Accounts {
		if err := ftpValidateAccount(a, websiteID, siteBase); err != nil {
			return "", err
		}
		if seen[a.UserName] {
			return "", fmt.Errorf("duplicate ftp account user name %q", a.UserName)
		}
		seen[a.UserName] = true
	}
	return websiteID, nil
}

func ftpValidateAccount(a FTPAccountSpec, websiteID, siteBase string) error {
	if !ftpUserRe.MatchString(a.UserName) || !validUnixUserName(a.UserName) {
		return fmt.Errorf("invalid ftp account user name %q", a.UserName)
	}
	if want := "ep-ftp-" + websiteID[:8] + "-"; !strings.HasPrefix(a.UserName, want) {
		return fmt.Errorf("ftp account %q does not belong to website %s", a.UserName, websiteID)
	}
	if a.Protocol != ftpProtoFTP && a.Protocol != ftpProtoSFTP {
		return fmt.Errorf("invalid ftp account protocol %q", a.Protocol)
	}
	if !ftpCryptRe.MatchString(a.PasswordCrypt) {
		return fmt.Errorf("ftp account %s: password_crypt must be a $6$ SHA-512-crypt hash", a.UserName)
	}
	if !ftpValidHomeDir(a.HomeDir, websiteID, siteBase) {
		return fmt.Errorf("ftp account %s: home_dir %q must be inside the website tree", a.UserName, a.HomeDir)
	}
	return nil
}

// ftpValidHomeDir requires an absolute path inside the site tree and rejects
// whitespace, quotes and control bytes: home dirs are rendered into sshd and
// vsftpd config lines, so any of those would smuggle config directives.
func ftpValidHomeDir(home, websiteID, siteBase string) bool {
	if home == "" {
		return false
	}
	clean := filepath.Clean(home)
	base := filepath.Join(siteBase, websiteID)
	if clean != base && !strings.HasPrefix(clean, base+string(filepath.Separator)) {
		return false
	}
	return !strings.ContainsFunc(clean, func(r rune) bool {
		return r < 0x21 || r == 0x7f || r == '"' || r == '\''
	})
}

// ============================================================================
// RENDERING (pure)
// ============================================================================

// ftpRenderSFTPConf merges the desired Match blocks into the drop-in content,
// preserving blocks of other websites (prefix filter) so per-site syncs never
// clobber each other. Empty result = the file must be removed.
func ftpRenderSFTPConf(existing string, desired []FTPAccountSpec, prefix string) string {
	var kept []string
	for _, b := range ftpSplitMatchBlocks(existing) {
		if b.user == "" || strings.HasPrefix(b.user, prefix) {
			continue
		}
		// Drop trailing blank lines so repeated syncs don't accumulate them.
		lines := b.lines
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		kept = append(kept, strings.Join(lines, "\n")+"\n")
	}
	if len(kept) == 0 && len(desired) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("# managed by EpicPanel — do not edit\n")
	out.WriteString("# SFTP-only accounts. ChrootDirectory is deliberately not used: the site tree\n")
	out.WriteString("# is owned by the site unix user and sshd requires a root-owned chroot path.\n")
	out.WriteString("# ForceCommand internal-sftp with a fixed start directory is the equivalent.\n")
	for _, body := range kept {
		out.WriteString(body)
	}
	for _, a := range desired {
		out.WriteString(ftpRenderMatchBlock(a))
	}
	return out.String()
}

func ftpRenderMatchBlock(a FTPAccountSpec) string {
	return "Match User " + a.UserName + "\n" +
		"    ForceCommand internal-sftp -d " + a.HomeDir + " -u 077\n" +
		"    AllowTcpForwarding no\n" +
		"    X11Forwarding no\n" +
		"    PermitTunnel no\n"
}

type ftpMatchBlock struct {
	user  string
	lines []string
}

func ftpSplitMatchBlocks(content string) []ftpMatchBlock {
	var blocks []ftpMatchBlock
	for _, line := range strings.Split(content, "\n") {
		if u, ok := strings.CutPrefix(line, "Match User "); ok {
			blocks = append(blocks, ftpMatchBlock{user: strings.TrimSpace(u)})
		}
		if len(blocks) == 0 {
			continue
		}
		b := &blocks[len(blocks)-1]
		b.lines = append(b.lines, line)
	}
	return blocks
}

func ftpParseMatchUsers(content string) []string {
	var out []string
	for _, b := range ftpSplitMatchBlocks(content) {
		if b.user != "" {
			out = append(out, b.user)
		}
	}
	return out
}

// ftpRenderVirtualUsers rebuilds the credential file: lines for this website's
// prefix are replaced by the desired set, foreign sites' lines survive.
func ftpRenderVirtualUsers(existing string, desired []FTPAccountSpec, prefix string) string {
	var out strings.Builder
	for _, line := range strings.Split(existing, "\n") {
		name, _, ok := strings.Cut(line, ":")
		if !ok || name == "" || strings.HasPrefix(name, prefix) || strings.HasPrefix(name, "#") {
			continue
		}
		out.WriteString(line + "\n")
	}
	for _, a := range desired {
		out.WriteString(a.UserName + ":" + a.PasswordCrypt + "\n")
	}
	return out.String()
}

func ftpParseVirtualUserNames(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		name, _, ok := strings.Cut(line, ":")
		if ok && name != "" && !strings.HasPrefix(name, "#") {
			out = append(out, name)
		}
	}
	return out
}

// ftpRenderPerUserConf pins a virtual user to its home dir. The hash never
// appears here (it lives only in the credential file).
func ftpRenderPerUserConf(a FTPAccountSpec) string {
	return "local_root=" + a.HomeDir + "\nwrite_enable=YES\n"
}

// ftpRenderVSFTPMainConf: virtual users authenticate via the epicpanel-vsftpd
// PAM stack and run as guest_username (nobody) — the site tree keeps its site
// owner. FTP is plaintext by nature; SFTP accounts are the recommended path.
func ftpRenderVSFTPMainConf(userConfDir string) string {
	return `# managed by EpicPanel — do not edit
# FTP is plaintext by nature; SFTP accounts (OpenSSH internal-sftp) are the
# recommended path. Virtual users authenticate via the epicpanel-vsftpd PAM
# stack and run as guest_username (nobody): the site tree keeps its site owner.
listen=YES
anonymous_enable=NO
local_enable=YES
guest_enable=YES
guest_username=nobody
pam_service_name=epicpanel-vsftpd
user_config_dir=` + userConfDir + `
chroot_local_user=NO
pasv_min_port=50000
pasv_max_port=50100
hide_ids=YES
ssl_enable=NO
write_enable=YES
`
}

// ftpRenderPAM renders the vsftpd PAM stack against our credential file.
// pam_pwdfile implements the auth phase only; the account phase must be
// pam_permit or every login fails with PAM_MODULE_UNKNOWN.
func ftpRenderPAM(pwdfile string) string {
	return "#%PAM-1.0\n" +
		"auth required pam_pwdfile.so pwdfile " + pwdfile + "\n" +
		"account required pam_permit.so\n"
}

// ftpRenderUnitDropIn overrides the distro ExecStart (which points at
// /etc/vsftpd.conf) so the service runs our managed config instead.
func ftpRenderUnitDropIn(mainConf string) string {
	return "[Service]\nExecStart=\nExecStart=/usr/sbin/vsftpd " + mainConf + "\n"
}

// ============================================================================
// DIFF (pure)
// ============================================================================

// ftpRemovals returns managed names present in existing but absent from the
// desired set, deduplicated. The site prefix guard means names outside this
// website's prefix are never removed.
func ftpRemovals(existing []string, desired []FTPAccountSpec, prefix string) []string {
	want := make(map[string]bool, len(desired))
	for _, a := range desired {
		want[a.UserName] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, u := range existing {
		if !want[u] && strings.HasPrefix(u, prefix) && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

func ftpExistingPerUser(dir, prefix string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && ftpUserRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// ============================================================================
// EXEC HELPERS
// ============================================================================

// ftpEnsureSFTPUser creates the system user (nologin shell) when missing,
// applies the one-way hash via usermod -p (argv — a validated crypt hash, not
// plaintext) and creates the home dir. Idempotent: unchanged hash is skipped.
func (e *Executor) ftpEnsureSFTPUser(ctx context.Context, a FTPAccountSpec) (bool, error) {
	_, lookupErr := user.Lookup(a.UserName)
	isNew := lookupErr != nil
	uid, gid, err := e.ensureUnixUser(a.UserName)
	if err != nil {
		return isNew, fmt.Errorf("ensure sftp user %s: %w", a.UserName, err)
	}
	if cur := ftpShadowHash(a.UserName); cur != a.PasswordCrypt {
		if err := e.run(ctx, "usermod", "-p", a.PasswordCrypt, a.UserName); err != nil {
			return isNew, fmt.Errorf("set password for %s: %w", a.UserName, err)
		}
	}
	if err := ftpEnsureHome(a.HomeDir, uid, gid); err != nil {
		return isNew, fmt.Errorf("sftp home %s: %w", a.HomeDir, err)
	}
	return isNew, nil
}

// ftpShadowHash reads the user's current crypt hash from /etc/shadow (the
// agent runs as root) so unchanged passwords skip the usermod call.
func ftpShadowHash(name string) string {
	b, err := os.ReadFile("/etc/shadow")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.SplitN(line, ":", 3)
		if len(f) >= 2 && f[0] == name {
			return f[1]
		}
	}
	return ""
}

// ftpEnsureHome creates the home dir when missing and hands it to the sftp
// user. An existing directory keeps its owner: the home may be the site base
// (empty home_subdir) and taking it from the site user would break the site.
func ftpEnsureHome(home string, uid, gid int) error {
	if _, err := os.Stat(home); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(home, 0o750); err != nil {
		return err
	}
	return os.Chown(home, uid, gid)
}

// ftpEnsurePackages installs vsftpd + libpam-pwdfile when FTP accounts must be
// provisioned. A missing binary after the install attempt fails the job
// (retryable) rather than shipping a broken service.
func (e *Executor) ftpEnsurePackages(ctx context.Context) error {
	if _, err := exec.LookPath("vsftpd"); err == nil && dpkgInstalled("libpam-pwdfile") {
		return nil
	}
	if err := e.aptInstall(ctx, []string{"vsftpd", "libpam-pwdfile"}, "", "vsftpd"); err != nil {
		return fmt.Errorf("install vsftpd: %w", err)
	}
	if _, err := exec.LookPath("vsftpd"); err != nil {
		return fmt.Errorf("vsftpd still missing after install attempt")
	}
	return nil
}

// ftpCheckVSFTPDConf parses the config through vsftpd in inetd mode (listen
// disabled) as a syntax check. vsftpd has no dedicated test flag and exit
// codes vary between builds, so only an explicit parse error ("500 OOPS")
// fails the check; unsupported-override or inetd-mode exits count as pass.
func ftpCheckVSFTPDConf(ctx context.Context, bin, conf string) error {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(c, bin, "-olisten=NO", conf).CombinedOutput()
	if i := strings.Index(string(out), "500 OOPS"); i >= 0 {
		return fmt.Errorf("vsftpd rejected %s: %s", conf, tail(out, 300))
	}
	return nil
}

// ftpReadFile returns content or "" when missing — empty and absent are
// equivalent for the managed-content comparisons.
func ftpReadFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// ftpWriteFileIfChanged writes content atomically when it differs from the
// file's current content; reports whether a write happened.
func ftpWriteFileIfChanged(path, content string, perm os.FileMode) (bool, error) {
	if ftpReadFile(path) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, AtomicWriteFile(path, []byte(content), perm)
}

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ftpTestWebsiteID = "11111111-2222-3333-4444-555555555555"

const ftpTestHashA = "$6$salt$aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const ftpTestHashB = "$6$salt$bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func ftpTestPayload() FTPSyncPayload {
	return FTPSyncPayload{
		WebsiteID: ftpTestWebsiteID,
		Accounts: []FTPAccountSpec{
			{ID: "aaaaaaaa-0000-0000-0000-000000000001", UserName: "ep-ftp-11111111-web", Protocol: "sftp", PasswordCrypt: ftpTestHashA, HomeDir: "/srv/epicpanel/websites/" + ftpTestWebsiteID + "/public"},
			{ID: "aaaaaaaa-0000-0000-0000-000000000002", UserName: "ep-ftp-11111111-ftp", Protocol: "ftp", PasswordCrypt: ftpTestHashB, HomeDir: "/srv/epicpanel/websites/" + ftpTestWebsiteID + "/uploads"},
		},
	}
}

// ============================================================================
// Payload validation matrix.
// ============================================================================

func TestFTPValidatePayload(t *testing.T) {
	siteBase := "/srv/epicpanel/websites"
	base := siteBase + "/" + ftpTestWebsiteID

	cases := []struct {
		name    string
		mutate  func(*FTPSyncPayload)
		wantErr bool
	}{
		{"valid", func(p *FTPSyncPayload) {}, false},
		{"zero accounts is valid", func(p *FTPSyncPayload) { p.Accounts = nil }, false},
		{"bad uuid", func(p *FTPSyncPayload) { p.WebsiteID = "not-a-uuid" }, true},
		{"empty uuid", func(p *FTPSyncPayload) { p.WebsiteID = "" }, true},
		{"uppercase username", func(p *FTPSyncPayload) { p.Accounts[0].UserName = "ep-ftp-11111111-Web" }, true},
		{"underscore username", func(p *FTPSyncPayload) { p.Accounts[0].UserName = "ep-ftp-11111111-web_1" }, true},
		{"too long username", func(p *FTPSyncPayload) { p.Accounts[0].UserName = "ep-ftp-" + strings.Repeat("a", 26) }, true},
		{"wrong site prefix", func(p *FTPSyncPayload) { p.Accounts[0].UserName = "ep-ftp-99999999-web" }, true},
		{"missing ep-ftp prefix", func(p *FTPSyncPayload) { p.Accounts[0].UserName = "webmaster" }, true},
		{"bad protocol", func(p *FTPSyncPayload) { p.Accounts[0].Protocol = "scp" }, true},
		{"empty protocol", func(p *FTPSyncPayload) { p.Accounts[0].Protocol = "" }, true},
		{"md5 hash", func(p *FTPSyncPayload) { p.Accounts[0].PasswordCrypt = "$1$salt$abc" }, true},
		{"plaintext password", func(p *FTPSyncPayload) { p.Accounts[0].PasswordCrypt = "hunter2" }, true},
		{"empty hash", func(p *FTPSyncPayload) { p.Accounts[0].PasswordCrypt = "" }, true},
		{"hash with whitespace", func(p *FTPSyncPayload) { p.Accounts[0].PasswordCrypt = "$6$salt$abc def" }, true},
		{"other site home", func(p *FTPSyncPayload) {
			p.Accounts[0].HomeDir = siteBase + "/99999999-2222-3333-4444-555555555555/public"
		}, true},
		{"home traversal", func(p *FTPSyncPayload) { p.Accounts[0].HomeDir = base + "/public/../../etc" }, true},
		{"home sibling escape", func(p *FTPSyncPayload) { p.Accounts[0].HomeDir = base + "/../" + ftpTestWebsiteID + "x/y" }, true},
		{"home outside tree", func(p *FTPSyncPayload) { p.Accounts[0].HomeDir = "/etc/passwd" }, true},
		{"home relative", func(p *FTPSyncPayload) { p.Accounts[0].HomeDir = "public" }, true},
		{"home empty", func(p *FTPSyncPayload) { p.Accounts[0].HomeDir = "" }, true},
		{"home with space", func(p *FTPSyncPayload) { p.Accounts[0].HomeDir = base + "/my dir" }, true},
		{"home newline injection", func(p *FTPSyncPayload) { p.Accounts[0].HomeDir = base + "/x\nMatch User evil" }, true},
		{"duplicate usernames", func(p *FTPSyncPayload) { p.Accounts[1].UserName = p.Accounts[0].UserName }, true},
		{"home at site root", func(p *FTPSyncPayload) { p.Accounts[0].HomeDir = base }, false},
		{"home trailing slash cleaned", func(p *FTPSyncPayload) { p.Accounts[0].HomeDir = base + "/public/" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := ftpTestPayload()
			tc.mutate(&payload)
			_, err := ftpValidatePayload(payload, siteBase)
			if tc.wantErr != (err != nil) {
				t.Fatalf("wantErr=%v, got err=%v", tc.wantErr, err)
			}
		})
	}
}

// ============================================================================
// Rendering.
// ============================================================================

func TestFTPRenderPerUserConf(t *testing.T) {
	a := FTPAccountSpec{UserName: "ep-ftp-11111111-ftp", Protocol: "ftp", PasswordCrypt: ftpTestHashB, HomeDir: "/srv/epicpanel/websites/x/uploads"}
	got := ftpRenderPerUserConf(a)
	want := "local_root=/srv/epicpanel/websites/x/uploads\nwrite_enable=YES\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "$6$") {
		t.Error("hash must never appear in the per-user vsftpd config")
	}
}

func TestFTPRenderVirtualUsers(t *testing.T) {
	existing := "ep-ftp-99999999-keep:$6$foreign$\nep-ftp-11111111-old:" + ftpTestHashA + "\n"
	desired := []FTPAccountSpec{{UserName: "ep-ftp-11111111-ftp", Protocol: "ftp", PasswordCrypt: ftpTestHashB}}
	got := ftpRenderVirtualUsers(existing, desired, "ep-ftp-11111111-")
	want := "ep-ftp-99999999-keep:$6$foreign$\nep-ftp-11111111-ftp:" + ftpTestHashB + "\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := ftpRenderVirtualUsers("", nil, "ep-ftp-11111111-"); got != "" {
		t.Fatalf("empty desired + empty existing = %q, want \"\"", got)
	}
	// Comment lines are dropped, not carried over as credentials.
	if got := ftpRenderVirtualUsers("# comment\nep-ftp-99999999-k:$6$x\n", nil, "ep-ftp-11111111-"); got != "ep-ftp-99999999-k:$6$x\n" {
		t.Fatalf("comment handling: %q", got)
	}
}

func TestFTPRenderSFTPConf(t *testing.T) {
	prefix := "ep-ftp-11111111-"
	foreign := "Match User ep-ftp-99999999-other\n    ForceCommand internal-sftp -d /srv/other -u 077\n    AllowTcpForwarding no\n"
	existing := "# managed by EpicPanel — do not edit\n" +
		"Match User ep-ftp-11111111-gone\n    ForceCommand internal-sftp -d /srv/gone -u 077\n\n" +
		foreign
	desired := []FTPAccountSpec{{UserName: "ep-ftp-11111111-web", Protocol: "sftp", PasswordCrypt: ftpTestHashA, HomeDir: "/srv/epicpanel/websites/" + ftpTestWebsiteID + "/public"}}

	got := ftpRenderSFTPConf(existing, desired, prefix)
	want := `# managed by EpicPanel — do not edit
# SFTP-only accounts. ChrootDirectory is deliberately not used: the site tree
# is owned by the site unix user and sshd requires a root-owned chroot path.
# ForceCommand internal-sftp with a fixed start directory is the equivalent.
Match User ep-ftp-99999999-other
    ForceCommand internal-sftp -d /srv/other -u 077
    AllowTcpForwarding no
Match User ep-ftp-11111111-web
    ForceCommand internal-sftp -d /srv/epicpanel/websites/11111111-2222-3333-4444-555555555555/public -u 077
    AllowTcpForwarding no
    X11Forwarding no
    PermitTunnel no
`
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	if strings.Contains(got, "ep-ftp-11111111-gone") {
		t.Error("stale same-site account must be dropped")
	}
	if strings.Contains(got, ftpTestHashA) || strings.Contains(got, "password") {
		t.Error("hash or credentials must never appear in the sshd config")
	}
	if got := ftpRenderSFTPConf(existing, nil, prefix); !strings.Contains(got, "ep-ftp-99999999-other") || strings.Contains(got, "11111111") {
		t.Fatalf("removal must keep foreign blocks only:\n%s", got)
	}
	if got := ftpRenderSFTPConf("", nil, prefix); got != "" {
		t.Fatalf("nothing managed = empty content (file removed), got %q", got)
	}
}

func TestFTPRenderVSFTPMainConf(t *testing.T) {
	got := ftpRenderVSFTPMainConf("/etc/vsftpd/epicpanel/users")
	for _, want := range []string{
		"listen=YES", "anonymous_enable=NO", "guest_enable=YES", "guest_username=nobody",
		"pam_service_name=epicpanel-vsftpd", "user_config_dir=/etc/vsftpd/epicpanel/users",
		"chroot_local_user=NO", "pasv_min_port=50000", "pasv_max_port=50100",
		"hide_ids=YES", "ssl_enable=NO", "write_enable=YES",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("main conf missing %q", want)
		}
	}
	if !strings.Contains(got, "plaintext") {
		t.Error("plaintext-by-nature warning comment missing")
	}
}

func TestFTPRenderPAMAndDropIn(t *testing.T) {
	pam := ftpRenderPAM("/etc/vsftpd/epicpanel/virtual-users.txt")
	wantPam := "#%PAM-1.0\nauth required pam_pwdfile.so pwdfile /etc/vsftpd/epicpanel/virtual-users.txt\naccount required pam_permit.so\n"
	if pam != wantPam {
		t.Fatalf("pam = %q, want %q", pam, wantPam)
	}
	dropIn := ftpRenderUnitDropIn("/etc/vsftpd/epicpanel/vsftpd.conf")
	wantDrop := "[Service]\nExecStart=\nExecStart=/usr/sbin/vsftpd /etc/vsftpd/epicpanel/vsftpd.conf\n"
	if dropIn != wantDrop {
		t.Fatalf("drop-in = %q, want %q", dropIn, wantDrop)
	}
}

// ============================================================================
// Diff.
// ============================================================================

func TestFTPRemovals(t *testing.T) {
	prefix := "ep-ftp-11111111-"
	existing := []string{"ep-ftp-11111111-a", "ep-ftp-11111111-b", "ep-ftp-11111111-b", "ep-ftp-99999999-foreign", "ep-sftp-notmanaged"}
	desired := []FTPAccountSpec{{UserName: "ep-ftp-11111111-a"}}
	got := ftpRemovals(existing, desired, prefix)
	want := []string{"ep-ftp-11111111-b"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %v, want %v", got, want)
	}
	all := ftpRemovals(existing, nil, prefix)
	if len(all) != 2 {
		t.Fatalf("empty desired must remove all own-prefix accounts, got %v", all)
	}
}

func TestFTPParseMatchUsers(t *testing.T) {
	content := "Match User ep-ftp-11111111-a\n    ForceCommand internal-sftp\nMatch User ep-ftp-99999999-b\n    AllowTcpForwarding no\n"
	got := ftpParseMatchUsers(content)
	want := []string{"ep-ftp-11111111-a", "ep-ftp-99999999-b"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// Removal accounting against previous state files in a temp dir (no exec).
func TestFTPRemovalFromStateFiles(t *testing.T) {
	dir := t.TempDir()
	p := ftpPaths{
		sshdDropIn:   filepath.Join(dir, "ssh", "sshd_config.d", "epicpanel-ftp.conf"),
		virtualUsers: filepath.Join(dir, "vsftpd", "virtual-users.txt"),
		userConfDir:  filepath.Join(dir, "vsftpd", "users"),
	}
	prefix := "ep-ftp-11111111-"

	prevSSHD := ftpRenderSFTPConf("", []FTPAccountSpec{
		{UserName: "ep-ftp-11111111-web", Protocol: "sftp", HomeDir: "/srv/x"},
		{UserName: "ep-ftp-11111111-gonessh", Protocol: "sftp", HomeDir: "/srv/y"},
	}, prefix)
	if err := os.MkdirAll(filepath.Dir(p.sshdDropIn), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.sshdDropIn, []byte(prevSSHD), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.userConfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.virtualUsers, []byte("ep-ftp-11111111-ftp:"+ftpTestHashB+"\nep-ftp-11111111-gone:"+ftpTestHashA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.userConfDir, "ep-ftp-11111111-gone"), []byte("local_root=/srv/gone\nwrite_enable=YES\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	desiredFTP := []FTPAccountSpec{{UserName: "ep-ftp-11111111-ftp", Protocol: "ftp", PasswordCrypt: ftpTestHashB, HomeDir: "/srv/keep"}}
	removedFTP := ftpRemovals(append(ftpParseVirtualUserNames(ftpReadFile(p.virtualUsers)), ftpExistingPerUser(p.userConfDir, prefix)...), desiredFTP, prefix)
	if len(removedFTP) != 1 || removedFTP[0] != "ep-ftp-11111111-gone" {
		t.Fatalf("removedFTP = %v, want [ep-ftp-11111111-gone]", removedFTP)
	}

	desiredSFTP := []FTPAccountSpec{{UserName: "ep-ftp-11111111-web", Protocol: "sftp", HomeDir: "/srv/x"}}
	removedSFTP := ftpRemovals(ftpParseMatchUsers(ftpReadFile(p.sshdDropIn)), desiredSFTP, prefix)
	if len(removedSFTP) != 1 || removedSFTP[0] != "ep-ftp-11111111-gonessh" {
		t.Fatalf("removedSFTP = %v, want [ep-ftp-11111111-gonessh]", removedSFTP)
	}

	// Rebuilding with the desired set drops the stale credential line and
	// keeps foreign sites intact.
	rebuilt := ftpRenderVirtualUsers(ftpReadFile(p.virtualUsers), desiredFTP, prefix)
	if strings.Contains(rebuilt, "ep-ftp-11111111-gone:") || !strings.Contains(rebuilt, "ep-ftp-11111111-ftp:"+ftpTestHashB) {
		t.Fatalf("rebuilt = %q", rebuilt)
	}
}

// ============================================================================
// Credential file write: mode 0600 + idempotent no-op on unchanged content.
// ============================================================================

func TestFTPWriteFileIfChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "vsftpd", "virtual-users.txt")
	content := "ep-ftp-11111111-ftp:" + ftpTestHashB + "\n"

	changed, err := ftpWriteFileIfChanged(path, content, 0o600)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %v, want 0600", fi.Mode().Perm())
	}

	changed, err = ftpWriteFileIfChanged(path, content, 0o600)
	if err != nil || changed {
		t.Fatalf("unchanged rewrite must be a no-op: changed=%v err=%v", changed, err)
	}

	newContent := "ep-ftp-11111111-ftp2:" + ftpTestHashA + "\n"
	changed, err = ftpWriteFileIfChanged(path, newContent, 0o600)
	if err != nil || !changed {
		t.Fatalf("changed rewrite: changed=%v err=%v", changed, err)
	}
	if b, _ := os.ReadFile(path); string(b) != newContent {
		t.Fatalf("content = %q, want %q", b, newContent)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode after rewrite = %v, want 0600", fi.Mode().Perm())
	}
}

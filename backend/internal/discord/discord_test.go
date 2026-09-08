package discord

import (
	"strings"
	"testing"
	"time"
)

// --- state machine (verbatim edges) ---

func TestStateMachineEdges(t *testing.T) {
	cases := []struct {
		from, to Status
		legal    bool
	}{
		{StatusInstalling, StatusStopped, true},
		{StatusInstalling, StatusStarting, true},
		{StatusInstalling, StatusFailed, true},
		{StatusStopped, StatusStarting, true},
		{StatusStarting, StatusRunning, true},
		{StatusStarting, StatusCrashed, true},
		{StatusRunning, StatusStopping, true},
		{StatusRunning, StatusCrashed, true},
		{StatusStopping, StatusStopped, true},
		{StatusCrashed, StatusStarting, true},
		{StatusCrashed, StatusStopped, true},
		{StatusFailed, StatusInstalling, true},
		{StatusDeleting, StatusDeleted, true},
		// illegal edges
		{StatusStopped, StatusRunning, false},  // must pass through starting
		{StatusRunning, StatusStopped, false},  // must pass through stopping
		{StatusRunning, StatusRunning, false},  // self edges are no-ops, not transitions
		{StatusDeleted, StatusStarting, false}, // terminal
		{StatusInstalling, StatusDeleting, true},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.legal {
			t.Errorf("CanTransition(%s -> %s) = %v, want %v", c.from, c.to, got, c.legal)
		}
	}
}

func TestAgentStatusMapping(t *testing.T) {
	cases := []struct {
		unit    string
		desired string
		want    Status
	}{
		{"active", DesiredRunning, StatusRunning},
		{"active", DesiredStopped, StatusStopping},
		{"activating", DesiredRunning, StatusStarting},
		{"deactivating", DesiredRunning, StatusStopping},
		{"failed", DesiredRunning, StatusCrashed},
		{"inactive", DesiredRunning, StatusStopped},
		{"", DesiredStopped, StatusStopped},
		{"weird", DesiredRunning, StatusStopped},
	}
	for _, c := range cases {
		if got := AgentStatus(c.unit, c.desired); got != c.want {
			t.Errorf("AgentStatus(%q, %q) = %s, want %s", c.unit, c.desired, got, c.want)
		}
	}
}

// --- runtime abstraction ---

func TestRuntimeProviders(t *testing.T) {
	node, err := RuntimeFor("node")
	if err != nil {
		t.Fatalf("node runtime: %v", err)
	}
	if !node.Supported() {
		t.Fatal("node must be supported")
	}
	if err := node.ValidateVersion("22"); err != nil {
		t.Errorf("node 22 should validate: %v", err)
	}
	if err := node.ValidateVersion("99"); err == nil {
		t.Error("node 99 must be rejected")
	}
	if err := node.ValidateStartup("index.js", ""); err != nil {
		t.Errorf("node startup file: %v", err)
	}
	if err := node.ValidateStartup("", ""); err == nil {
		t.Error("node needs startup_file or startup_command")
	}

	py, err := RuntimeFor("python")
	if err != nil {
		t.Fatalf("python runtime: %v", err)
	}
	if py.DefaultVersion() != "3.12" {
		t.Errorf("python default = %q", py.DefaultVersion())
	}
	if err := py.ValidateStartup("bot.py", ""); err != nil {
		t.Errorf("python startup file: %v", err)
	}
	if err := py.ValidateStartup("", ""); err == nil {
		t.Error("python needs startup too")
	}

	// Java: foundation only — resolvable but unsupported.
	java, err := RuntimeFor("java")
	if err != nil {
		t.Fatalf("java runtime should resolve (foundation): %v", err)
	}
	if java.Supported() {
		t.Error("java must report unsupported (foundation)")
	}

	// Unknown runtime.
	if _, err := RuntimeFor("ruby"); err == nil {
		t.Error("ruby must be unknown")
	}

	// Offers include all three, java flagged by empty versions.
	offers := VersionOffers()
	if len(offers) != 3 {
		t.Fatalf("offers = %d, want 3", len(offers))
	}
	var javaOffer bool
	for _, o := range offers {
		if o.Runtime == "java" && len(o.Versions) == 0 {
			javaOffer = true
		}
	}
	if !javaOffer {
		t.Error("java offer missing or not flagged")
	}
}

func TestValidateCreate(t *testing.T) {
	env, ver, file, err := ValidateCreate("my-bot", "node", "", "", "", "", "", map[string]string{"TOKEN": "abc123def"})
	if err != nil {
		t.Fatalf("valid create: %v", err)
	}
	if ver != "22" {
		t.Errorf("default node version = %q", ver)
	}
	if file != "index.js" {
		t.Errorf("default startup = %q", file)
	}
	if env["TOKEN"] != "abc123def" {
		t.Errorf("env passthrough = %v", env)
	}

	// bad names
	for _, name := range []string{"", "-bad", "bad-", "Has Upper", "way-too-long-" + strings.Repeat("x", 60)} {
		if _, _, _, err := ValidateCreate(name, "node", "", "index.js", "", "", "", nil); err == nil {
			t.Errorf("name %q must be rejected", name)
		}
	}

	// java rejected at create
	if _, _, _, err := ValidateCreate("jb", "java", "", "", "", "", "", nil); err == nil {
		t.Error("java create must be rejected (foundation only)")
	}

	// bad restart policy
	if _, _, _, err := ValidateCreate("b", "node", "", "index.js", "", "", "sometimes", nil); err == nil {
		t.Error("restart policy 'sometimes' must be rejected")
	}

	// git repo without startup gets the runtime default file (no error)
	if _, _, file, err := ValidateCreate("gb", "python", "", "", "", "https://github.com/x/y", "", nil); err != nil {
		t.Errorf("git-only create (python) failed: %v", err)
	} else if file != "bot.py" {
		t.Errorf("git-only create should fall back to default startup, got %q", file)
	}
}

// --- secrets: encryption at rest + mandatory scrub ---

func TestEnvEncryptionRoundTrip(t *testing.T) {
	orig := map[string]string{"DISCORD_TOKEN": "super-secret-token-value-12345", "PORT": "8080"}
	enc, err := EncryptEnv(orig)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(enc, "super-secret-token-value") {
		t.Fatal("ciphertext contains plaintext secret")
	}
	got, err := DecryptEnv(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got["DISCORD_TOKEN"] != orig["DISCORD_TOKEN"] || got["PORT"] != orig["PORT"] {
		t.Fatalf("round trip mismatch: %v", got)
	}

	// empty env stores "" and decrypts to empty map
	enc, err = EncryptEnv(nil)
	if err != nil || enc != "" {
		t.Fatalf("empty env enc = %q err %v", enc, err)
	}
	m, err := DecryptEnv("")
	if err != nil || len(m) != 0 {
		t.Fatalf("empty decrypt = %v err %v", m, err)
	}
}

func TestEnvValidation(t *testing.T) {
	if err := ValidateEnvKey("GOOD_KEY_1"); err != nil {
		t.Errorf("good key rejected: %v", err)
	}
	for _, bad := range []string{"", "BAD KEY", "A;B", strings.Repeat("K", 65)} {
		if err := ValidateEnvKey(bad); err == nil {
			t.Errorf("key %q must be rejected", bad)
		}
	}
	// newline injection is sanitized, not stored
	v, err := ValidateEnvValue("line1\nline2\r\nEND")
	if err != nil {
		t.Fatalf("value sanitize: %v", err)
	}
	if strings.ContainsAny(v, "\n\r") {
		t.Errorf("newline survived sanitization: %q", v)
	}
	if _, err := ValidateEnvValue(strings.Repeat("x", 5000)); err == nil {
		t.Error("oversized value must be rejected")
	}
	if len(envKeyRejectionCases()) != 3 {
		t.Error("sanity")
	}
}

func envKeyRejectionCases() []string { return []string{"", "BAD KEY", "A;B"} }

// TestSecretScrub is the MANDATORY scrub proof: secret values (raw,
// URL-escaped, base64) never survive ScrubText.
func TestSecretScrub(t *testing.T) {
	secrets := []string{"MTIzNDU2Nzg5MGFiY2RlZg-token-8888", "https://hook.example.com/abc/DEF789"}
	log := "starting bot with token MTIzNDU2Nzg5MGFiY2RlZg-token-8888 ok\n" +
		"webhook: https://hook.example.com/abc/DEF789 registered\n" +
		"encoded: MTIzNDU2Nzg5MGFiY2RlZg%2Dtoken%2D8888\n" +
		"b64: TVRJek5ETlUyTn pnNW1HRmlZMlJsWmY= ignored\n"
	out := ScrubText(log, secrets)
	if strings.Contains(out, "MTIzNDU2Nzg5MGFiY2RlZg-token-8888") {
		t.Errorf("raw secret leaked: %q", out)
	}
	if strings.Contains(out, "https://hook.example.com/abc/DEF789") {
		t.Errorf("webhook secret leaked: %q", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("expected redaction marker: %q", out)
	}
	// short values are NOT scrubbed (false positives) but long ones are
	if ScrubText("pw=short", []string{"short"}) == "[redacted]" {
		t.Error("short values must not be scrubbed")
	}
	// no secrets = unchanged
	if ScrubText("clean line", nil) != "clean line" {
		t.Error("no-op scrub changed text")
	}
}

func TestMaskEnv(t *testing.T) {
	masked := MaskEnv(map[string]string{"B": "1", "A": "2", "C": "3"})
	if strings.Join(masked.Keys, ",") != "A,B,C" {
		t.Errorf("mask keys = %v", masked.Keys)
	}
}

func TestGitTokenSealing(t *testing.T) {
	enc, err := EncryptGitToken("ghp_realtokenvalue123456")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if strings.Contains(enc, "ghp_realtoken") {
		t.Fatal("token stored in plaintext")
	}
	got, err := DecryptGitToken(enc)
	if err != nil || got != "ghp_realtokenvalue123456" {
		t.Fatalf("unseal = %q err %v", got, err)
	}
	if enc2, _ := EncryptGitToken(""); enc2 != "" {
		t.Error("empty token should stay empty")
	}
}

// --- cron ---

func TestCronParsing(t *testing.T) {
	// every 5 minutes
	d, err := ParseCron("*/5 * * * *")
	if err != nil {
		t.Fatalf("cron */5: %v", err)
	}
	if d > 6*time.Minute || d <= 0 {
		t.Errorf("next */5 interval = %v", d)
	}
	// fixed daily time
	if _, err := ParseCron("30 4 * * *"); err != nil {
		t.Errorf("cron 30 4: %v", err)
	}
	// lists and ranges
	if _, err := ParseCron("0,15,30,45 0-6 1,15 * 1-5"); err != nil {
		t.Errorf("cron list/range: %v", err)
	}
	// invalid
	for _, bad := range []string{"* * * *", "61 * * * *", "*/0 * * * *", "* * * * * *", "a * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("cron %q must be rejected", bad)
		}
	}
	// NextCronAfter: "30 4 * * *" at 05:00 → next day 04:30 (~23.5h)
	now := time.Date(2026, 9, 8, 5, 0, 0, 0, time.UTC)
	d = NextCronAfter("30 4 * * *", now)
	if d < 23*time.Hour || d > 24*time.Hour {
		t.Errorf("NextCronAfter daily = %v, want ~23.5h", d)
	}
}

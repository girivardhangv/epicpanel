package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epicbyte/epicpanel/backend/internal/discord"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// --- unit naming + user derivation ---

func TestBotUnitName(t *testing.T) {
	id := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	if got := BotUnitName(id); got != "epicpanel-app-"+id {
		t.Errorf("BotUnitName = %q", got)
	}
	if BotUnitName("not-a-uuid") != "" {
		t.Error("invalid id must yield empty unit name")
	}
	u, err := botUserFor(id)
	if err != nil {
		t.Fatalf("botUserFor: %v", err)
	}
	if u != "ep-bot-6ba7b810" {
		t.Errorf("botUserFor = %q", u)
	}
	if _, err := botUserFor("nope"); err == nil {
		t.Error("invalid id must error")
	}
}

func TestRestartPolicyFor(t *testing.T) {
	cases := map[string]string{
		"always": "always", "no": "no", "": "on-failure",
		"on-failure": "on-failure", "injected; rm -rf": "on-failure",
	}
	for in, want := range cases {
		if got := restartPolicyFor(in); got != want {
			t.Errorf("restartPolicyFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- path safety for uploads (no traversal, no symlink escape) ---

func TestSafeBotPath(t *testing.T) {
	if _, err := safeBotPath("/srv/x", "../../etc/passwd"); err == nil {
		t.Error("traversal must be rejected")
	}
	if _, err := safeBotPath("/srv/x", "/absolute"); err == nil {
		t.Error("absolute path must be rejected")
	}
	if _, err := safeBotPath("/srv/x", ""); err == nil {
		t.Error("empty path must be rejected")
	}
	// good relative path resolves under root
	got, err := safeBotPath("/srv/x", "src/index.js")
	if err != nil || got != filepath.Join("/srv/x", "src/index.js") {
		t.Errorf("safe path = %q err %v", got, err)
	}

	// symlink escape: a symlinked directory inside the tree pointing out
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := safeBotPath(root, "link/evil.txt"); err == nil {
		t.Error("symlink escape must be rejected")
	}
}

func TestIsSecretishPath(t *testing.T) {
	for _, p := range []string{".env", "prod.env", "server.key", "cert.pem"} {
		if !isSecretishPath(p) {
			t.Errorf("%q should be secretish", p)
		}
	}
	if isSecretishPath("index.js") {
		t.Error("index.js is not secretish")
	}
}

// --- encrypted env plumbing (agent side) ---

func TestDecryptEnvEnc(t *testing.T) {
	vars := map[string]string{"DISCORD_TOKEN": "agent-side-secret-987654321"}
	plain, err := json.Marshal(vars)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := secretbox.Encrypt(string(plain))
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString(ct)
	got, err := decryptEnvEnc(enc)
	if err != nil {
		t.Fatalf("decryptEnvEnc: %v", err)
	}
	if got["DISCORD_TOKEN"] != vars["DISCORD_TOKEN"] {
		t.Fatalf("round trip = %v", got)
	}
	// empty and garbage
	if _, err := decryptEnvEnc(""); err != nil {
		t.Errorf("empty should be ok: %v", err)
	}
	if _, err := decryptEnvEnc(base64.StdEncoding.EncodeToString([]byte("garbage"))); err == nil {
		t.Error("garbage must fail")
	}
}

// TestScrubLogNoPlaintextInOutcome: the install outcome log (stored in the
// job result, listable via the API) must never contain a secret value.
func TestScrubLogNoPlaintextInOutcome(t *testing.T) {
	vars := map[string]string{"DISCORD_TOKEN": "plaintext-token-abcdef-12345"}
	plain, _ := json.Marshal(vars)
	ct, _ := secretbox.Encrypt(string(plain))
	envEnc := base64.StdEncoding.EncodeToString(ct)

	log := "npm install done\ntoken=plaintext-token-abcdef-12345 loaded\nready"
	out := scrubLog(log, envEnc)
	if strings.Contains(out, "plaintext-token-abcdef-12345") {
		t.Fatalf("secret leaked into outcome log: %q", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("expected redaction marker: %q", out)
	}
}

// --- console ring: scrub-on-ingest + seq dedup for job retries ---

func TestBotConsoleRingScrubAndDedup(t *testing.T) {
	botID := "11111111-2222-3333-4444-555555555555"
	defer dropBotSpec(botID)

	vars := map[string]string{"DISCORD_TOKEN": "ring-secret-token-7777777"}
	plain, _ := json.Marshal(vars)
	ct, _ := secretbox.Encrypt(string(plain))
	rememberBotSpec(botID, base64.StdEncoding.EncodeToString(ct), "")

	first, last := appendBotLines(botID, []string{
		"bot booting",
		"logged in as token ring-secret-token-7777777",
	}, botSecrets(botID))
	if first == 0 || last != first+1 {
		t.Fatalf("seqs = %d..%d", first, last)
	}

	r := ringFor(botID)
	r.mu.Lock()
	texts := make([]string, len(r.lines))
	for i, l := range r.lines {
		texts[i] = l.Text
	}
	r.mu.Unlock()
	joined := strings.Join(texts, "\n")
	if strings.Contains(joined, "ring-secret-token-7777777") {
		t.Fatalf("secret leaked through ring: %q", joined)
	}
	if !strings.Contains(joined, "[redacted]") {
		t.Errorf("expected redaction: %q", joined)
	}

	// tail from cursor: only newer lines returned
	lines, next, err := botConsoleLines(context.Background(), BotUnitName(botID), botID, 200, last, nil)
	if err != nil {
		t.Fatalf("console lines: %v", err)
	}
	if len(lines) != 0 || next != last {
		t.Errorf("tail after cursor = %v next %d", lines, next)
	}

	// new lines advance the cursor strictly
	_, last2 := appendBotLines(botID, []string{"second run line"}, nil)
	if last2 != last+1 {
		t.Errorf("new line should extend cursor: %d vs %d", last2, last+1)
	}

	// refill dedup: repeated bot_logs jobs against the same window must not
	// duplicate ring content (journald since-window + overlap guard).
	lines2, next2, err := botConsoleLines(context.Background(), BotUnitName(botID), botID, 200, 0, nil)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	lines3, next3, err := botConsoleLines(context.Background(), BotUnitName(botID), botID, 200, 0, nil)
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	if next2 != next3 || len(lines2) != len(lines3) {
		t.Errorf("repeated snapshot drifted: next %d/%d lines %d/%d", next2, next3, len(lines2), len(lines3))
	}
}

// --- git credential env: token stays out of argv ---

func TestGitCredEnv(t *testing.T) {
	env := gitCredEnv("https://github.com/org/repo.git", "ghp_secrettoken123")
	if len(env) != 2 {
		t.Fatalf("env = %v", env)
	}
	if !strings.Contains(env[1], "x-access-token:ghp_secrettoken123") {
		t.Errorf("rewrite env missing token: %v", env[1])
	}
	if gitCredEnv("git@github.com:org/repo.git", "tok") != nil {
		t.Error("ssh remotes need no credential env")
	}
	if gitCredEnv("https://github.com/o/r", "") != nil {
		t.Error("no token = no env")
	}
}

func TestSanitizeRepoURL(t *testing.T) {
	in := "https://user:pass@github.com/org/repo.git"
	out := sanitizeRepoURL(in)
	if strings.Contains(out, "pass") {
		t.Errorf("credentials leaked: %q", out)
	}
	if sanitizeRepoURL("https://github.com/o/r") != "https://github.com/o/r" {
		t.Error("clean URL unchanged")
	}
}

// --- discord envelope outcome payloads marshal cleanly (wire contract) ---

func TestOutcomePayloadShapes(t *testing.T) {
	b, err := json.Marshal(discord.BotStatusOutcome{BotID: "b", UnitState: "active", UptimeS: 10, Restarts: 2})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["unit_state"] != "active" || back["restarts"] != float64(2) {
		t.Errorf("payload shape = %v", back)
	}
}

package websites

import (
	"strings"
	"testing"
)

// encryptAppEnv / decryptAppEnv round trip; nil env must leave the stored
// blob untouched (nil cipher = no write).
func TestAppEnvRoundTrip(t *testing.T) {
	cipher, apiErr := encryptAppEnv(map[string]string{"DATABASE_URL": "postgres://x", "NODE_ENV": "production"})
	if apiErr != nil {
		t.Fatalf("encrypt: %v", apiErr)
	}
	if cipher == nil || len(cipher) == 0 {
		t.Fatal("nil cipher for non-nil env")
	}
	env, err := decryptAppEnv(cipher)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if env["NODE_ENV"] != "production" || env["DATABASE_URL"] != "postgres://x" {
		t.Errorf("round trip lost values: %v", env)
	}
	// nil env → nil cipher → caller skips SetAppEnv entirely.
	if c, _ := encryptAppEnv(nil); c != nil {
		t.Error("nil env must produce nil cipher")
	}
}

// Env keys follow the agent's app.env rules; bad keys are rejected
// control-plane side (defense in depth — the agent re-validates).
func TestValidEnvKey(t *testing.T) {
	for _, k := range []string{"DATABASE_URL", "PORT", "A1_B2", "x"} {
		if !validEnvKey(k) {
			t.Errorf("key %q should be valid", k)
		}
	}
	for _, k := range []string{"", "MY KEY", "DASH-KEY", "DOT.KEY", strings.Repeat("A", 65)} {
		if validEnvKey(k) {
			t.Errorf("key %q should be invalid", k)
		}
	}
}

// isAppRuntime gates every app endpoint and the payload builder.
func TestIsAppRuntime(t *testing.T) {
	for _, rt := range []Runtime{RuntimeNode, RuntimePython, RuntimeGo} {
		if !isAppRuntime(rt) {
			t.Errorf("runtime %s should be app-mode", rt)
		}
	}
	for _, rt := range []Runtime{RuntimePHP, RuntimeStatic} {
		if isAppRuntime(rt) {
			t.Errorf("runtime %s must not be app-mode", rt)
		}
	}
}

// Secrets handling for Minecraft instances: the RCON password is generated
// panel-side, encrypted at rest (secretbox AES-GCM), written to
// server.properties as 0600 by the agent, and NEVER rendered in logs or API
// responses. Every console line that passes through the ring is scrubbed
// against the password, and the control plane scrubs again at the API edge.
package minecraft

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// MinScrubValueLen — shorter values produce false-positive redactions.
const MinScrubValueLen = 8

// GenerateRCONPassword produces a 32-char URL-safe random password.
func GenerateRCONPassword() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// EncryptRCONPassword seals the password (base64 ciphertext for TEXT col).
func EncryptRCONPassword(pw string) (string, error) {
	if pw == "" {
		return "", nil
	}
	ct, err := secretbox.Encrypt(pw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// DecryptRCONPassword opens the stored ciphertext; "" stays "".
func DecryptRCONPassword(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	return secretbox.Decrypt(raw)
}

// SecretValues returns the values that must never appear in logs or API
// payloads (the RCON password). Keys stay visible where they exist; only
// values are secret.
func SecretValues(rconPassword string) []string {
	if len(rconPassword) >= MinScrubValueLen {
		return []string{rconPassword}
	}
	return nil
}

const redacted = "[redacted]"

// ScrubText removes every occurrence of the given secret values from a log
// line (exact, URL-escaped and base64 forms). Secrets must never leak
// through console output.
func ScrubText(text string, secrets []string) string {
	if text == "" || len(secrets) == 0 {
		return text
	}
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(text, s) {
			text = strings.ReplaceAll(text, s, redacted)
		}
		if esc := url.QueryEscape(s); esc != s && strings.Contains(text, esc) {
			text = strings.ReplaceAll(text, esc, redacted)
		}
		if b64 := base64.StdEncoding.EncodeToString([]byte(s)); len(b64) >= MinScrubValueLen && strings.Contains(text, b64) {
			text = strings.ReplaceAll(text, b64, redacted)
		}
	}
	return text
}

// envKeyRe kept for property key sanity (same shell-safe shape).
var envKeyRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

// validPropKey fast path used by ValidateProperty (service.go) internals.
func validPropKey(k string) bool { return envKeyRe.MatchString(k) }

// MarshalProps is a small helper for JSON maps of properties.
func MarshalProps(props map[string]string) (string, error) {
	if len(props) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(props)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

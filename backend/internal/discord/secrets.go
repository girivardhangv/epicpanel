// Secrets handling for Discord bots: env vars and git tokens are encrypted
// at rest (secretbox AES-GCM), injected at unit start via a 0600
// EnvironmentFile, and NEVER rendered in logs or API responses. Every log
// line that passes through the console ring is scrubbed against the known
// secret values, and the control plane scrubs again at the API edge.
package discord

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// MaxEnvVars and value bounds keep the encrypted blob small and the
// EnvironmentFile writable in one pass.
const (
	MaxEnvVars       = 64
	MaxEnvKeyLen     = 64
	MaxEnvValueLen   = 4096
	MinScrubValueLen = 8 // shorter values produce false-positive redactions
)

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateEnvKey enforces shell-safe env keys (Systemd EnvironmentFile).
func ValidateEnvKey(k string) error {
	if k == "" || len(k) > MaxEnvKeyLen {
		return errEnv("environment variable key length invalid")
	}
	if !envKeyRe.MatchString(k) {
		return errEnv("invalid environment variable key: " + k)
	}
	return nil
}

// ValidateEnvValue strips newline injection and enforces a size bound.
// The sanitized value is returned; callers store the sanitized form.
func ValidateEnvValue(v string) (string, error) {
	v = strings.ReplaceAll(v, "\n", " ")
	v = strings.ReplaceAll(v, "\r", " ")
	if len(v) > MaxEnvValueLen {
		return "", errEnv("environment variable value too large (max 4096 bytes)")
	}
	if !utf8.ValidString(v) {
		return "", errEnv("environment variable value must be valid UTF-8")
	}
	return v, nil
}

type envError struct{ msg string }

func (e *envError) Error() string { return e.msg }

func errEnv(msg string) error { return &envError{msg: msg} }

// AsEnvError reports whether err came from env validation (API mapping).
func AsEnvError(err error) bool {
	_, ok := err.(*envError)
	return ok
}

// EncryptEnv seals the whole env map into one ciphertext (base64 text for
// the TEXT column). A nil/empty map stores "" (no env).
func EncryptEnv(vars map[string]string) (string, error) {
	if len(vars) == 0 {
		return "", nil
	}
	if len(vars) > MaxEnvVars {
		return "", errEnv("too many environment variables (max 64)")
	}
	clean := make(map[string]string, len(vars))
	for k, v := range vars {
		if err := ValidateEnvKey(k); err != nil {
			return "", err
		}
		sanitized, err := ValidateEnvValue(v)
		if err != nil {
			return "", err
		}
		clean[k] = sanitized
	}
	plain, err := json.Marshal(clean)
	if err != nil {
		return "", err
	}
	ct, err := secretbox.Encrypt(string(plain))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// DecryptEnv opens the stored ciphertext. Empty storage = empty map.
func DecryptEnv(enc string) (map[string]string, error) {
	if enc == "" {
		return map[string]string{}, nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	plain, err := secretbox.Decrypt(raw)
	if err != nil {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(plain), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EncryptGitToken seals a git access token; "" stays "".
func EncryptGitToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	ct, err := secretbox.Encrypt(token)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// DecryptGitToken opens a stored git token; "" stays "".
func DecryptGitToken(enc string) (string, error) {
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
// payloads: every env value long enough to redact safely, plus the git
// token. The KEY names stay visible (customers need to know what they set);
// only values are secret.
func SecretValues(vars map[string]string, gitToken string) []string {
	out := make([]string, 0, len(vars)+1)
	for _, v := range vars {
		if len(v) >= MinScrubValueLen {
			out = append(out, v)
		}
	}
	if len(gitToken) >= MinScrubValueLen {
		out = append(out, gitToken)
	}
	return out
}

const redacted = "[redacted]"

// ScrubText removes every occurrence of the given secret values from a log
// line (exact, URL-escaped and base64 forms). Secrets must never leak
// through console output — bots routinely print their own tokens.
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

// MaskedEnv is the API shape for env vars: keys only, values never leave
// the server (write-only API).
type MaskedEnv struct {
	Keys []string `json:"keys"`
}

// MaskEnv projects an env map to its keys (sorted).
func MaskEnv(vars map[string]string) MaskedEnv {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return MaskedEnv{Keys: keys}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// EpisodeWindow bounds how long a crash-recovery episode counts restarts
// before the counter resets (a healthy run longer than this starts fresh).
const EpisodeWindow = 15 * time.Minute

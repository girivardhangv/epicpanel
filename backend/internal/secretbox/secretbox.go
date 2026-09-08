package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var ErrNotConfigured = errors.New("secret key unavailable; credentials cannot be encrypted or recovered")

var (
	mu     sync.Mutex
	key    []byte
	warned bool
)

// keyFile persists the encryption key so credentials survive restarts even
// when EPICPANEL_SECRET_KEY is not exported (dev runs, manual restarts).
const keyFile = "/etc/epicpanel/secret.key"

// Key returns the 32-byte encryption key:
//  1. EPICPANEL_SECRET_KEY env (hex), persisted to the key file for future
//     restarts that omit the env var;
//  2. the key file itself (/etc/epicpanel/secret.key);
//  3. a newly generated key written to the key file.
func Key() ([]byte, error) {
	mu.Lock()
	defer mu.Unlock()
	if key != nil {
		return key, nil
	}
	if v := os.Getenv("EPICPANEL_SECRET_KEY"); v != "" {
		b, err := hex.DecodeString(v)
		if err != nil || len(b) != 32 {
			return nil, errors.New("EPICPANEL_SECRET_KEY must be 64 hex chars (32 bytes)")
		}
		key = b
		persistKey(b) // keep the file in sync so env-less restarts still decrypt
		return key, nil
	}
	if b, err := os.ReadFile(keyFile); err == nil {
		if k, derr := hex.DecodeString(strings.TrimSpace(string(b))); derr == nil && len(k) == 32 {
			key = k
			return key, nil
		}
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if !persistKey(b) {
		key = b
		if !warned {
			slog.Warn("EPICPANEL_SECRET_KEY not set and key file unwritable; using ephemeral key — stored credentials will be unrecoverable after restart")
			warned = true
		}
		return key, nil
	}
	key = b
	return key, nil
}

// persistKey writes the hex key to the key file (0600). Best effort.
func persistKey(b []byte) bool {
	if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
		return false
	}
	if err := os.WriteFile(keyFile, []byte(hex.EncodeToString(b)+"\n"), 0o600); err != nil {
		return false
	}
	return true
}

func Encrypt(plaintext string) ([]byte, error) {
	k, err := Key()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func Decrypt(ciphertext []byte) (string, error) {
	k, err := Key()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// Fingerprint is a short non-reversible identifier of a secret (change detection).
func Fingerprint(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:8])
}

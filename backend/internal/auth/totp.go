// Package auth — RFC 6238 TOTP (SHA-1, 6 digits, 30s step) implemented on the
// standard library only: no new dependency for the 2FA foundation.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a fresh 20-byte base32 secret.
func GenerateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// TOTPCode computes the 6-digit code for a base32 secret at time t.
func TOTPCode(secretB32 string, t time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secretB32, " ", "")))
	if err != nil {
		return "", fmt.Errorf("invalid secret: %w", err)
	}
	return hotp(key, t.Unix()/30), nil
}

// VerifyTOTP accepts the code for t-30s, t, t+30s (clock skew tolerance).
// Comparison is constant-time.
func VerifyTOTP(secretB32, code string, t time.Time) bool {
	if len(code) != 6 {
		return false
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.ReplaceAll(secretB32, " ", "")))
	if err != nil {
		return false
	}
	ok := false
	for _, step := range []int64{-1, 0, 1} {
		want := hotp(key, t.Unix()/30+step)
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			ok = true
		}
	}
	return ok
}

// hotp is RFC 4226 (HMAC-SHA1, dynamic truncation, 6 digits).
func hotp(key []byte, counter int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[off]&0x7f) << 24) | (uint32(sum[off+1]) << 16) | (uint32(sum[off+2]) << 8) | uint32(sum[off+3])
	return fmt.Sprintf("%06d", bin%1000000)
}

// TOTPProvisioningURI renders the otpauth:// URI authenticator apps scan.
func TOTPProvisioningURI(secret, email, issuer string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30",
		issuer, email, secret, issuer)
}

// GenerateRecoveryCodes returns 10 single-use codes (shown once; only their
// SHA-256 hashes are stored).
func GenerateRecoveryCodes() ([]string, error) {
	codes := make([]string, 10)
	for i := range codes {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		codes[i] = fmt.Sprintf("%s-%s", b32.EncodeToString(b[:5]), b32.EncodeToString(b[5:]))
	}
	return codes, nil
}

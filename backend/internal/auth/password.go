// Argon2id password hashing in the de-facto standard PHC string format:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt-b64>$<hash-b64>
//
// Parameters can be upgraded over time; Verify transparently reports whether
// a stored hash should be re-hashed with stronger parameters.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	defaultMemoryKiB uint32 = 19456 // 19 MiB, OWASP baseline
	defaultTime      uint32 = 2
	defaultThreads   uint8  = 1
	saltLen                 = 16
	keyLen                  = 32
)

type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

func DefaultParams() Params {
	return Params{MemoryKiB: defaultMemoryKiB, Time: defaultTime, Threads: defaultThreads}
}

var (
	ErrInvalidHash = errors.New("password: invalid hash format")
	ErrMismatch    = errors.New("password: hash does not match")
)

// Hash derives a PHC-formatted Argon2id digest of plain.
func Hash(plain string) (string, error) {
	return HashWithParams(plain, DefaultParams())
}

func HashWithParams(plain string, p Params) (string, error) {
	if p.MemoryKiB == 0 {
		p.MemoryKiB = defaultMemoryKiB
	}
	if p.Time == 0 {
		p.Time = defaultTime
	}
	if p.Threads == 0 {
		p.Threads = defaultThreads
	}

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(plain), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	return encode(p, salt, key), nil
}

func encode(p Params, salt, key []byte) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key))
}

// Verify checks plain against a PHC-formatted Argon2id hash in constant time.
// The second return value is true when the hash was produced with weaker
// parameters than current defaults (candidates for re-hash on next login).
func Verify(hash, plain string) (ok bool, needsUpgrade bool, err error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, false, fmt.Errorf("%w: bad version field", ErrInvalidHash)
	}

	p := Params{}
	var t, m int64
	n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p.Threads)
	if err != nil || n != 3 {
		return false, false, fmt.Errorf("%w: bad parameter field", ErrInvalidHash)
	}
	p.MemoryKiB = uint32(m)
	p.Time = uint32(t)

	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, false, fmt.Errorf("%w: bad salt", ErrInvalidHash)
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, false, fmt.Errorf("%w: bad digest", ErrInvalidHash)
	}
	if len(want) < 16 {
		return false, false, fmt.Errorf("%w: digest too short", ErrInvalidHash)
	}

	got := argon2.IDKey([]byte(plain), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	needsUpgrade = version != argon2.Version || p.MemoryKiB < defaultMemoryKiB || p.Time < defaultTime
	return true, needsUpgrade, nil
}

// ValidatePolicy enforces the configured strength requirements.
// classes counts how many character groups are present:
// lowercase, uppercase, digits, symbols. It returns a list of human-readable
// problems; an empty list means the password satisfies policy.
func ValidatePolicy(plain string, minLength, minClasses int) []string {
	var problems []string
	if len(plain) < minLength {
		problems = append(problems, strconv.Itoa(minLength)+" characters minimum")
	}
	classes := 0
	classPresence := [4]bool{
		strings.ContainsAny(plain, "abcdefghijklmnopqrstuvwxyz"),
		strings.ContainsAny(plain, "ABCDEFGHIJKLMNOPQRSTUVWXYZ"),
		strings.ContainsAny(plain, "0123456789"),
		hasSymbol(plain),
	}
	for _, present := range classPresence {
		if present {
			classes++
		}
	}
	if classes < minClasses {
		problems = append(problems, "use at least "+strconv.Itoa(minClasses)+" character classes (upper, lower, digits, symbols)")
	}
	return problems
}

func hasSymbol(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return true
		}
	}
	return false
}

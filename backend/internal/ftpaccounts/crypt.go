package ftpaccounts

import (
	"crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"errors"
	"hash"
	"math/big"
	"strconv"
	"strings"
)

// Pure-Go glibc-style SHA-512-crypt ($6$, 5000 rounds default) so the agent
// can feed useradd -p directly. No external dependencies.

const (
	cryptRoundsDefault = 5000
	cryptRoundsMin     = 1000
	cryptRoundsMax     = 999_999_999
	maxSaltLen         = 16
	cryptB64Alphabet   = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

var errBadHash = errors.New("malformed sha512-crypt hash")

// emitOrder is the glibc output permutation for SHA-512: 21 groups of three
// digest bytes (encoded LSB-first in 4 chars each) plus the last byte.
var emitOrder = [21][3]int{
	{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45},
	{25, 46, 4}, {47, 5, 26}, {6, 27, 48}, {28, 49, 7},
	{50, 8, 29}, {9, 30, 51}, {31, 52, 10}, {53, 11, 32},
	{12, 33, 54}, {34, 55, 13}, {56, 14, 35}, {15, 36, 57},
	{37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19},
	{62, 20, 41},
}

// HashPassword derives a "$6$salt$hash" SHA-512-crypt string with a random
// 16-character salt. The salt makes the output one-way and unsuitable for
// plaintext recovery, so it is safe to include in job payloads.
func HashPassword(password string) (string, error) {
	salt, err := randomSalt(maxSaltLen)
	if err != nil {
		return "", err
	}
	digest := sha512CryptDigest([]byte(password), salt, cryptRoundsDefault)
	return "$6$" + string(salt) + "$" + encodeDigest(digest[:]), nil
}

// VerifyPassword recomputes the digest under the stored hash's own salt and
// rounds and compares in constant time. Only "$6$" settings are supported.
func VerifyPassword(hash, password string) bool {
	recomputed, err := cryptWithSetting(password, hash)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(recomputed), []byte(hash)) == 1
}

// cryptWithSetting computes the full hash for an explicit "$6$..." setting
// (supports the "rounds=N" prefix and >16-char salt truncation like glibc).
func cryptWithSetting(password, setting string) (string, error) {
	const prefix = "$6$"
	if !strings.HasPrefix(setting, prefix) {
		return "", errBadHash
	}
	rest := setting[len(prefix):]
	rounds := cryptRoundsDefault
	explicitRounds := false
	if r, ok := strings.CutPrefix(rest, "rounds="); ok {
		i := strings.IndexByte(r, '$')
		if i < 0 {
			return "", errBadHash
		}
		n, err := strconv.Atoi(r[:i])
		if err != nil || n < 0 {
			return "", errBadHash
		}
		rounds, rest, explicitRounds = clampRounds(n), r[i+1:], true
	}
	i := strings.IndexByte(rest, '$')
	if i < 0 {
		return "", errBadHash
	}
	salt := rest[:i]
	if len(salt) > maxSaltLen {
		salt = salt[:maxSaltLen]
	}
	full := sha512CryptDigest([]byte(password), []byte(salt), rounds)
	digest := encodeDigest(full[:])
	if explicitRounds {
		return prefix + "rounds=" + strconv.Itoa(rounds) + "$" + salt + "$" + digest, nil
	}
	return prefix + salt + "$" + digest, nil
}

func clampRounds(n int) int {
	switch {
	case n < cryptRoundsMin:
		return cryptRoundsMin
	case n > cryptRoundsMax:
		return cryptRoundsMax
	}
	return n
}

func randomSalt(n int) ([]byte, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(cryptB64Alphabet)))
	for i := range out {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return nil, err
		}
		out[i] = cryptB64Alphabet[idx.Int64()]
	}
	return out, nil
}

// sha512CryptDigest implements the sha512-crypt digest (Drepper spec as used
// by glibc): A = H(key+salt), mixed with B = H(key+salt+key) by key length
// and its bit representation, then rounds iterations over the P and S byte
// sequences.
func sha512CryptDigest(password, salt []byte, rounds int) [sha512.Size]byte {
	keyLen := len(password)
	saltLen := len(salt)
	var h hash.Hash

	h = sha512.New()
	h.Write(password)
	h.Write(salt)
	h.Write(password)
	digestB := h.Sum(nil)

	// Digest A context: seeded with key+salt, extended with the B sequence.
	h = sha512.New()
	h.Write(password)
	h.Write(salt)
	n := keyLen
	for n > sha512.Size {
		h.Write(digestB)
		n -= sha512.Size
	}
	h.Write(digestB[:n])
	for n = keyLen; n > 0; n >>= 1 {
		if n&1 != 0 {
			h.Write(digestB)
		} else {
			h.Write(password)
		}
	}
	altResult := h.Sum(nil)

	h = sha512.New()
	for i := 0; i < keyLen; i++ {
		h.Write(password)
	}
	pBytes := repeatTo(h.Sum(nil), keyLen)

	h = sha512.New()
	for i := 0; i < 16+int(altResult[0]); i++ {
		h.Write(salt)
	}
	sBytes := repeatTo(h.Sum(nil), saltLen)

	for cnt := 0; cnt < rounds; cnt++ {
		h = sha512.New()
		if cnt&1 != 0 {
			h.Write(pBytes)
		} else {
			h.Write(altResult)
		}
		if cnt%3 != 0 {
			h.Write(sBytes)
		}
		if cnt%7 != 0 {
			h.Write(pBytes)
		}
		if cnt&1 != 0 {
			h.Write(altResult)
		} else {
			h.Write(pBytes)
		}
		altResult = h.Sum(nil)
	}

	var out [sha512.Size]byte
	copy(out[:], altResult)
	return out
}

func repeatTo(src []byte, n int) []byte {
	out := make([]byte, n)
	for i := 0; i < n; i += len(src) {
		copy(out[i:], src)
	}
	return out
}

func encodeDigest(digest []byte) string {
	var sb strings.Builder
	sb.Grow(86)
	for _, tri := range emitOrder {
		emit24(&sb, digest[tri[0]], digest[tri[1]], digest[tri[2]], 4)
	}
	emit24(&sb, 0, 0, digest[sha512.Size-1], 2)
	return sb.String()
}

func emit24(sb *strings.Builder, b2, b1, b0 byte, n int) {
	w := uint32(b2)<<16 | uint32(b1)<<8 | uint32(b0)
	for i := 0; i < n; i++ {
		sb.WriteByte(cryptB64Alphabet[w&0x3f])
		w >>= 6
	}
}

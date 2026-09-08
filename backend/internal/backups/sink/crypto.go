// Encryption at rest for backup artifacts: per-backup data key wrapped via
// secretbox (AES-GCM, panel key), archive encrypted with the data key using
// chunked AES-GCM so multi-GB archives never sit in memory.
//
// Artifact format ("EPBK1"):
//   magic   "EPBK1\n"            (6 bytes)
//   chunk   u32 BE max plaintext size per chunk (for decoder sanity)
//   chunks  [u32 BE cipherLen][12B nonce][ciphertext+16B tag]...
//   the final chunk has cipherLen == tag-only (zero plaintext) as terminator
//   AAD for chunk i = 8-byte BE chunk index (reordering is detected).
package sink

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

// DataKeySize is the per-artifact data key size (AES-256).
const DataKeySize = 32

// DefaultChunkSize is the plaintext chunk size (4 MiB).
const DefaultChunkSize = 4 << 20

var magic = []byte("EPBK1\n")

// ErrCorrupt marks decryption failures (bad format, tampered chunks).
var ErrCorrupt = errors.New("backup artifact corrupt or wrong key")

// NewDataKey generates a fresh per-backup data key.
func NewDataKey() ([]byte, error) {
	k := make([]byte, DataKeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// WrapKey seals the raw data key with the panel key (secretbox). The sealed
// blob is what is stored in the backups row and shipped in job payloads.
func WrapKey(raw []byte) (string, error) {
	if len(raw) != DataKeySize {
		return "", fmt.Errorf("data key must be %d bytes", DataKeySize)
	}
	sealed, err := secretbox.Encrypt(string(raw))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// UnwrapKey opens a sealed data key (agent-side, transiently in memory).
func UnwrapKey(sealed string) ([]byte, error) {
	blob, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: data key blob: %v", ErrCorrupt, err)
	}
	plain, err := secretbox.Decrypt(blob)
	if err != nil {
		return nil, fmt.Errorf("%w: data key unwrap failed", ErrCorrupt)
	}
	return []byte(plain), nil
}

// EncryptFile encrypts src into dst using the data key (chunked AES-GCM).
func EncryptFile(key []byte, src, dst string) (int64, error) {
	if len(key) != DataKeySize {
		return 0, fmt.Errorf("data key must be %d bytes", DataKeySize)
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	n, err := encryptStream(key, in, out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

// DecryptFile decrypts src into dst using the data key.
func DecryptFile(key []byte, src, dst string) (int64, error) {
	if len(key) != DataKeySize {
		return 0, fmt.Errorf("data key must be %d bytes", DataKeySize)
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	n, err := decryptStream(key, in, out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}

func encryptStream(key []byte, r io.Reader, w io.Writer) (int64, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return 0, err
	}
	if _, err := w.Write(magic); err != nil {
		return 0, err
	}
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], DefaultChunkSize)
	if _, err := w.Write(sz[:]); err != nil {
		return 0, err
	}

	buf := make([]byte, DefaultChunkSize)
	var index uint64
	var total int64
	for {
		n, rerr := io.ReadFull(r, buf)
		if n > 0 {
			var idx [8]byte
			binary.BigEndian.PutUint64(idx[:], index)
			ct := gcm.Seal(nil, nonceFor(index), buf[:n], idx[:])
			var hdr [4]byte
			binary.BigEndian.PutUint32(hdr[:], uint32(len(ct)))
			if _, werr := w.Write(hdr[:]); werr != nil {
				return total, werr
			}
			if _, werr := w.Write(ct); werr != nil {
				return total, werr
			}
			total += int64(n)
			index++
		}
		if rerr == io.EOF || (rerr == io.ErrUnexpectedEOF && n == 0) {
			break
		}
		if rerr != nil && rerr != io.ErrUnexpectedEOF {
			return total, rerr
		}
		if rerr == io.ErrUnexpectedEOF {
			break
		}
	}
	// Terminator: zero-plaintext chunk (tag only), bound to the final index.
	var idx [8]byte
	binary.BigEndian.PutUint64(idx[:], index)
	term := gcm.Seal(nil, nonceFor(index), nil, idx[:])
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(term)))
	if _, err := w.Write(hdr[:]); err != nil {
		return total, err
	}
	if _, err := w.Write(term); err != nil {
		return total, err
	}
	return total, nil
}

func decryptStream(key []byte, r io.Reader, w io.Writer) (int64, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return 0, err
	}
	m := make([]byte, len(magic))
	if _, err := io.ReadFull(r, m); err != nil || string(m) != string(magic) {
		return 0, fmt.Errorf("%w: bad header", ErrCorrupt)
	}
	var sz [4]byte
	if _, err := io.ReadFull(r, sz[:]); err != nil {
		return 0, fmt.Errorf("%w: truncated header", ErrCorrupt)
	}
	if binary.BigEndian.Uint32(sz[:]) != DefaultChunkSize {
		return 0, fmt.Errorf("%w: unknown chunk size", ErrCorrupt)
	}

	var index uint64
	var total int64
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if err == io.EOF {
				return 0, fmt.Errorf("%w: missing terminator", ErrCorrupt)
			}
			return total, fmt.Errorf("%w: truncated chunk", ErrCorrupt)
		}
		clen := binary.BigEndian.Uint32(hdr[:])
		if clen < uint32(gcm.Overhead()) || clen > DefaultChunkSize+uint32(gcm.Overhead()) {
			return total, fmt.Errorf("%w: bad chunk length", ErrCorrupt)
		}
		ct := make([]byte, clen)
		if _, err := io.ReadFull(r, ct); err != nil {
			return total, fmt.Errorf("%w: truncated chunk body", ErrCorrupt)
		}
		var idx [8]byte
		binary.BigEndian.PutUint64(idx[:], index)
		plain, err := gcm.Open(nil, nonceFor(index), ct, idx[:])
		if err != nil {
			return total, fmt.Errorf("%w: chunk %d: tampered or wrong key", ErrCorrupt, index)
		}
		if len(plain) == 0 {
			return total, nil // terminator chunk
		}
		if _, err := w.Write(plain); err != nil {
			return total, err
		}
		total += int64(len(plain))
		index++
	}
}

// nonceFor derives the per-chunk nonce: random bytes are mixed with the
// chunk index which is also bound as AAD (double binding).
func nonceFor(index uint64) []byte {
	// Deterministic per (key, index): the key is unique per artifact, so a
	// static nonce prefix + counter is safe (never reused under one key).
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[4:], index)
	return n
}

// EncodeCreds seals the credentials struct (agent + control plane both hold
// the panel key).
func EncodeCreds(c Credentials) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sealed, err := secretbox.Encrypt(string(b))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// DecodeCreds opens a sealed credentials blob into c.
func DecodeCreds(sealed string, c *Credentials) error {
	blob, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return fmt.Errorf("%w: credentials blob: %v", ErrInvalid, err)
	}
	plain, err := secretbox.Decrypt(blob)
	if err != nil {
		return fmt.Errorf("%w: credentials unwrap failed", ErrInvalid)
	}
	return json.Unmarshal([]byte(plain), c)
}

package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// randomURLSafe returns n bytes of crypto/rand as a URL-safe string.
func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// mysqlRootExec runs one SQL statement via the mysql client over the local
// unix socket using root's socket auth (Debian/Ubuntu default). The SQL is
// formatted only from regex-validated identifiers and base64url passwords.
func (e *Executor) mysqlRootExec(ctx context.Context, format string, args ...any) error {
	sqlText := fmt.Sprintf(format, args...)
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "mysql", "-u", "root", "-e", sqlText)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mysql: %s (%w)", tail(out, 300), err)
	}
	return nil
}

func (e *Executor) mysqlRootQueryRow(ctx context.Context, dst *bool, format string, args ...any) error {
	sqlText := fmt.Sprintf(format, args...)
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "mysql", "-N", "-s", "-u", "root", "-e", sqlText)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	*dst = strings.TrimSpace(string(out)) == "1"
	return nil
}

// copyTree recursively copies src into dst (best-effort metadata, real errors).
func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		s := filepath.Join(src, entry.Name())
		d := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if err := copyTree(s, d); err != nil {
				return err
			}
			continue
		}
		b, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func uuidFromStr(s string) (interface{ String() string }, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, err
	}
	return id, nil
}

// syscallStatT aliases syscall.Stat_t for site ownership reads.
type syscallStatT = syscall.Stat_t

// userLookupID resolves a uid (or username) to a username.
func userLookupID(uidOrName string) (string, error) {
	u, err := user.LookupId(uidOrName)
	if err != nil {
		// maybe it's already a name
		if u2, err2 := user.Lookup(uidOrName); err2 == nil {
			return u2.Username, nil
		}
		return "", err
	}
	return u.Username, nil
}

// phpBinary resolves the CLI php for a minor version. Distro packages live
// in /usr/bin; panel-provisioned runtimes (runtime_php_source) symlink their
// versioned CLIs into /usr/local/bin. Falls back to the distro path so the
// caller's existence check reports "php X is not installed".
var phpBinaryDirs = []string{"/usr/bin", "/usr/local/bin"}

func phpBinary(minor string) string {
	for _, dir := range phpBinaryDirs {
		cand := filepath.Join(dir, "php"+minor)
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "/usr/bin/php" + minor
}

// cryptoRead wraps crypto/rand for token generation.
func cryptoRead(b []byte) (int, error) {
	return rand.Read(b)
}

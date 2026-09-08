package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ============================================================================
// SHARED CONFIG-SAFETY PIPELINE — atomic write -> validate -> rollback.
//
// Every config mutation performed by the web-server providers goes through
// these helpers: the new content is written atomically (tmp in the same
// directory + rename), the running service validates it, and on failure the
// previous content is restored so a bad change can never take the whole
// server down.
// ============================================================================

// AtomicWriteFile writes content to path atomically: the payload is staged in
// a temp file inside the same directory (so the rename is a same-filesystem
// operation) and renamed over the target.
func AtomicWriteFile(path string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName) // no-op after a successful rename
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("swap %s: %w", path, err)
	}
	return nil
}

// validateSwapTimeout bounds how long a single config validation may run.
const validateSwapTimeout = 120 * time.Second

// SwapValidated replaces the file at path with content after validate()
// succeeds. The current content is backed up in memory, written atomically,
// then validate() runs under a hard 120s timeout. On failure (or timeout) the
// previous content is restored — or the file removed when there was none — and
// the validation error is returned.
func SwapValidated(path string, content []byte, perm os.FileMode, validate func() error) error {
	backup, hadBackup := "", false
	if b, err := os.ReadFile(path); err == nil {
		backup = string(b)
		hadBackup = true
	}
	if err := AtomicWriteFile(path, content, perm); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- validate() }()
	select {
	case err := <-errCh:
		if err == nil {
			return nil
		}
		restoreFile(path, []byte(backup), hadBackup, perm)
		return fmt.Errorf("validation failed: %w", err)
	case <-time.After(validateSwapTimeout):
		restoreFile(path, []byte(backup), hadBackup, perm)
		return fmt.Errorf("validation timed out after %s", validateSwapTimeout)
	}
}

// restoreFile puts back the previous file content, or removes the file when
// there was none.
func restoreFile(path string, backup []byte, hadBackup bool, perm os.FileMode) {
	if hadBackup {
		_ = os.WriteFile(path, backup, perm)
		return
	}
	_ = os.Remove(path)
}

// ValidateCmd runs name+args (strict argv, never a shell) with a hard timeout
// and returns an error carrying the last 500 bytes of combined output.
func ValidateCmd(ctx context.Context, timeout time.Duration, name string, args ...string) error {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(c, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s (%w)", name, strings.Join(args, " "), tail(out, 500), err)
	}
	return nil
}
